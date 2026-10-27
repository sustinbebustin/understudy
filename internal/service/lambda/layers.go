package lambda

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The layer store (lambda-destinations-layers.md 6, 11.5.1): a scope's
// layers by name, each with its versions, their archives in the blob store
// and their permissions. The operations are in layer_ops.go.

// Layer is one layer of a scope and its versions that are not deleted.
type Layer struct {
	Name string `json:"name"`
	// LastVersion is the highest number ever published; the next publish is
	// LastVersion+1 even after every version is deleted
	// (lambda-destinations-layers.md finding 9).
	LastVersion int64                  `json:"lastVersion"`
	Versions    map[int64]LayerVersion `json:"versions,omitempty"`
}

// LayerVersion is one published version of a layer.
type LayerVersion struct {
	Number      int64  `json:"number"`
	Description string `json:"description"`
	// CompatibleRuntimes and CompatibleArchitectures are the lists as sent,
	// order kept: nil when not sent, which renders no member, and empty when
	// sent empty, which renders [] (lambda-destinations-layers.md 6.3:
	// test_lambda_api.snapshot.json:L8426-L8443). omitzero keeps the two
	// apart in a snapshot, where omitempty would drop an empty list.
	CompatibleRuntimes      []string `json:"compatibleRuntimes,omitzero"`
	CompatibleArchitectures []string `json:"compatibleArchitectures,omitzero"`
	// LicenseInfo is nil when not sent and "" when sent empty, which
	// renders as such (6.3, 11.7).
	LicenseInfo *string `json:"licenseInfo,omitzero"`
	// StorageMode is Content.S3ObjectStorageMode as sent, "" when it was
	// not. It is stored only: the archive is copied for REFERENCE as for
	// COPY (11.1).
	StorageMode string        `json:"storageMode,omitempty"`
	Created     clock.Instant `json:"created"`
	// Sha256 is CodeSha256: the base64 SHA-256 of the archive.
	Sha256 string `json:"sha256"`
	// Size is the archive's zipped size, Unzipped the sum of its entries'
	// uncompressed sizes.
	Size     int64    `json:"size"`
	Unzipped int64    `json:"unzipped"`
	Blob     blob.Ref `json:"blob"`
	// Policy is the version's statements in insertion order, and
	// PolicyRevision its RevisionId, "" while Policy is empty.
	Policy         []Statement `json:"policy,omitempty"`
	PolicyRevision string      `json:"policyRevision,omitempty"`
}

// blobRefs lists the blob reference of every version l holds.
func (l Layer) blobRefs() []blob.Ref {
	refs := make([]blob.Ref, 0, len(l.Versions))
	for _, n := range slices.Sorted(maps.Keys(l.Versions)) {
		refs = append(refs, l.Versions[n].Blob)
	}
	return refs
}

// clone returns a copy that shares no map with l.
func (l Layer) clone() Layer {
	l.Versions = maps.Clone(l.Versions)
	return l
}

// newest lists l's versions that match the filters, newest first.
func (l Layer) newest(runtime *lambdaapi.Runtime, arch *lambdaapi.Architecture) []LayerVersion {
	numbers := slices.Sorted(maps.Keys(l.Versions))
	slices.Reverse(numbers)
	var out []LayerVersion
	for _, n := range numbers {
		if v := l.Versions[n]; v.matches(runtime, arch) {
			out = append(out, v)
		}
	}
	return out
}

// matches reports whether a listing filtered by runtime and arch shows v:
// a filter matches only a version whose compatible list holds it
// (lambda-destinations-layers.md 6.5).
func (v LayerVersion) matches(runtime *lambdaapi.Runtime, arch *lambdaapi.Architecture) bool {
	return (runtime == nil || slices.Contains(v.CompatibleRuntimes, string(*runtime))) &&
		(arch == nil || slices.Contains(v.CompatibleArchitectures, string(*arch)))
}

// layerRef names a layer: the scope it lives in and its name.
type layerRef struct {
	scope aws.Scope
	name  string
}

