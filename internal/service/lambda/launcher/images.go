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
// (lambda-api.md 4.2). provided.al2023 and python3.13 are the digests the
// research ran against on 2026-10-08 (containers.md 0); the others were
// read from public.ecr.aws on 2026-10-09.
func RuntimeImages() []RuntimeImage {
	const repo = "public.ecr.aws/lambda/"
	return []RuntimeImage{
		{Runtime: "provided.al2023", Repository: repo + "provided", Tag: "al2023", Digest: "sha256:e10c4765865b384b15eacc855a3f25ac61745d8dd1f510217deecf23b88fdbd5"},
		{Runtime: "python3.13", Repository: repo + "python", Tag: "3.13", Digest: "sha256:0fddf7542198843bd5a488b17e35473659fdf69f0788c7b3563f0231edf3384d"},
		{Runtime: "python3.12", Repository: repo + "python", Tag: "3.12", Digest: "sha256:78ec401f7684bdd366e6b9bad77bcf1f51b3b24a7c90f8b4a4d121188b2a5985"},
		{Runtime: "nodejs22.x", Repository: repo + "nodejs", Tag: "22", Digest: "sha256:30584cf5ce0fe543ce34fe3eddbd6177b3d1aa6b5f437a5f60a8b1a8702085f0"},
		{Runtime: "nodejs20.x", Repository: repo + "nodejs", Tag: "20", Digest: "sha256:a4440274d6f0fb4e6cb92cd5f2a97254efe6612f631d9974b197ecbd4a61fcab"},
		{Runtime: "java21", Repository: repo + "java", Tag: "21", Digest: "sha256:4fab6f58fdad00442a6741674f88d65a2c53525a0073f3d94066b36318ec48ee"},
		{Runtime: "dotnet8", Repository: repo + "dotnet", Tag: "8", Digest: "sha256:8c7fc5c0dc3280f7eee87d58bfc639e024d2ae739829b5595b335c60a5806855"},
		{Runtime: "ruby3.3", Repository: repo + "ruby", Tag: "3.3", Digest: "sha256:e00bc937926c8e1810b5d4788f3d39f0b6c53e6e2e4e621212f31a569bfdb878"},
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
	repo := repository(ref)
	for _, rd := range img.RepoDigests {
		if r, d, ok := strings.Cut(rd, "@"); ok && r == repo {
			digest = d
			break
		}
	}
	return Image{Ref: ref, Digest: digest, Architecture: arch}
}

// repository strips the tag and digest from a reference.
func repository(ref string) string {
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
