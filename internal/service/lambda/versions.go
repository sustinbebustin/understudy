package lambda

import (
	"context"
	"reflect"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/paging"
)

func (s *Service) bindVersions(b lambdaapi.Binder) {
	b.PublishVersion(s.publishVersion, partial(publishToRefused), kernel.WithCheck(func(in *lambdaapi.PublishVersionInput) *awserr.Error {
		return checkPublishTo("PublishVersion", in.PublishTo)
	}))
	b.ListVersionsByFunction(s.listVersionsByFunction, full())
}

func (s *Service) publishVersion(_ context.Context, c *kernel.Call, in *lambdaapi.PublishVersionInput) (*lambdaapi.FunctionConfiguration, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	if err := ref.unqualified(errQualifierNotAccepted("PublishVersion")); err != nil {
		return nil, err
	}
	out, err := write(s, c, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (lambdaapi.FunctionConfiguration, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return lambdaapi.FunctionConfiguration{}, err
		}
		if err := mutable(ref.arn(), &f.Latest); err != nil {
			return lambdaapi.FunctionConfiguration{}, err
		}
		if in.CodeSha256 != nil {
			if actual := codeSha256(f.Latest.Code); *in.CodeSha256 != actual {
				return lambdaapi.FunctionConfiguration{}, errCodeSha256Mismatch(*in.CodeSha256, actual)
			}
		}
		if in.RevisionId != nil && *in.RevisionId != f.Latest.RevisionID {
			return lambdaapi.FunctionConfiguration{}, errRevisionMismatch()
		}
		if err := s.retainForPublish(&f, now); err != nil {
			return lambdaapi.FunctionConfiguration{}, err
		}
		f = f.clone()
		v, added := s.publish(st, tx, ref.scope, &f, in.Description, now)
		if added {
			st.Functions[ref.name] = f
		} else {
			tx.Unchanged()
		}
		return renderConfig(ref.scope, &f, &v, ref.arn()+":"+v.Number, false), nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.arn())
	return &out, nil
}

// nextVersion is the code and status of the version publishing $LATEST of
// f adds: $LATEST's code, or the image an update of $LATEST waits for,
// which the new version then waits for too. adds is false when that code
// and $LATEST's configuration are the last published version's, which
// publishing then returns again (lambda-api.md 7.1).
func (s *Service) nextVersion(f *Function, now clock.Instant) (code Code, status Status, adds bool) {
	code, status = f.Latest.Code, s.initialStatus(f.Latest.Code, now)
	if pending, ok := pendingImage(&f.Latest); ok {
		code, status = pending, PullingImage{}
	}
	n := len(f.Versions)
	return code, status, n == 0 || !unchanged(&f.Versions[n-1], f.Latest.Config, code)
}

// retainForPublish retains the package reference of the zip version
// publishing $LATEST of f would add. It is the one step of publishing
// that can fail, so a write runs it before it changes the state; a write
// that changes the state first holds the reference before it starts
// (holdForPublish).
func (s *Service) retainForPublish(f *Function, now clock.Instant) *awserr.Error {
	code, _, adds := s.nextVersion(f, now)
	if z, ok := code.(ZipCode); ok && adds {
		if err := s.blobs.Retain(z.Blob); err != nil {
			return awserr.Internalf(err)
		}
	}
	return nil
}

