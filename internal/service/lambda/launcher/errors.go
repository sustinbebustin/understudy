package launcher

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// SpecError reports a Spec or Slot the launcher cannot turn into a
// container. Nothing was created.
type SpecError struct {
	Field  string
	Reason string
}

func (e *SpecError) Error() string {
	return fmt.Sprintf("Lambda container spec: %s %s", e.Field, e.Reason)
}

// PullError reports an image that is neither present nor pullable. The
// Lambda service moves the function to State Failed with this message
// (ARCHITECTURE.md 14.3).
type PullError struct {
	Ref      string
	Platform docker.Platform
	Err      error
}

func (e *PullError) Error() string {
	return fmt.Sprintf("image %s for %s is not present locally and could not be pulled: %v; check the image name and network access, or pull it with `docker pull --platform %s %s`",
		e.Ref, e.Platform, e.Err, e.Platform, e.Ref)
}

func (e *PullError) Unwrap() error { return e.Err }

// ECRImageError reports an Amazon ECR image that is not present locally
// for the function's architecture: understudy does not emulate ECR and
// never pulls from AWS.
type ECRImageError struct {
	Ref string
	// LocalArchitecture is the OCI architecture of the image present
	// locally under Ref, or empty when none is.
	LocalArchitecture string
}

func (e *ECRImageError) Error() string {
	if e.LocalArchitecture != "" {
		return fmt.Sprintf("image %s is present locally only for %s, not the function's architecture, and ECR is not emulated; build the image for the function's architecture and tag it as %s, or change the function's Architectures",
			e.Ref, e.LocalArchitecture, e.Ref)
	}
	return fmt.Sprintf("image %s is not present locally and ECR is not emulated; build or tag your local image as %s", e.Ref, e.Ref)
}

// ImageArchitectureError reports an image built locally, with no registry
// to pull another architecture from, that is present only for an
// architecture other than the function's. Nothing was pulled.
type ImageArchitectureError struct {
	Ref      string
	Declared Architecture
	// Present is the image's OCI architecture as the daemon reports it.
	Present string
}

func (e *ImageArchitectureError) Error() string {
	return fmt.Sprintf("image %s is present locally only for %q and was built locally, so there is no registry to pull %s from; rebuild it with `docker build --platform %s` or set the function's Architectures to match the image",
		e.Ref, e.Present, e.Declared, e.Declared.Platform())
}

// WatchError reports an Exits subscription while another is open on the
// same launcher; exits go to one consumer, the pool.
type WatchError struct{}

func (*WatchError) Error() string {
	return "launcher exits are already being watched; one consumer (the pool) owns the exit stream until the context of its Exits call ends"
}
