package ddblocal

import (
	"context"
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// Digest pins amazon/dynamodb-local 3.3.1 (dynamodb-local.md 1). Docker Hub
// tags are mutable, so the image is always addressed by digest.
const Digest = "sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab"

// The repositories the pinned digest is pulled from, in order: Docker Hub,
// then the ECR Public mirror, which serves the same digest and has no
// anonymous pull rate limit (dynamodb-local-terms.md 1.1).
const (
	dockerHub = "amazon/dynamodb-local"
	ecrPublic = "public.ecr.aws/aws-dynamodb-local/aws-dynamodb-local"
)

// Reference is the pinned image on Docker Hub.
func Reference() string { return dockerHub + "@" + Digest }

// references are the pinned image in every repository, in pull order.
func references() []string {
	return []string{Reference(), ecrPublic + "@" + Digest}
}

// ensureImage returns a reference to the pinned image that is present on
// the daemon, pulling it when no repository's copy is: Docker Hub first,
// the ECR Public mirror when that fails. The image is never re-tagged.
func ensureImage(ctx context.Context, d *docker.Client) (string, error) {
	refs := references()
	for _, ref := range refs {
		_, err := d.ImageInspect(ctx, ref)
		if err == nil {
			return ref, nil
		}
		if !docker.IsNotFound(err) {
			return "", fmt.Errorf("ddblocal: inspecting %s: %w", ref, err)
		}
	}
	var failed []error
	for _, ref := range refs {
		err := d.ImagePull(ctx, ref, docker.Platform{}, nil)
		if err == nil {
			return ref, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		failed = append(failed, fmt.Errorf("%s: %w", ref, err))
	}
	return "", &PullError{Attempts: failed}
}

// PullError is a pull of the pinned image that failed from every
// repository.
type PullError struct{ Attempts []error }

func (e *PullError) Error() string {
	parts := make([]string, len(e.Attempts))
	for i, err := range e.Attempts {
		parts[i] = err.Error()
	}
	return "ddblocal: pulling DynamoDB Local failed from every registry (" + strings.Join(parts, "; ") +
		"); check network access to Docker Hub and public.ecr.aws, or pull " + Reference() + " by hand"
}

// Unwrap returns the failure of each repository.
func (e *PullError) Unwrap() []error { return e.Attempts }
