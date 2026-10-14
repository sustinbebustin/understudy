package lambda

import (
	"context"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
)

// Options are the Lambda settings the server fills from the configuration. The
// zero value is AWS's behavior with every provisioning state settled before
// the call that starts it returns, and no Docker daemon to pull images from.
type Options struct {
	// StateTransitions is the lambda.stateTransitions switch.
	StateTransitions StateTransitions
	// Images makes container images present for image functions; a
	// *launcher.Launcher. nil means no Docker daemon is reachable, so an
	// image function becomes Failed with a message naming its image.
	Images ImageSource
}

// StateTransitions says when the provisioning states of functions and event
// source mappings settle (ARCHITECTURE.md 9, the provisioning settle class).
type StateTransitions uint8

// StateTransitions values. The zero value is TransitionsFinal.
const (
	// TransitionsFinal settles a state before the mutating call returns, in
	// every clock mode: a new function is Active, an update Successful, a
	// new mapping Enabled.
	TransitionsFinal StateTransitions = iota
	// TransitionsClock keeps the intermediate states (Pending, InProgress,
	// Creating, Enabling, Disabling, Updating, Deleting) for settleDelay of
	// emulator time, with AWS's ResourceConflictException for the calls
	// that land inside the window, for tests of SDK waiters.
	TransitionsClock
)

// String returns the switch value as configured: "final" or "clock".
func (t StateTransitions) String() string {
	switch t {
	case TransitionsFinal:
		return "final"
	case TransitionsClock:
		return "clock"
	default:
		return "StateTransitions(" + strconv.Itoa(int(t)) + ")"
	}
}

// ImageSource makes a container image present for an architecture and
// reports its digest. *launcher.Launcher implements it.
type ImageSource interface {
	EnsureImage(ctx context.Context, ref string, arch launcher.Architecture, progress func(docker.Progress)) (launcher.Image, error)
}
