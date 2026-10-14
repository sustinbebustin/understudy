package launcher

import (
	"context"
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

// RuntimeImages is the pinned table for the phase 1 runtimes
// (lambda-api.md 4.2): each tag's multi-architecture index digest as
// public.ecr.aws served it on 2026-10-14.
func RuntimeImages() []RuntimeImage {
	const repo = "public.ecr.aws/lambda/"
	return []RuntimeImage{
		{Runtime: "provided.al2023", Repository: repo + "provided", Tag: "al2023", Digest: "sha256:a79fa3e8a59e153b5aa69323d10ee2c8f54826e9834afaa1ac4bc79fbaaaf39f"},
		{Runtime: "python3.13", Repository: repo + "python", Tag: "3.13", Digest: "sha256:1f12740d125ab92d8ec3345058da72ba55deeca968b83044ea74af29f0d7f42b"},
		{Runtime: "python3.12", Repository: repo + "python", Tag: "3.12", Digest: "sha256:c05509c1a303e7ca10acc26782f530ff1a8573411132e2a67c1924d687a65a96"},
		{Runtime: "nodejs22.x", Repository: repo + "nodejs", Tag: "22", Digest: "sha256:7dd535c30683b97ea8dc29e5d3731d4ba2516ddeec37adaee204863e54bb3557"},
		{Runtime: "nodejs20.x", Repository: repo + "nodejs", Tag: "20", Digest: "sha256:a4440274d6f0fb4e6cb92cd5f2a97254efe6612f631d9974b197ecbd4a61fcab"},
		{Runtime: "java21", Repository: repo + "java", Tag: "21", Digest: "sha256:811285999ac7833636bacfbe780012aede4e38c12d15b0ff4a7d90806549ef5c"},
		{Runtime: "dotnet8", Repository: repo + "dotnet", Tag: "8", Digest: "sha256:729b066ac5e5b6c3259c053b84b697ea451dfae7f9ae9835568561fb6b9a5f63"},
		{Runtime: "ruby3.3", Repository: repo + "ruby", Tag: "3.3", Digest: "sha256:588e6b7487f76d6289d4be5c129ad1092b5953822bba42757b7292d04922326e"},
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
	// image ID for an image built locally that has none.
	Digest       string
	Architecture Architecture
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
	digest := img.ID
	repo := Repository(ref)
	for _, rd := range img.RepoDigests {
		if r, d, ok := strings.Cut(rd, "@"); ok && r == repo {
			digest = d
			break
		}
	}
	return Image{Ref: ref, Digest: digest, Architecture: arch}
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
