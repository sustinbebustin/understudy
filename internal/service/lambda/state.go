package lambda

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 1

// State is the Lambda state of one account and region: its functions by
// name and its event source mappings by UUID. Records are values replaced
// on change; a value read out of a cell is always a copy.
type State struct {
	Functions map[string]Function `json:"functions,omitempty"`
	Mappings  map[string]Mapping  `json:"mappings,omitempty"`
	// Serial counts the identifiers the scope has minted (revision ids,
	// function URL ids); each is derived from the scope and its serial, so
	// none repeats until the scope is reset.
	Serial uint64 `json:"serial,omitzero"`
	// RetiredURLs are the ids of the function URLs deleted in the scope,
	// directly or with their alias or function, which a request then finds
	// forbidden rather than unknown, as on AWS.
	RetiredURLs map[string]bool `json:"retiredUrls,omitempty"`
}

func newState(aws.Scope) State {
	return State{Functions: map[string]Function{}, Mappings: map[string]Mapping{}}
}

// IsZero reports a scope with nothing in it, which snapshots skip. A scope
// that minted identifiers is not empty even once its functions are gone:
// its serial and retired URL ids keep later ids from repeating them.
func (s *State) IsZero() bool {
	return len(s.Functions) == 0 && len(s.Mappings) == 0 && s.Serial == 0 && len(s.RetiredURLs) == 0
}

// Partitioned finds IsZero and BlobRefs by type assertions; these keep a
// rename from silently dropping the skip or the blobs.
var (
	_ store.Zeroer     = (*State)(nil)
	_ store.BlobHolder = (*State)(nil)
)

// BlobRefs lists one entry per reference the state holds on a zip package:
// one per version whose code is a zip.
func (s *State) BlobRefs() []string {
	var refs []string
	for _, name := range slices.Sorted(maps.Keys(s.Functions)) {
		for _, ref := range s.Functions[name].zipRefs() {
			refs = append(refs, ref.String())
		}
	}
	return refs
}

// retireURLs records the ids of urls, which are being deleted.
func (s *State) retireURLs(urls ...URLConfig) {
	for _, u := range urls {
		if s.RetiredURLs == nil {
			s.RetiredURLs = map[string]bool{}
		}
		s.RetiredURLs[u.ID] = true
	}
}

// mint derives a new identifier of the scope from its serial and purpose:
// a revision id or a function URL id. Identifiers derived this way never
// repeat within the scope and draw from no identifier stream.
func (s *State) mint(scope aws.Scope, purpose string) [sha256.Size]byte {
	s.Serial++
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], s.Serial)
	return sha256.Sum256([]byte(purpose + "\x00" + scope.Account.String() + "\x00" + scope.Region.String() + "\x00" + string(n[:])))
}

// revision mints a RevisionId: a version 4 UUID.
func (s *State) revision(scope aws.Scope) string {
	h := s.mint(scope, "revision")
	h[6] = h[6]&0x0f | 0x40
	h[8] = h[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// urlID mints a function URL id: 32 lowercase letters and digits. Any web
// page a browser visits can send requests to a function URL, so, as on
// AWS, the id must not be guessable: it also hashes request, the id of the
// call creating the URL, which is random unless understudy runs under a
// seed.
func (s *State) urlID(scope aws.Scope, request ids.RequestID) string {
	h := s.mint(scope, "url\x00"+string(request))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:20]))
}

