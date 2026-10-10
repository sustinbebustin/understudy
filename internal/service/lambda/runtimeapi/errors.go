package runtimeapi

import (
	"fmt"
	"net/netip"
	"strconv"
	"time"
)

// InvocationError reports an Invocation missing a field the runtime
// protocol requires. Nothing was sent to the runtime.
type InvocationError struct {
	Field  string
	Reason string
}

func (e *InvocationError) Error() string {
	return fmt.Sprintf("runtime API invocation refused: %s %s", e.Field, e.Reason)
}

// BusyError reports an Invoke on an environment that already has an
// invocation queued or running. An environment serves one invocation at a
// time; concurrency comes from more environments.
type BusyError struct {
	Addr netip.AddrPort
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("runtime API environment %s already has an invocation in flight; acquire another environment for concurrent invocations", e.Addr)
}

// ClosedError reports an environment whose listener is closed.
type ClosedError struct {
	Addr netip.AddrPort
}

func (e *ClosedError) Error() string {
	return fmt.Sprintf("runtime API environment %s is closed; acquire a new environment", e.Addr)
}

// Spoilage says why an environment can take no more invocations although
// its container may still run.
type Spoilage uint8

// The ways an earlier invocation spoils an environment.
const (
	// TimedOut: the invocation timed out with the runtime inside the
	// handler.
	TimedOut Spoilage = iota + 1
	// Abandoned: the caller gave up while the runtime was inside the
	// handler.
	Abandoned
	// NeverPolled: the runtime never asked for an invocation (NoPollError).
	NeverPolled
	// NeverRepolled: the runtime answered an invocation but did not ask for
	// the next one within that one's timeout.
	NeverRepolled
)

func (s Spoilage) String() string {
	switch s {
	case TimedOut:
		return "an earlier invocation timed out while the runtime was still running it"
	case Abandoned:
		return "an earlier invocation was abandoned by its caller while the runtime was running it"
	case NeverPolled:
		return "the runtime never asked for an invocation"
	case NeverRepolled:
		return "the runtime answered its last invocation but did not ask for the next one within the function's timeout"
	}
	return "Spoilage(" + strconv.Itoa(int(s)) + ")"
}

// UnusableError reports an environment an earlier invocation left unusable.
// Its container must be replaced.
type UnusableError struct {
	Addr   netip.AddrPort
	Reason Spoilage
}

func (e *UnusableError) Error() string {
	return fmt.Sprintf("runtime API environment %s cannot take another invocation (%v); remove its container and start a new environment", e.Addr, e.Reason)
}

// NoPollError reports a runtime that never asked for an invocation within
// the init limit and the init retry after it: almost always a container
// that cannot reach the Runtime API listener (ADR 0011).
type NoPollError struct {
	// Addr is the address handed to the container as AWS_LAMBDA_RUNTIME_API.
	Addr netip.AddrPort
	// Bind is the address the listener is bound to.
	Bind netip.Addr
	// Topology names the detected container networking topology.
	Topology string
	// Waited is the init limit plus the function's timeout.
	Waited time.Duration
}

func (e *NoPollError) Error() string {
	return fmt.Sprintf("the function's runtime did not call GET /2018-06-01/runtime/invocation/next within %s (the init limit plus the function's timeout, Lambda's init retry). "+
		"The container was told AWS_LAMBDA_RUNTIME_API=%s (listener bound to %s, topology: %s). "+
		"Likely causes: the listener is only reachable on loopback; a firewall drops traffic from the docker0 bridge to the host; "+
		"rootless Docker blocks containers from reaching the host. Check the function's logs for a connection error and run `understudy doctor`; "+
		"if the logs show initialization still running, raise the function's timeout",
		e.Waited, e.Addr, e.Bind, e.Topology)
}
