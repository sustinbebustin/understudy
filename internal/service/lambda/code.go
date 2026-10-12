package lambda

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The package limits of lambda-api.md 4.3.
const (
	maxZipped   = 52_428_800
	maxUnzipped = codestore.DefaultMaxUnzipped
)

// codeSource is the code a CreateFunction or UpdateFunctionCode names.
//
//sumtype:decl
type codeSource interface{ isCodeSource() }

// zipSource is a zip package sent inline.
type zipSource struct{ bytes []byte }

// s3Source is a package in an S3 bucket, at version when one is named.
type s3Source struct {
	bucket, key string
	version     *string
}

// imageSource is a container image.
type imageSource struct{ uri string }

func (zipSource) isCodeSource()   {}
func (s3Source) isCodeSource()    {}
func (imageSource) isCodeSource() {}

// sourceOf reads which code source the members name for a function of the
// package type: exactly one of ZipFile, S3Bucket with S3Key, or ImageUri,
// matching the type; S3ObjectVersion goes with an S3 location.
func sourceOf(packageType lambdaapi.PackageType, zipFile []byte, bucket, key, version, imageURI *string) (codeSource, *awserr.Error) {
	hasZip, hasS3, hasImage := zipFile != nil, bucket != nil || key != nil, imageURI != nil
	switch {
	case packageType == lambdaapi.PackageTypeImage && hasImage && !hasZip && !hasS3:
		return imageSource{uri: *imageURI}, nil
	case packageType == lambdaapi.PackageTypeZip && hasZip && !hasS3 && !hasImage:
		return zipSource{bytes: zipFile}, nil
	case packageType == lambdaapi.PackageTypeZip && hasS3 && bucket != nil && key != nil && !hasZip && !hasImage:
		return s3Source{bucket: *bucket, key: *key, version: version}, nil
	}
	return nil, errCodeSource(string(packageType))
}

// checkZip checks a package sent inline: at most maxZipped bytes, and an
// archive checkArchive accepts.
func checkZip(b []byte) *awserr.Error {
	if len(b) > maxZipped {
		return errZippedTooLarge()
	}
	return checkArchive(b)
}

// checkArchive checks that b is a zip archive whose entries claim at most
// maxUnzipped bytes; a package read from S3 has only this limit, since the
// zipped one is the API upload's (lambda-api.md 4.3). The archive is only
// read here; codestore extracts it, entry by entry and zip-slip safe, when
// an environment first runs it.
func checkArchive(b []byte) *awserr.Error {
	r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return errInvalidZip()
	}
	var total uint64
	for _, f := range r.File {
		total += f.UncompressedSize64
		if total > uint64(maxUnzipped) {
			return errUnzippedTooLarge()
		}
	}
	return nil
}

// storeZip keeps a checked zip package in the blob store. The returned
// code holds one blob reference, which the caller hands to the state or
// releases.
func (s *Service) storeZip(ctx context.Context, b []byte) (ZipCode, *awserr.Error) {
	if s.blobs == nil {
		return ZipCode{}, awserr.Internalf(errors.New("the Lambda service was built without a blob store, so it cannot keep function code; wire kernel.ServiceDeps.Blob"))
	}
	ref, size, err := s.blobs.Put(ctx, bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return ZipCode{}, awserr.Internalf(err)
	}
	sum := sha256.Sum256(b)
	return ZipCode{Sha256: base64.StdEncoding.EncodeToString(sum[:]), Size: size, Blob: ref}, nil
}

// release drops blob references a failed or replaced write no longer
// holds. A failure is logged: the blob then outlives its use until the
// next restart prunes it.
func (s *Service) release(ctx context.Context, refs ...blob.Ref) {
	for _, r := range refs {
		if err := s.blobs.Release(r); err != nil {
			s.k.Log().LogAttrs(ctx, slog.LevelWarn, "releasing a Lambda code package failed", slog.String("blob", r.String()), slog.String("error", err.Error()))
		}
	}
}

// codeRefs lists the blob references of the zip packages among codes.
func codeRefs(codes ...Code) []blob.Ref {
	var refs []blob.Ref
	for _, c := range codes {
		if z, ok := c.(ZipCode); ok {
			refs = append(refs, z.Blob)
		}
	}
	return refs
}

// imageFailure is why an image could not be made present, as the
// version's StateReasonCode and reason.
type imageFailure struct {
	code   string
	reason string
}

// onImage is TimerImage's handler: it starts making the version's image
// present as tracked work, which commits the outcome with the timer's
// call. A version no longer waiting for an image makes it a no-op.
func (s *Service) onImage(c *kernel.Call, key clock.TimerKey, _ clock.Instant) {
	ref, ok := parseVersionID(key.ID)
	if !ok {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "Lambda image timer names no function version", slog.String("timer", key.ID))
		return
	}
	cell, ok := s.cells.Lookup(ref.scope)
	if !ok {
		return
	}
	type wanted struct {
		uri, arch string
		ok        bool
	}
	w := store.Read(cell, func(st *State) wanted {
		v, ok := st.versionOf(ref)
		if !ok {
			return wanted{}
		}
		uri, ok := awaitedImage(&v)
		return wanted{uri: uri, arch: v.Config.Architecture, ok: ok}
	})
	if !w.ok {
		return
	}
	// The pull counts as InvocationRunning, the closed WorkKind set's only
	// kind for Lambda work in progress, so settling waits for it; status
	// and diagnostics therefore list an image pull as an invocation.
	s.k.Go(kernel.InvocationRunning, func(ctx context.Context) {
		digest, failure := s.ensureImage(ctx, w.uri, w.arch)
		if ctx.Err() != nil {
			return
		}
		s.imageReady(ctx, c, ref, w.uri, digest, failure)
	})
}