// Function is one function: its $LATEST version, its published versions
// and the function-level settings every version shares.
type Function struct {
	Name    string        `json:"name"`
	Created clock.Instant `json:"created"`
	Latest  Version       `json:"latest"`
	// Versions are the published versions, oldest first.
	Versions []Version `json:"versions,omitempty"`
	// LastVersion is the highest version number ever published; numbers
	// are never reused, even after the version is deleted.
	LastVersion int              `json:"lastVersion,omitzero"`
	Aliases     map[string]Alias `json:"aliases,omitempty"`
	// Policies are the resource policies by qualifier; "" is the
	// unqualified function's. A policy has no RevisionId of its own: it
	// shares its target's (revisionOf).
	Policies map[string]Policy `json:"policies,omitempty"`
	// Reserved is the reserved concurrency; nil when none is set.
	Reserved *int32            `json:"reserved,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	// URLs are the function URL configurations by qualifier; "" is the
	// unqualified function's.
	URLs map[string]URLConfig `json:"urls,omitempty"`
	// EventInvoke are the asynchronous invocation configurations by
	// qualifier; "$LATEST" also serves the unqualified function.
	EventInvoke map[string]EventInvokeConfig `json:"eventInvoke,omitempty"`
	// Generation counts code and configuration changes of $LATEST; an
	// execution environment of an older generation is not reused.
	Generation uint64 `json:"generation"`
	// Incarnation is minted when the function is created and never
	// changes, so the execution environments of a function deleted and
	// created again under its name, whose generations start over, are not
	// reused. Unlike a RevisionId, a permission change or a publish leaves
	// it as it is.
	Incarnation string `json:"incarnation,omitempty"`
}

// revisionOf is the RevisionId of what qualifier q names: $LATEST's for
// the unqualified function, else the version's or the alias's. A resource
// policy reports, and its changes are checked against, the RevisionId of
// its target (test_lambda_permission_revisions).
func (f Function) revisionOf(q string) (string, bool) {
	if a, ok := f.Aliases[q]; ok {
		return a.RevisionID, true
	}
	if q == "" {
		q = latest
	}
	v, ok := f.version(q)
	return v.RevisionID, ok
}

// withRevision returns a copy of f, sharing no map or slice with it, in
// which what qualifier q names, as revisionOf reads it, has RevisionId rev.
func (f Function) withRevision(q, rev string) Function {
	f = f.clone()
	if a, ok := f.Aliases[q]; ok {
		a.RevisionID = rev
		f.Aliases[q] = a
		return f
	}
	if q == "" {
		q = latest
	}
	if v, ok := f.version(q); ok {
		v.RevisionID = rev
		f = f.withVersion(v)
	}
	return f
}

// zipRefs lists the blob reference of every version whose code is a zip,
// and of the zip an update in progress replaced.
func (f Function) zipRefs() []blob.Ref {
	versions := f.all()
	codes := make([]Code, 0, len(versions))
	for i := range versions {
		codes = append(codes, versions[i].Code)
		if replaced := versions[i].replacedCode(); replaced != nil {
			codes = append(codes, replaced)
		}
	}
	return codeRefs(codes...)
}

// all lists $LATEST and then the published versions, oldest first.
func (f Function) all() []Version {
	return append([]Version{f.Latest}, f.Versions...)
}

// version returns the version numbered n ("$LATEST" or decimal).
func (f Function) version(n string) (Version, bool) {
	if n == latest {
		return f.Latest, true
	}
	i := slices.IndexFunc(f.Versions, func(v Version) bool { return v.Number == n })
	if i < 0 {
		return Version{}, false
	}
	return f.Versions[i], true
}

// withVersion returns a copy of f with v stored over the version with its
// number.
func (f Function) withVersion(v Version) Function {
	f = f.clone()
	if v.Number == latest {
		f.Latest = v
		return f
	}
	if i := slices.IndexFunc(f.Versions, func(x Version) bool { return x.Number == v.Number }); i >= 0 {
		f.Versions[i] = v
	}
	return f
}

// clone returns a copy that shares no map or slice with f, so a handler
// can change it and store it back as a new value.
func (f Function) clone() Function {
	f.Versions = slices.Clone(f.Versions)
	f.Aliases = maps.Clone(f.Aliases)
	f.Policies = maps.Clone(f.Policies)
	f.Tags = maps.Clone(f.Tags)
	f.URLs = maps.Clone(f.URLs)
	f.EventInvoke = maps.Clone(f.EventInvoke)
	return f
}

// latest is the version name of the unpublished, mutable version.
const latest = "$LATEST"

// latestPublished is the version PublishTo LATEST_PUBLISHED creates on a
// Lambda Managed Instances function. understudy has no managed instances,
// so no function has it.
const latestPublished = "$LATEST.PUBLISHED"

// Version is the configuration and code of $LATEST or of one published
// version, with its provisioning status.
type Version struct {
	// Number is "$LATEST" or the decimal version number.
	Number string `json:"number"`
	Config Config `json:"config"`
	Code   Code   `json:"code"`
	Status Status `json:"status"`
	// Modified is LastModified: the commit instant of the last change.
	Modified   clock.Instant `json:"modified"`
	RevisionID string        `json:"revisionId"`
	// LatestDescription is $LATEST's Description when PublishVersion's
	// Description replaced it in this version; nil when the version carries
	// $LATEST's own. Publishing compares $LATEST with what it was then.
	LatestDescription *string `json:"latestDescription,omitempty"`
}

// Code is where a version's code comes from.
//
//sumtype:decl
type Code interface{ isCode() }

// ZipCode is a zip package kept in the blob store.
type ZipCode struct {
	// Sha256 is CodeSha256: the base64 SHA-256 of the package.
	Sha256 string   `json:"sha256"`
	Size   int64    `json:"size"`
	Blob   blob.Ref `json:"blob"`
}

// ImageCode is a container image.
type ImageCode struct {
	URI string `json:"uri"`
	// Digest is the repository digest (or image ID) of the image present
	// locally; empty until the image is present.
	Digest string `json:"digest,omitempty"`
}

func (ZipCode) isCode()   {}
func (ImageCode) isCode() {}

// Status is a version's provisioning status: State and LastUpdateStatus
// together (lambda-api.md 5.1). Only Pending, Active and Failed are ever
// reported: the SDK waiters match no acceptor on the other states and
// would poll until they time out.
//
//sumtype:decl
type Status interface{ isStatus() }

// Creating is a version being provisioned (State Pending) until Settles,
// under TransitionsClock. It has no LastUpdateStatus yet.
type Creating struct {
	Settles clock.Instant `json:"settles"`
}

// PullingImage is an image version being created (State Pending) while
// its image is made present.
type PullingImage struct{}

// Active is a version that can be invoked, with the status of its last
// update.
type Active struct {
	Update Update `json:"update"`
}

// Failed is a version whose creation failed; it must be deleted and
// created again.
type Failed struct {
	ReasonCode string `json:"reasonCode"`
	Reason     string `json:"reason"`
}

func (Creating) isStatus()     {}
func (PullingImage) isStatus() {}
func (Active) isStatus()       {}
func (Failed) isStatus()       {}

// Update is an Active version's LastUpdateStatus.
//
//sumtype:decl
type Update interface{ isUpdate() }

// Successful is the status after a completed creation or update.
type Successful struct{}

// Updating is an update being applied (InProgress) until Settles, under
// TransitionsClock. Invocations run Serving until then: AWS keeps running
// a function's previous code and configuration until an update completes.
type Updating struct {
	Settles clock.Instant `json:"settles"`
	Serving Serving       `json:"serving"`
}

// Serving is what $LATEST was before an update in progress.
type Serving struct {
	Config Config `json:"config"`
	// Code is the code an UpdateFunctionCode replaced, which keeps its
	// package reference until the update completes; nil when the update
	// left the code as it was.
	Code Code `json:"code,omitempty"`
	// Generation is the function's Generation before the update, so the
	// warm environments of the previous code keep serving.
	Generation uint64 `json:"generation"`
}

// serving is what invocations of v run: v itself, or what it was while an
// update is in progress. generation is the function's current Generation,
// which a version that is not $LATEST ignores.
func (v Version) serving(generation uint64) (runs Version, runsGeneration uint64) {
	a, ok := v.Status.(Active)
	if !ok {
		return v, generation
	}
	u, ok := a.Update.(Updating)
	if !ok {
		return v, generation
	}
	v.Config = u.Serving.Config
	if u.Serving.Code != nil {
		v.Code = u.Serving.Code
	}
	return v, u.Serving.Generation
}

// replacedCode is the code an update in progress keeps for the
// invocations it serves, nil when there is none.
func (v Version) replacedCode() Code {
	if a, ok := v.Status.(Active); ok {
		if u, ok := a.Update.(Updating); ok {
			return u.Serving.Code
		}
	}
	return nil
}

// UpdatingImage is a code update to Pending's image, applied once the
// image is present; until then the version keeps its previous code.
type UpdatingImage struct {
	Pending ImageCode `json:"pending"`
}

// UpdateFailed is an update that could not be applied; the version keeps
// its previous code.
type UpdateFailed struct {
	ReasonCode string `json:"reasonCode"`
	Reason     string `json:"reason"`
}

func (Successful) isUpdate()    {}
func (Updating) isUpdate()      {}
func (UpdatingImage) isUpdate() {}
func (UpdateFailed) isUpdate()  {}

// Alias is a named pointer to a version, with optional weighted routing to
// a second version.
type Alias struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	// Weights maps the additional version to its share of invocations; nil
	// when the alias routes to one version.
	Weights    map[string]float64 `json:"weights,omitempty"`
	RevisionID string             `json:"revisionId"`
}

// Policy is a resource policy: its statements in insertion order.
type Policy struct {
	Statements []Statement `json:"statements"`
}

// Statement is one policy statement, kept as the JSON text AddPermission
// returned for it, so GetPolicy echoes it byte for byte.
type Statement struct {
	Sid  string `json:"sid"`
	JSON string `json:"json"`
}

// URLConfig is a function URL configuration.
type URLConfig struct {
	// ID is the url id minted at creation: the first label of the URL's
	// host, which never changes.
	ID       string `json:"id"`
	AuthType string `json:"authType"`
	// InvokeMode is what the caller set; empty means BUFFERED, which the
	// read operations report and CreateFunctionUrlConfig does not echo.
	InvokeMode string        `json:"invokeMode,omitempty"`
	Cors       *Cors         `json:"cors,omitempty"`
	Created    clock.Instant `json:"created"`
	Modified   clock.Instant `json:"modified"`
}

// Cors is a function URL's CORS configuration, stored and echoed.
type Cors struct {
	AllowCredentials *bool    `json:"allowCredentials,omitempty"`
	AllowHeaders     []string `json:"allowHeaders,omitempty"`
	AllowMethods     []string `json:"allowMethods,omitempty"`
	AllowOrigins     []string `json:"allowOrigins,omitempty"`
	ExposeHeaders    []string `json:"exposeHeaders,omitempty"`
	MaxAge           *int32   `json:"maxAge,omitempty"`
}

// EventInvokeConfig is an asynchronous invocation configuration, stored.
type EventInvokeConfig struct {
	MaximumRetryAttempts     *int32        `json:"maximumRetryAttempts,omitempty"`
	MaximumEventAgeInSeconds *int32        `json:"maximumEventAgeInSeconds,omitempty"`
	OnSuccess                string        `json:"onSuccess,omitempty"`
	OnFailure                string        `json:"onFailure,omitempty"`
	Modified                 clock.Instant `json:"modified"`
}

// stateCodec is the codec of State: the tagged-union tables of its sums.
// The tables are static, so an error building them is a bug in them; the
// codec then fails every section with it.
func stateCodec() store.Codec {
	c, err := unions()
	if err != nil {
		return store.Codec{
			Marshal:   func(any) ([]byte, error) { return nil, err },
			Unmarshal: func([]byte, any) error { return err },
		}
	}
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}

func unions() (jsonx.Codec, error) {
	code, err := jsonx.NewUnion("lambda.Code",
		jsonx.Variant[Code, ZipCode]("zip"),
		jsonx.Variant[Code, ImageCode]("image"),
	)
	if err != nil {
		return jsonx.Codec{}, fmt.Errorf("lambda state codec: %w", err)
	}
	statuses, err := jsonx.NewUnion("lambda.Status",
		jsonx.Variant[Status, Creating]("creating"),
		jsonx.Variant[Status, PullingImage]("pulling-image"),
		jsonx.Variant[Status, Active]("active"),
		jsonx.Variant[Status, Failed]("failed"),
	)
	if err != nil {
		return jsonx.Codec{}, fmt.Errorf("lambda state codec: %w", err)
	}
	updates, err := jsonx.NewUnion("lambda.Update",
		jsonx.Variant[Update, Successful]("successful"),
		jsonx.Variant[Update, Updating]("updating"),
		jsonx.Variant[Update, UpdatingImage]("updating-image"),
		jsonx.Variant[Update, UpdateFailed]("failed"),
	)
	if err != nil {
		return jsonx.Codec{}, fmt.Errorf("lambda state codec: %w", err)
	}
	scaling, err := jsonx.NewUnion("lambda.Scaling",
		jsonx.Variant[Scaling, Unbounded]("unbounded"),
		jsonx.Variant[Scaling, MaximumConcurrency]("maximum-concurrency"),
		jsonx.Variant[Scaling, ProvisionedPollers]("provisioned-pollers"),
	)
	if err != nil {
		return jsonx.Codec{}, fmt.Errorf("lambda state codec: %w", err)
	}
	return jsonx.NewCodec(code, statuses, updates, scaling), nil
}
