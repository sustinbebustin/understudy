package launcher

import (
	"context"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// RuntimeImage is the AWS base image a managed runtime runs in, pinned by
// digest so every understudy release runs the images it was tested with
// (ARCHITECTURE.md 14.3). The digest is the multi-architecture index, so
// one pin serves x86_64 and arm64.
type RuntimeImage struct {
	Runtime    string
	Repository string
	Tag        string
	Digest     string
}

// Ref is the pinned reference pulled and run: repository@digest.
func (r RuntimeImage) Ref() string { return r.Repository + "@" + r.Digest }

// RuntimeImages is the pinned table of the runtimes understudy supports
// (lambda-api.md 4.2): each tag's multi-architecture index digest as
// public.ecr.aws served it on 2026-11-13. scripts/repin-runtime-images
// rewrites the digests and that date when a release is prepared.
func RuntimeImages() []RuntimeImage {
	const repo = "public.ecr.aws/lambda/"
	return []RuntimeImage{
		{Runtime: "provided.al2023", Repository: repo + "provided", Tag: "al2023", Digest: "sha256:75c00eca722a6a3c14ad4586777e701f2edcae65deffb86cc1edb398ac2d5ee1"},
		{Runtime: "python3.13", Repository: repo + "python", Tag: "3.13", Digest: "sha256:6465169ec9334097b25b291e98fa704c522bd0f84622d56e43117f629cc8f5af"},
		{Runtime: "python3.12", Repository: repo + "python", Tag: "3.12", Digest: "sha256:c453db4acf023dc6882576cc792d3f908a12d2e450580e536e3421fb24bb9e88"},
		{Runtime: "nodejs22.x", Repository: repo + "nodejs", Tag: "22", Digest: "sha256:d95b861c8139eb5229f9703ff217e3cf0ca040430bfa090fee47fac5096a530f"},
		{Runtime: "nodejs20.x", Repository: repo + "nodejs", Tag: "20", Digest: "sha256:a4440274d6f0fb4e6cb92cd5f2a97254efe6612f631d9974b197ecbd4a61fcab"},
		{Runtime: "java21", Repository: repo + "java", Tag: "21", Digest: "sha256:d0cf7e06ca58e73efe356ca97e30d1b3f45b96e945ebf6bcf6bef99a80005fff"},
		{Runtime: "dotnet8", Repository: repo + "dotnet", Tag: "8", Digest: "sha256:66395d6489d41ba2e7e3d649183c2e31291d9212227c9f3da526cdb4993b0d9b"},
		{Runtime: "ruby3.3", Repository: repo + "ruby", Tag: "3.3", Digest: "sha256:39e51a8c7e06f5203e0848fd46c17c1bfcc3423885a3b2624635f2f5a45e3f2a"},
	}
}

// ImageForRuntime returns the pinned image of a Lambda Runtime value.
func ImageForRuntime(runtime string) (RuntimeImage, bool) {
	for _, img := range RuntimeImages() {
		if img.Runtime == runtime {
			return img, true
		}
	}
	return RuntimeImage{}, false
}

// Image is an image present locally for an architecture.
type Image struct {
	Ref string
	// Digest is the repository digest the daemon recorded for Ref, or the
	// image ID for an image built locally that has none, as Kind says.
	Digest       string
	Kind         DigestKind
	Architecture Architecture
}

// DigestKind is what an Image's Digest is, which decides the reference
// that names the image again: repository@digest or the digest alone.
type DigestKind uint8

const (
	// RepositoryDigest is a digest the daemon recorded for the image's
	// repository, named as repository@digest.
	RepositoryDigest DigestKind = iota + 1
	// ImageID is the ID of an image with no repository digest for its
	// repository, as a local build has; the daemon resolves it as is.
	ImageID
)

// String names the kind: "repository digest" or "image ID".
func (k DigestKind) String() string {
	switch k {
	case RepositoryDigest:
		return "repository digest"
	case ImageID:
		return "image ID"
	default:
		return "DigestKind(" + strconv.Itoa(int(k)) + ")"
	}
}

type imageKey struct {
	ref  string
	arch Architecture
}

// EnsureImage makes ref present for arch: a local image of that
// architecture is used as is (local tags first, so `docker build -t
// <ImageUri>` works), anything else is pulled for the architecture's
// platform. Concurrent calls for one image share a single pull, which
// reports progress to the caller that started it until that caller
// returns; each caller stops waiting when its own ctx ends, and the pull
// is cancelled once no caller waits for it. progress is never called
// after EnsureImage returns. Nothing is ever pulled from Amazon ECR: an
// ECR reference not present for arch fails with *ECRImageError. An image
// built locally (no repository digest) that is present only for another
// architecture fails with *ImageArchitectureError rather than being
// replaced by a pull. A failed pull is a *PullError.
func (l *Launcher) EnsureImage(ctx context.Context, ref string, arch Architecture, progress func(docker.Progress)) (Image, error) {
	return doReporting(ctx, &l.pulls, imageKey{ref: ref, arch: arch}, progress, func(ctx context.Context, report func(docker.Progress)) (Image, error) {
		return l.ensureImage(ctx, ref, arch, report)
	})
}

func (l *Launcher) ensureImage(ctx context.Context, ref string, arch Architecture, progress func(docker.Progress)) (Image, error) {
	img, err := l.client.ImageInspect(ctx, ref)
	switch {
	case err != nil && !docker.IsNotFound(err):
		return Image{}, err
	case err != nil && isECR(ref):
		return Image{}, &ECRImageError{Ref: ref}
	case err == nil:
		if got, known := machineArchitecture(img.Architecture); known && got == arch {
			return present(ref, arch, img), nil
		}
		if isECR(ref) {
			return Image{}, &ECRImageError{Ref: ref, LocalArchitecture: img.Architecture}
		}
		if len(img.RepoDigests) == 0 {
			return Image{}, &ImageArchitectureError{Ref: ref, Declared: arch, Present: img.Architecture}
		}
	}
	if err := l.client.ImagePull(ctx, ref, arch.Platform(), progress); err != nil {
		return Image{}, &PullError{Ref: ref, Platform: arch.Platform(), Err: err}
	}
	img, err = l.client.ImageInspect(ctx, ref)
	if err != nil {
		return Image{}, &PullError{Ref: ref, Platform: arch.Platform(), Err: err}
	}
	return present(ref, arch, img), nil
}

func present(ref string, arch Architecture, img docker.Image) Image {
	repo := Repository(ref)
	for _, rd := range img.RepoDigests {
		if r, d, ok := strings.Cut(rd, "@"); ok && r == repo {
			return Image{Ref: ref, Digest: d, Kind: RepositoryDigest, Architecture: arch}
		}
	}
	return Image{Ref: ref, Digest: img.ID, Kind: ImageID, Architecture: arch}
}

// Repository strips the tag and digest from an image reference, the part
// a repository digest is paired with (repo@sha256:...). A registry port is
// not a tag.
func Repository(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	nameStart := strings.LastIndex(ref, "/") + 1
	if i := strings.LastIndex(ref[nameStart:], ":"); i >= 0 {
		return ref[:nameStart+i]
	}
	return ref
}

// isECR reports whether ref names a private Amazon ECR registry:
// <account>.dkr.ecr.<region>.amazonaws.com[.cn]/...
func isECR(ref string) bool {
	host, _, ok := strings.Cut(ref, "/")
	if !ok {
		return false
	}
	return strings.Contains(host, ".dkr.ecr.") && (strings.HasSuffix(host, ".amazonaws.com") || strings.HasSuffix(host, ".amazonaws.com.cn"))
}