// versionRef names one version of a function in a scope.
type versionRef struct {
	scope        aws.Scope
	name, number string
}

// versionOf finds the version ref names.
func (st *State) versionOf(ref versionRef) (Version, bool) {
	f, ok := st.Functions[ref.name]
	if !ok {
		return Version{}, false
	}
	return f.version(ref.number)
}

// awaitedImage is the image a version waits for: its own while it is
// created, the update's while an update waits.
func awaitedImage(v *Version) (string, bool) {
	switch st := v.Status.(type) {
	case PullingImage:
		if img, ok := v.Code.(ImageCode); ok {
			return img.URI, true
		}
	case Active:
		if u, ok := st.Update.(UpdatingImage); ok {
			return u.Pending.URI, true
		}
	case Creating, Failed:
	}
	return "", false
}

// ensureImage makes uri present for the architecture and returns its
// digest, or why it could not.
func (s *Service) ensureImage(ctx context.Context, uri, arch string) (string, *imageFailure) {
	if s.opts.Images == nil {
		return "", &imageFailure{code: string(lambdaapi.StateReasonCodeImageAccessDenied), reason: "Lambda could not make the image " + uri +
			" present: no Docker daemon is reachable from understudy. Start Docker, check it with `understudy doctor`, then delete and create the function again."}
	}
	a, err := launcher.ParseArchitecture(arch)
	if err != nil {
		return "", &imageFailure{code: string(lambdaapi.StateReasonCodeInvalidImage), reason: "Lambda could not make the image " + uri + " present: " + err.Error()}
	}
	img, err := s.opts.Images.EnsureImage(ctx, uri, a, nil)
	if err == nil {
		return img.Digest, nil
	}
	code := lambdaapi.StateReasonCodeInvalidImage
	var pull *launcher.PullError
	var ecr *launcher.ECRImageError
	if errors.As(err, &pull) || errors.As(err, &ecr) {
		code = lambdaapi.StateReasonCodeImageAccessDenied
	}
	return "", &imageFailure{code: string(code), reason: "Lambda could not make the image " + uri + " present: " + err.Error()}
}

// imageReady commits the outcome of making uri present to the version
// still waiting for it; a version that moved on is left as it is.
func (s *Service) imageReady(ctx context.Context, c *kernel.Call, ref versionRef, uri, digest string, failure *imageFailure) {
	_, err := write(s, c, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		v, ok := st.versionOf(ref)
		if awaited, waiting := awaitedImage(&v); !ok || !waiting || awaited != uri {
			tx.Unchanged()
			return struct{}{}, nil
		}
		_, creating := v.Status.(PullingImage)
		f := st.Functions[ref.name]
		switch {
		case failure == nil:
			v.Code, v.Status = ImageCode{URI: uri, Digest: digest}, Active{Update: Successful{}}
			if ref.number == latest {
				// The code $LATEST runs changes only now: environments
				// started during the pull run the previous image and must
				// not be reused (ARCHITECTURE.md 14.2).
				f.Generation++
			}
		case creating:
			v.Status = Failed{ReasonCode: failure.code, Reason: failure.reason}
		default:
			v.Status = Active{Update: UpdateFailed{ReasonCode: failure.code, Reason: failure.reason}}
		}
		st.Functions[ref.name] = f.withVersion(v)
		return struct{}{}, nil
	})
	if err != nil {
		s.k.Log().LogAttrs(ctx, slog.LevelError, "recording a Lambda image outcome failed",
			slog.String("function", versionID(ref.scope, ref.name, ref.number)), slog.String("error", err.Message))
	}
}

// parseVersionID reads a version timer id: the version's qualified ARN.
func parseVersionID(id string) (versionRef, bool) {
	a, err := aws.ParseARN(id)
	if err != nil {
		return versionRef{}, false
	}
	rest, ok := strings.CutPrefix(a.Resource, "function:")
	if !ok {
		return versionRef{}, false
	}
	name, number, ok := strings.Cut(rest, ":")
	if !ok {
		return versionRef{}, false
	}
	scope, ok := scopeOf(a)
	return versionRef{scope: scope, name: name, number: number}, ok
}

// cutDigest returns the hex of a sha256 digest in a repository digest
// (repo@sha256:<hex>) or an image ID (sha256:<hex>).
func cutDigest(s string) (string, bool) {
	_, hex, ok := strings.Cut(s, "sha256:")
	return hex, ok && hex != ""
}