// arn is the layer's ARN: arn:<partition>:lambda:<region>:<account>:layer:<name>.
func (r layerRef) arn() string {
	return "arn:" + r.scope.Partition.String() + ":lambda:" + r.scope.Region.String() + ":" + r.scope.Account.String() + ":layer:" + r.name
}

// versionARN is the ARN of the layer's version n.
func (r layerRef) versionARN(n int64) string { return r.arn() + ":" + strconv.FormatInt(n, 10) }

// parseLayerName reads a LayerName member: a bare name in the caller's
// scope, or the unversioned layer ARN, whose scope is used
// (lambda-destinations-layers.md 6.2). The generated check reads the
// model's ^arn...|name$ as two unanchored alternatives, as AWS's search
// semantics do, so a value such as a versioned ARN passes it; the name read
// from it is then one no layer has, and only PublishLayerVersion, which
// would store it, refuses it (isLayerName).
func parseLayerName(c *kernel.Call, s string) (layerRef, *awserr.Error) {
	if !strings.HasPrefix(s, "arn:") {
		return layerRef{scope: c.Scope(), name: s}, nil
	}
	a, err := aws.ParseARN(s)
	if err != nil {
		return layerRef{}, errLayerName(s)
	}
	scope, serr := aws.ScopeOf(a)
	name, ok := strings.CutPrefix(a.Resource, "layer:")
	if serr != nil || !ok {
		return layerRef{}, errLayerName(s)
	}
	return layerRef{scope: scope, name: name}, nil
}

// layerNameShape is a layer name a version can be published under: the
// name branch of the model's LayerName pattern, anchored.
var layerNameShape = regexp.MustCompile(`^[a-zA-Z0-9-_]+$`)

// isLayerName reports whether name can name a stored layer.
func isLayerName(name string) bool { return layerNameShape.MatchString(name) }

// parseLayerVersionARN reads GetLayerVersionByArn's Arn, which the generated
// check has matched against the model's anchored LayerVersionArn pattern:
// a version of a layer, or an AWS-managed awslayer ARN, which names no
// layer understudy holds (lambda-destinations-layers.md 8.4); ok is false
// for the latter.
func parseLayerVersionARN(s string) (layerRef, int64, bool) {
	a, err := aws.ParseARN(s)
	if err != nil {
		return layerRef{}, 0, false
	}
	rest, isLayer := strings.CutPrefix(a.Resource, "layer:")
	scope, serr := aws.ScopeOf(a)
	if !isLayer || serr != nil {
		return layerRef{}, 0, false
	}
	name, number, ok := strings.Cut(rest, ":")
	n, nerr := strconv.ParseInt(number, 10, 64)
	if !ok || nerr != nil {
		return layerRef{}, 0, false
	}
	return layerRef{scope: scope, name: name}, n, true
}

// checkLayerVersion is every layer operation's refusal of a version number
// below 1 (lambda-destinations-layers.md 6.8: recorded for Get and Delete,
// inferred for the permission operations).
func checkLayerVersion(n int64) *awserr.Error {
	if n < 1 {
		return errLayerVersionBelowOne()
	}
	return nil
}

// layerVersion finds version n of the layer ref names.
func (st *State) layerVersion(ref layerRef, n int64) (LayerVersion, bool) {
	v, ok := st.Layers[ref.name].Versions[n]
	return v, ok
}

// withLayerVersion stores v as a version of the layer ref names, which is
// created when missing.
func (st *State) withLayerVersion(ref layerRef, v LayerVersion) {
	l := st.Layers[ref.name].clone()
	l.Name = ref.name
	l.LastVersion = max(l.LastVersion, v.Number)
	if l.Versions == nil {
		l.Versions = map[int64]LayerVersion{}
	}
	l.Versions[v.Number] = v
	if st.Layers == nil {
		st.Layers = map[string]Layer{}
	}
	st.Layers[ref.name] = l
}

