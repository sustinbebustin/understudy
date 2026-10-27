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

// layerLinkPrefix is where a layer version's Content.Location points, under
// codeLinkPrefix so the same raw operation serves it
// (lambda-destinations-layers.md 11.5.3).
const layerLinkPrefix = codeLinkPrefix + "layer/"

// layerLocation is the URL a layer version's archive downloads from:
// /_understudy/lambda-code/layer/<region>/<account>/<layer>/<version>/<sha>
// on the host the client addressed, the counterpart of codeLocation, where
// AWS answers a presigned S3 URL.
func layerLocation(e kernel.Endpoint, ref layerRef, v LayerVersion) string {
	return e.String() + layerLinkPrefix + ref.scope.Region.String() + "/" + ref.scope.Account.String() + "/" +
		url.PathEscape(ref.name) + "/" + strconv.FormatInt(v.Number, 10) + "/" + linkSha(v.Sha256)
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
// was made for; or a layer version's Content.Location link, while the
// version is not deleted. Any other link is ResourceNotFoundException. The
// link carries no credentials, as a presigned URL's signature is its own.
func (s *Service) downloadFunctionCode(_ context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	path := r.URL.EscapedPath()
	if strings.HasPrefix(path, layerLinkPrefix) {
		return s.downloadLayerArchive(c, path, w)
	}
	ref, sha, ok := s.parseCodeLink(path)
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
// not found. The body shares an origin with the admin prefix, so it is
// marked nosniff and sandboxed: a package crafted to sniff as HTML never
// runs script there.
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
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
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
	scope, ok := s.linkScope(parts[0], parts[1])
	if ferr != nil || verr != nil || !ok {
		return functionRef{}, "", false
	}
	return functionRef{scope: scope, name: function, qualifier: version}, parts[4], true
}

// linkScope is the scope holding Lambda state whose region and account a
// link names.
func (s *Service) linkScope(region, account string) (aws.Scope, bool) {
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		if scope.Region.String() == region && scope.Account.String() == account {
			return scope, true
		}
	}
	return aws.Scope{}, false
}

// downloadLayerArchive serves a link layerLocation made: the archive of the
// layer version it names, while that version is not deleted and its
// archive is the one the link was made for.
func (s *Service) downloadLayerArchive(c *kernel.Call, path string, w http.ResponseWriter) *awserr.Error {
	ref, n, sha, ok := s.parseLayerLink(path)
	if !ok {
		return errResourceNotFound()
	}
	v, err := view(s, ref.scope, func(st *State) (LayerVersion, *awserr.Error) {
		v, ok := st.layerVersion(ref, n)
		if !ok || linkSha(v.Sha256) != sha {
			return LayerVersion{}, errResourceNotFound()
		}
		return v, nil
	})
	if err != nil {
		return err
	}
	touch(c, ref.versionARN(n))
	return s.servePackage(w, v.Blob)
}

// parseLayerLink reads a link layerLocation made: the layer version it
// names, in a scope that holds Lambda state, and the archive's SHA-256.
func (s *Service) parseLayerLink(path string) (ref layerRef, version int64, sha string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(path, layerLinkPrefix), "/")
	if len(parts) != 5 {
		return layerRef{}, 0, "", false
	}
	name, nerr := url.PathUnescape(parts[2])
	n, verr := strconv.ParseInt(parts[3], 10, 64)
	scope, ok := s.linkScope(parts[0], parts[1])
	if nerr != nil || verr != nil || !ok {
		return layerRef{}, 0, "", false
	}
	return layerRef{scope: scope, name: name}, n, parts[4], true
}
