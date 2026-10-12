package lambda

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// codeLinkPrefix is where GetFunction's Code.Location points: the reserved
// prefix, which the edge routes to DownloadFunctionCode.
const codeLinkPrefix = "/_understudy/lambda-code/"

// codeLocation is the URL a zip version's package downloads from. AWS
// answers a presigned S3 URL (lambda-api.md 6.1); understudy serves the
// package itself, on the host the client addressed, at
// /_understudy/lambda-code/<region>/<account>/<function>/<version>/<sha>,
// where sha, the package's SHA-256 in unpadded base64url, keeps the link
// of an earlier package from serving a later one, as AWS's presigned URL
// names one object.
func codeLocation(e kernel.Endpoint, scope aws.Scope, function, version string, code ZipCode) string {
	return e.String() + codeLinkPrefix + scope.Region.String() + "/" + scope.Account.String() + "/" +
		url.PathEscape(function) + "/" + url.PathEscape(version) + "/" + linkSha(code.Sha256)
}

// linkSha is a CodeSha256 in a link's form: unpadded base64url.
func linkSha(codeSha256 string) string {
	raw, err := base64.StdEncoding.DecodeString(codeSha256)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// bindCodeLinks binds the raw operation behind Code.Location.
func (s *Service) bindCodeLinks(b lambdaapi.Binder) {
	b.DownloadFunctionCode(s.downloadFunctionCode, full())
}

// downloadFunctionCode serves a Code.Location link: the zip package of the
// version it names, while that version still runs the package the link
// was made for. Any other link is ResourceNotFoundException. The link
// carries no credentials, as a presigned URL's signature is its own.
func (s *Service) downloadFunctionCode(_ context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	ref, sha, ok := s.parseCodeLink(r.URL.EscapedPath())
	if !ok {
		return errResourceNotFound()
	}
	z, err := view(s, ref.scope, func(st *State) (ZipCode, *awserr.Error) {
		_, v, err := st.lookup(ref)
		if err != nil {
			return ZipCode{}, err
		}
		z, ok := v.Code.(ZipCode)
		if !ok || linkSha(z.Sha256) != sha {
			return ZipCode{}, errResourceNotFound()
		}
		return z, nil
	})
	if err != nil {
		return errResourceNotFound()
	}
	touch(c, ref.qualifiedARN())
	return s.servePackage(w, z.Blob)
}

// servePackage writes the package in ref as an application/zip body. A
// package released since the lookup, when its version was deleted, is
// not found.
func (s *Service) servePackage(w http.ResponseWriter, ref blob.Ref) *awserr.Error {
	f, err := s.blobs.Open(ref)
	if err != nil {
		return errResourceNotFound()
	}
	defer f.Close()
	size, err := f.Seek(0, io.SeekEnd)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		return awserr.Internalf(err)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f) //nolint:errcheck // the client went away; nothing is left to tell it
	return nil
}

// parseCodeLink reads a link codeLocation made: the version it names, in a
// scope that holds Lambda state, and the package's SHA-256.
func (s *Service) parseCodeLink(path string) (functionRef, string, bool) {
	rest, ok := strings.CutPrefix(path, codeLinkPrefix)
	if !ok {
		return functionRef{}, "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 5 {
		return functionRef{}, "", false
	}
	function, ferr := url.PathUnescape(parts[2])
	version, verr := url.PathUnescape(parts[3])
	if ferr != nil || verr != nil {
		return functionRef{}, "", false
	}
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		if scope.Region.String() == parts[0] && scope.Account.String() == parts[1] {
			return functionRef{scope: scope, name: function, qualifier: version}, parts[4], true
		}
	}
	return functionRef{}, "", false
}