// render is v as GetLayerVersion and PublishLayerVersion answer it
// (lambda-destinations-layers.md 6.3, 6.4): Description always, the
// license and compatible lists only when sent, CreatedDate in a function's
// LastModified format, and never a signing ARN or ResolvedS3Object (11.8).
func (v LayerVersion) render(e kernel.Endpoint, ref layerRef) lambdaapi.GetLayerVersionResponse {
	return lambdaapi.GetLayerVersionResponse{
		Content: &lambdaapi.LayerVersionContentOutput{
			Location:   new(layerLocation(e, ref, v)),
			CodeSha256: new(v.Sha256),
			CodeSize:   v.Size,
		},
		LayerArn:                new(ref.arn()),
		LayerVersionArn:         new(ref.versionARN(v.Number)),
		Description:             new(v.Description),
		CreatedDate:             new(lastModified(v.Created)),
		Version:                 v.Number,
		CompatibleArchitectures: enumList[lambdaapi.Architecture](v.CompatibleArchitectures),
		CompatibleRuntimes:      enumList[lambdaapi.Runtime](v.CompatibleRuntimes),
		LicenseInfo:             clonePtr(v.LicenseInfo),
	}
}

// listItem is v as the listings show it: render's fields without LayerArn
// or Content (lambda-destinations-layers.md 6.5).
func (v LayerVersion) listItem(ref layerRef) lambdaapi.LayerVersionsListItem {
	return lambdaapi.LayerVersionsListItem{
		LayerVersionArn:         new(ref.versionARN(v.Number)),
		Version:                 v.Number,
		Description:             new(v.Description),
		CreatedDate:             new(lastModified(v.Created)),
		CompatibleArchitectures: enumList[lambdaapi.Architecture](v.CompatibleArchitectures),
		CompatibleRuntimes:      enumList[lambdaapi.Runtime](v.CompatibleRuntimes),
		LicenseInfo:             clonePtr(v.LicenseInfo),
	}
}

// enumList converts stored names to an enum list, keeping nil apart from
// empty.
func enumList[E ~string](names []string) []E {
	if names == nil {
		return nil
	}
	out := make([]E, len(names))
	for i, n := range names {
		out[i] = E(n)
	}
	return out
}

// nameList converts an enum list to the names stored, keeping nil apart
// from empty.
func nameList[E ~string](values []E) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	return new(*p)
}

// filterRuntimes is the runtime enum ListLayers and ListLayerVersions check
// CompatibleRuntime against, in AWS's order: an internal list, not the
// model's Runtime enum (lambda-destinations-layers.md 6.5:
// test_lambda_api.snapshot.json:L8296).
func filterRuntimes() []string {
	return []string{
		"ruby3.5", "ruby2.6", "dotnetcore1.0", "python3.7", "nodejs8.10", "nasa", "ruby2.7", "python2.7-greengrass",
		"dotnetcore2.0", "python3.8", "java21", "dotnet6", "dotnetcore2.1", "python3.9", "java11", "nodejs6.10",
		"provided", "dotnetcore3.1", "dotnet8", "java25", "java17", "nodejs", "nodejs4.3", "java8.al2",
		"go1.x", "dotnet10", "nodejs20.x", "go1.9", "byol", "nodejs10.x", "provided.al2023", "nodejs22.x",
		"python3.10", "java8", "nodejs12.x", "python3.11", "nodejs24.x", "nodejs8.x", "python3.12", "nodejs14.x",
		"nodejs8.9", "nodejs26.x", "python3.13", "python3.14", "nodejs16.x", "python3.15", "provided.al2", "nodejs4.3-edge",
		"nodejs18.x", "ruby3.2", "python3.4", "ruby3.3", "ruby3.4", "ruby2.5", "python3.6", "python2.7",
	}
}

// runtimeFilterViolation checks a listing's CompatibleRuntime, which the
// overlay leaves to the service, against filterRuntimes.
func runtimeFilterViolation(r *lambdaapi.Runtime) []awserr.Violation {
	if r == nil || slices.Contains(filterRuntimes(), string(*r)) {
		return nil
	}
	return []awserr.Violation{{
		Path:       awserr.Path{"compatibleRuntime"},
		Value:      awserr.RenderedText{Text: string(*r)},
		Constraint: awserr.Enum{Values: filterRuntimes()},
	}}
}

