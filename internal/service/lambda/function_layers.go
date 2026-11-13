package lambda

import (
	"context"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Layers on functions (lambda-destinations-layers.md 7, 11.5.4):
// CreateFunction and UpdateFunctionConfiguration resolve a function's
// Layers against the layer store of layers.go when they are called, and the
// version keeps what they resolved.

// LayerRef is a layer version a function version uses, as resolved when it
// was attached: a later delete of the version changes nothing for the
// function (lambda-destinations-layers.md 7.2, 11.5.4).
type LayerRef struct {
	ARN string `json:"arn"`
	// CodeSize is the archive's zipped size, which Layers reports;
	// Unzipped the sum of its entries' uncompressed sizes, which counts
	// toward the function's unzipped limit.
	CodeSize int64  `json:"codeSize"`
	Sha256   string `json:"sha256"`
	Unzipped int64  `json:"unzipped"`
	// Blob is the archive. Every stored configuration listing the layer, a
	// version's or the one an update in progress serves, holds one reference
	// on it.
	Blob blob.Ref `json:"blob"`
}

// layerBlobs lists the blob reference each of layers holds.
func layerBlobs(layers []LayerRef) []blob.Ref {
	refs := make([]blob.Ref, 0, len(layers))
	for _, l := range layers {
		refs = append(refs, l.Blob)
	}
	return refs
}

// layerSwitches are the choices about layers on functions made without
// AWS-recorded evidence (lambda-destinations-layers.md 11.2), now recorded
// (12, 2026-11-03).
func layerSwitches() []fidelity.Switch[string] {
	return []fidelity.Switch[string]{
		// A path two of a function's layers hold has the later layer's
		// content in /opt (codestore.Store.Merge).
		recorded(ldlSwitch("lambda.layers.merge-conflict", "later-overwrites", evidence.AWSRecorded, "81-extraction-and-merge-order", "LDL-12",
			"lambda.Invoke")),
		// A layer marked for another runtime or architecture attaches: the
		// compatible lists are metadata ListLayers filters on (resolveLayers
		// checks neither).
		recorded(ldlSwitch("lambda.layers.compatibility", "not-enforced", evidence.AWSRecorded, "73-compatibility-is-metadata", "LDL-13",
			"lambda.CreateFunction", "lambda.UpdateFunctionConfiguration")),
		// Another account's layer version its owner's policy does not
		// grant the caller, or that does not exist, is refused with 403
		// AccessDeniedException, whatever --iam-enforce says
		// (readableLayerVersion). LDL-15 recorded CreateFunction and
		// GetLayerVersionByArn; UpdateFunctionConfiguration and
		// GetLayerVersion follow them.
		recorded(ldlSwitch("lambda.layers.cross-account-policy", "refuse", evidence.AWSRecorded, "74-layers-of-other-accounts", "LDL-15",
			"lambda.CreateFunction", "lambda.UpdateFunctionConfiguration", "lambda.GetLayerVersionByArn", "lambda.GetLayerVersion")),
	}
}

// layerOf is the layer a Layers entry, which the generated check has
// matched against the model's LayerVersionArn, is a version of: the ARN
// without its version, or the whole ARN for an AWS-managed awslayer, which
// has none.
func layerOf(arn string) string {
	if strings.Contains(arn, ":awslayer:") {
		return arn
	}
	if before, _, ok := strings.CutLast(arn, ":"); ok {
		return before
	}
	return arn
}

// checkLayers is the check of a function's Layers that needs no lookup: at
// most five, and no layer twice, in either version or the same one
// (lambda-destinations-layers.md 7.1: test_lambda_api.snapshot.json:L12313-L12329,
// L9336-L9371). The pair named is the first entry repeating a layer and
// the entry it repeats.
func checkLayers(layers []string) *awserr.Error {
	if len(layers) > maxLayers {
		return errTooManyLayers()
	}
	for i, arn := range layers {
		if j := slices.IndexFunc(layers[:i], func(earlier string) bool { return layerOf(earlier) == layerOf(arn) }); j >= 0 {
			return errLayerTwice(layers[j], arn)
		}
	}
	return nil
}

// attachment is a function's Layers resolved: one LayerRef per entry, in
// order, each holding one blob reference that the caller stores or
// releases.
type attachment struct {
	layers []LayerRef
}

// blobs lists the references a's layers hold.
func (a attachment) blobs() []blob.Ref { return layerBlobs(a.layers) }

// resolveLayers resolves the Layers caller gives a function in scope, in
// order: every entry in the function's region first, then each one a
// version understudy holds that caller may use (lambda-destinations-layers.md
// 7.1, 7.4, 11.5.6). Nothing is checked against the versions' compatible
// runtimes or architectures (switch lambda.layers.compatibility). On an
// error it holds nothing.
func (s *Service) resolveLayers(ctx context.Context, caller identity.Principal, scope aws.Scope, arns []string) (attachment, *awserr.Error) {
	for _, arn := range arns {
		if ref, _, ok := parseLayerVersionARN(arn); ok && ref.scope.Region != scope.Region {
			return attachment{}, errLayerRegion(scope.Region.String())
		}
	}
	var out attachment
	for _, arn := range arns {
		l, err := s.resolveLayer(ctx, caller, scope, arn)
		if err != nil {
			s.release(ctx, out.blobs()...)
			return attachment{}, err
		}
		out.layers = append(out.layers, l)
	}
	return out, nil
}

// resolveLayer finds the layer version arn names for caller's function in
// scope and takes one blob reference on its archive, under the layer cell's
// lock so a concurrent DeleteLayerVersion cannot free it first. A version
// of another account attaches only when its owner's policy grants scope's
// account (readableLayerVersion).
func (s *Service) resolveLayer(ctx context.Context, caller identity.Principal, scope aws.Scope, arn string) (LayerRef, *awserr.Error) {
	if a, err := aws.ParseARN(arn); err == nil && strings.HasPrefix(a.Resource, "awslayer:") {
		return LayerRef{}, errManagedLayerNotFound(arn)
	}
	ref, n, ok := parseLayerVersionARN(arn)
	if !ok {
		// A version number past int64 names no version understudy holds.
		return LayerRef{}, errLayerNotFound(arn)
	}
	return store.View2(s.cells, ref.scope, func(st *State) (LayerRef, *awserr.Error) {
		v, ok, err := st.readableLayerVersion(caller, scope, ref, n)
		switch {
		case err != nil:
			return LayerRef{}, err
		case !ok:
			return LayerRef{}, errLayerNotFound(arn)
		}
		if err := s.retain(ctx, v.Blob); err != nil {
			return LayerRef{}, err
		}
		return LayerRef{ARN: ref.versionARN(n), CodeSize: v.Size, Sha256: v.Sha256, Unzipped: v.Unzipped, Blob: v.Blob}, nil
	})
}

// readableLayerVersion finds version n of the layer ref names for caller,
// who calls in scope; ok is false for a missing version of scope's
// account. Another account's version its owner's policy does not grant
// scope's account, or that does not exist, is errLayerVersionDenied in
// every --iam-enforce mode, since AWS's resource-policy gate does not
// depend on the caller's own policies (lambda-destinations-layers.md 7.4:
// LDL-15 09, 10; switch lambda.layers.cross-account-policy).
func (st *State) readableLayerVersion(caller identity.Principal, scope aws.Scope, ref layerRef, n int64) (LayerVersion, bool, *awserr.Error) {
	v, ok := st.layerVersion(ref, n)
	if ref.scope.Account != scope.Account && (!ok || !v.grants(scope.Partition, scope.Account)) {
		return LayerVersion{}, false, errLayerVersionDenied(caller, ref.versionARN(n))
	}
	return v, ok, nil
}

// grants reports whether v's policy lets account use the version: an Allow
// for everyone or for the account, by id or root ARN. A statement limited
// to an organization counts, since the caller's organization is unknown.
func (v LayerVersion) grants(partition aws.Partition, account aws.AccountID) bool {
	doc := policydoc.ParseStored(v.policyDocument())
	if doc == nil {
		return false
	}
	root := aws.RootARN(partition, account.String()).String()
	for _, st := range doc.Statements {
		p, ok := st.Principal.(policydoc.Principal)
		if !ok || st.Effect != policydoc.Allow {
			continue
		}
		switch who := p.Who.(type) {
		case policydoc.Anyone:
			return true
		case policydoc.Named:
			if slices.ContainsFunc(who.AWS, func(a string) bool { return a == "*" || a == root || a == account.String() }) {
				return true
			}
		}
	}
	return false
}

// checkCodeWithLayers refuses a zip package whose unzipped size and its
// layers' together pass maxUnzipped, "250 MB unzipped including layers"
// (https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html;
// lambda-destinations-layers.md 7.1, 11.5.4).
func checkCodeWithLayers(code Code, layers []LayerRef) *awserr.Error {
	z, ok := code.(ZipCode)
	if !ok || len(layers) == 0 {
		return nil
	}
	total := z.Unzipped
	for _, l := range layers {
		total += l.Unzipped
	}
	if total > maxUnzipped {
		return errCodeWithLayersTooLarge()
	}
	return nil
}
