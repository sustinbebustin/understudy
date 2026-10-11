package lambda

import (
	"context"
	"encoding/base64"
	"reflect"
	"sort"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

func (s *Service) bindVersions(b lambdaapi.Binder) {
	b.PublishVersion(s.publishVersion, full())
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
	if description != nil {
		v.Config.Description = *description
	}
	f.LastVersion++
	f.Versions = append(f.Versions, v)
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

// unchanged reports a published version with config and code, its
// Description aside.
func unchanged(published *Version, config Config, code Code) bool {
	a, b := published.Config, config
	a.Description, b.Description = "", ""
	return reflect.DeepEqual(a, b) && codeSha256(published.Code) == codeSha256(code) && sameImage(published.Code, code)
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
	p, err := page(rows, func(r versionRow) string { return r.key }, in.MaxItems, listPageCap, in.Marker)
	if err != nil {
		return nil, err
	}
	return &lambdaapi.ListVersionsByFunctionOutput{Versions: configs(p.items), NextMarker: p.next}, nil
}

// listPage is one page of a listing and the marker of the next.
type listPage[T any] struct {
	items []T
	next  *string
}

// page returns the page of items, sorted by key, that starts at marker and
// holds at most limit items, and never more than capacity. The marker is
// opaque to clients: the base64 of the next item's key, so a listing that
// changes between pages neither repeats nor skips an item still present.
func page[T any](items []T, key func(T) string, limit *int32, capacity int, marker *string) (listPage[T], *awserr.Error) {
	start := 0
	if marker != nil {
		from, err := base64.RawURLEncoding.DecodeString(*marker)
		if err != nil {
			return listPage[T]{}, errInvalidMarker(*marker)
		}
		start = sort.Search(len(items), func(i int) bool { return key(items[i]) >= string(from) })
	}
	n := capacity
	if limit != nil {
		n = min(int(*limit), capacity)
	}
	end := min(start+n, len(items))
	p := listPage[T]{items: items[start:end]}
	if end < len(items) {
		p.next = new(base64.RawURLEncoding.EncodeToString([]byte(key(items[end]))))
	}
	return p, nil
}