// validatePublishLayerVersion checks the compatible lists, which the
// overlay leaves to the service: a runtime outside the model's enum is
// refused with the 25-value creation list, the model's enum being the set
// accepted (deprecated runtimes included, finding 11), and an architecture
// outside it with the rolled-up element constraint, runtimes first
// (lambda-destinations-layers.md 6.3: test_lambda_api.snapshot.json:L8444-L8463).
func validatePublishLayerVersion(in *lambdaapi.PublishLayerVersionInput) []awserr.Violation {
	var vs []awserr.Violation
	if slices.ContainsFunc(in.CompatibleRuntimes, func(r lambdaapi.Runtime) bool { return !r.Known() }) {
		vs = append(vs, awserr.Violation{
			Path:       awserr.Path{"compatibleRuntimes"},
			Value:      awserr.RenderedText{Text: "[" + strings.Join(nameList(in.CompatibleRuntimes), ", ") + "]"},
			Constraint: awserr.Enum{Values: listedRuntimes()},
		})
	}
	return append(vs, architectureViolation("compatibleArchitectures", in.CompatibleArchitectures)...)
}

// layerPrincipal renders AddLayerVersionPermission's Principal as Lambda
// writes it (lambda-destinations-layers.md 6.7): "*" as the string "*", an
// account id or an account's root ARN as {"AWS": <root ARN>}. The model's
// pattern ^\d{12}|\*|arn:...:root$ is three unanchored alternatives, so a
// value that merely contains one passes the generated check; such a value
// names no principal and is refused with AddPermission's recorded text, as
// AWS's answer for a layer is unrecorded.
func layerPrincipal(scope aws.Scope, p string) (policydoc.PrincipalElement, *awserr.Error) {
	switch {
	case p == "*":
		return policydoc.Principal{Who: policydoc.Anyone{}}, nil
	case len(p) == 12 && isVersionNumber(p):
		return policydoc.Principal{Who: policydoc.Named{AWS: []string{aws.RootARN(scope.Partition, p).String()}}}, nil
	case rootARN.MatchString(p):
		return policydoc.Principal{Who: policydoc.Named{AWS: []string{p}}}, nil
	}
	return nil, errInvalidPrincipal()
}

// rootARN is the root ARN branch of the model's LayerPermissionAllowedPrincipal
// pattern, anchored.
var rootARN = regexp.MustCompile(`^arn:(aws[a-zA-Z-]*):iam::\d{12}:root$`)

// layerStatement is the statement AddLayerVersionPermission adds to the
// version versionARN names: Allow, the principal, the action and the
// version, and an organization's PrincipalOrgID condition
// (lambda-destinations-layers.md 6.7, 9.4).
func layerStatement(in *lambdaapi.AddLayerVersionPermissionInput, principal policydoc.PrincipalElement, versionARN string) policydoc.Statement {
	st := policydoc.Statement{
		Sid:       in.StatementId,
		Effect:    policydoc.Allow,
		Principal: principal,
		Action:    policydoc.Action{in.Action},
		Resource:  policydoc.Resource{versionARN},
	}
	if in.OrganizationId != nil {
		st.Condition = policydoc.Condition{{
			Operator: policydoc.Operator{Test: policydoc.StringEquals},
			Key:      "aws:PrincipalOrgID",
			Values:   []policydoc.Value{policydoc.StringValue(*in.OrganizationId)},
		}}
	}
	return st
}

// checkLayerPolicyRevision is AddLayerVersionPermission's and
// RemoveLayerVersionPermission's RevisionId precondition: rev, when given,
// must be the version policy's RevisionId; a version without statements has
// none, so any rev fails (lambda-destinations-layers.md 6.8).
func checkLayerPolicyRevision(v LayerVersion, rev *string) *awserr.Error {
	if rev != nil && (v.PolicyRevision == "" || *rev != v.PolicyRevision) {
		return errLayerPolicyRevisionMismatch()
	}
	return nil
}

// policyDocument is the version's policy as GetLayerVersionPolicy returns
// it; "" when it has no statement.
func (v LayerVersion) policyDocument() string {
	if len(v.Policy) == 0 {
		return ""
	}
	return Policy{Statements: v.Policy}.document()
}