// publish snapshots $LATEST of f as a new version (nextVersion), or
// returns the last published version when nothing changed since; added
// reports which. description overrides the version's Description. The
// caller already holds the package reference a new zip version takes
// (retainForPublish or holdForPublish), so publish cannot fail.
func (s *Service) publish(st *State, tx *kernel.Tx, scope aws.Scope, f *Function, description *string, now clock.Instant) (v Version, added bool) {
	code, status, adds := s.nextVersion(f, now)
	if !adds {
		return f.Versions[len(f.Versions)-1], false
	}
	v = Version{
		Number:     strconv.Itoa(f.LastVersion + 1),
		Config:     f.Latest.Config.clone(),
		Code:       code,
		Status:     status,
		Modified:   now,
		RevisionID: st.revision(scope),
	}
	if description != nil && *description != v.Config.Description {
		v.LatestDescription = new(v.Config.Description)
		v.Config.Description = *description
	}
	f.LastVersion++
	f.Versions = append(f.Versions, v)
	// Publishing changes $LATEST's RevisionId too, so a RevisionId read
	// before the publish no longer matches it
	// (test_function_revisions_version_and_alias).
	f.Latest.RevisionID = st.revision(scope)
	s.armProvisioning(tx, scope, f.Name, &v, now)
	return v, true
}

// pendingImage is the image an update of v waits for.
func pendingImage(v *Version) (ImageCode, bool) {
	if a, ok := v.Status.(Active); ok {
		if u, ok := a.Update.(UpdatingImage); ok {
			return u.Pending, true
		}
	}
	return ImageCode{}, false
}

// unchanged reports that $LATEST, with config and code, is what it was
// when published was published. Its Description counts as configuration:
// AWS publishes a new version after an update that changed only the
// Description (LocalStack's AWS-validated provider compares $LATEST's
// revision); the Description PublishVersion gave the version does not.
func unchanged(published *Version, config Config, code Code) bool {
	was := published.Config
	if published.LatestDescription != nil {
		was.Description = *published.LatestDescription
	}
	return reflect.DeepEqual(was, config) && codeSha256(published.Code) == codeSha256(code) && sameImage(published.Code, code)
}

// sameImage reports image code naming the same URI; zip code compares by
// digest alone.
func sameImage(a, b Code) bool {
	x, okA := a.(ImageCode)
	y, okB := b.(ImageCode)
	return okA == okB && (!okA || x.URI == y.URI)
}

// codeSha256 is the CodeSha256 AWS reports for code.
func codeSha256(code Code) string {
	switch code := code.(type) {
	case ZipCode:
		return code.Sha256
	case ImageCode:
		return imageSha256(code)
	}
	return ""
}

func (s *Service) listVersionsByFunction(_ context.Context, c *kernel.Call, in *lambdaapi.ListVersionsByFunctionInput) (*lambdaapi.ListVersionsByFunctionOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	rows, err := view(s, ref.scope, func(st *State) ([]versionRow, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return nil, err
		}
		return versionRows(ref.scope, &f, "", false), nil
	})
	if err != nil {
		return nil, err
	}
	p, err := page(listingVersions, rows, func(r versionRow) string { return r.key }, in.MaxItems, listPageCap, in.Marker)
	if err != nil {
		return nil, err
	}
	return &lambdaapi.ListVersionsByFunctionOutput{Versions: configs(p.Items), NextMarker: p.Next}, nil
}

// The Lambda listings, one per operation, each named inside the markers it
// issues so that every other listing refuses them.
const (
	listingFunctions           paging.Listing = "functions"
	listingVersions            paging.Listing = "versions"
	listingAliases             paging.Listing = "aliases"
	listingEventSourceMappings paging.Listing = "event-source-mappings"
	listingEventInvokeConfigs  paging.Listing = "event-invoke-configs"
	listingFunctionURLConfigs  paging.Listing = "function-url-configs"
)

// page returns the page of listing l over items, sorted by key with unique
// keys, that starts at marker and holds at most limit items, and never more
// than capacity. A marker no page of l issued is InvalidParameterValue
// rather than a silent seek.
func page[T any](l paging.Listing, items []T, key func(T) string, limit *int32, capacity int, marker *string) (paging.Page[T], *awserr.Error) {
	n := capacity
	if limit != nil {
		n = min(int(*limit), capacity)
	}
	p, err := paging.Keyset(l, items, key, n, marker)
	if err != nil {
		return paging.Page[T]{}, errInvalidMarker(err.Marker)
	}
	return p, nil
}
