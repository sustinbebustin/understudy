package pool

import (
	"crypto/sha256"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// EnvState is where an execution environment is in its life:
// Starting -> Reserved -> Busy -> Idle -> Reserved ... -> Draining -> Gone,
// and Starting|Reserved|Busy|Idle -> Faulted -> Gone (ARCHITECTURE.md 14.2).
//
//sumtype:decl
type EnvState interface{ isEnvState() }

// Starting is a container being created and started for the caller that
// acquired it. Elapsed is monotonic wall time since the start began.
type Starting struct{ Elapsed func() time.Duration }

// Reserved is an environment leased to one caller that has not yet
// dispatched an invocation.
type Reserved struct{}

// Busy is an environment running one invocation for its leaseholder.
type Busy struct{ Invocation ids.LambdaRequestID }

// Idle is a warm environment waiting for an invocation. Elapsed is
// monotonic wall time since it became idle; the emulator clock never
// touches it.
type Idle struct{ Elapsed func() time.Duration }

// Draining is an environment being removed while healthy.
type Draining struct{ Reason DrainReason }

// Faulted is an environment being removed because its runtime failed.
type Faulted struct{ Cause FaultCause }

// Gone is an environment whose listener is closed and whose container is
// removed.
type Gone struct{}

func (Starting) isEnvState() {}
func (Reserved) isEnvState() {}
func (Busy) isEnvState()     {}
func (Idle) isEnvState()     {}
func (Draining) isEnvState() {}
func (Faulted) isEnvState()  {}
func (Gone) isEnvState()     {}

// DrainReason says why a healthy environment is removed.
type DrainReason uint8

// The closed set of drain reasons.
const (
	// IdleExpired: idle longer than the idle timeout.
	IdleExpired DrainReason = iota + 1
	// NewGeneration: the function's code or configuration changed.
	NewGeneration
	// Evicted: over the per-function idle cap, or making room under the
	// global cap.
	Evicted
	// Reset: the emulator's state was reset.
	Reset
	// Shutdown: the pool is closing.
	Shutdown
	// Abandoned: its leaseholder gave up mid-invocation, so the runtime's
	// state is unknown.
	Abandoned
)

func (r DrainReason) String() string {
	switch r {
	case IdleExpired:
		return "idle expired"
	case NewGeneration:
		return "new generation"
	case Evicted:
		return "evicted"
	case Reset:
		return "reset"
	case Shutdown:
		return "shutdown"
	case Abandoned:
		return "abandoned"
	}
	return "DrainReason(" + strconv.Itoa(int(r)) + ")"
}

// FaultCause says why an environment's runtime failed.
//
//sumtype:decl
type FaultCause interface{ isFaultCause() }

// InitFailed is a runtime that reported an initialization error.
type InitFailed struct{ Type string }

// Crashed is a runtime whose container exited with Code.
type Crashed struct{ Code int }

// OutOfMemory is a container killed for exceeding its memory limit.
type OutOfMemory struct{}

// TimedOut is an invocation that outlived the function's timeout; the
// container is killed and the next invocation starts cold.
type TimedOut struct{}

// WrongArchitecture is code built for another architecture.
type WrongArchitecture struct{ Declared, Host launcher.Architecture }

func (InitFailed) isFaultCause()        {}
func (Crashed) isFaultCause()           {}
func (OutOfMemory) isFaultCause()       {}
func (TimedOut) isFaultCause()          {}
func (WrongArchitecture) isFaultCause() {}

// faultOf returns the fault a failing outcome means; Success and
// FunctionError leave the runtime healthy.
func faultOf(o runtimeapi.Outcome) (FaultCause, bool) {
	switch o := o.(type) {
	case runtimeapi.Success, runtimeapi.FunctionError:
		return nil, false
	case runtimeapi.Timeout:
		return TimedOut{}, true
	case runtimeapi.ExitError:
		return Crashed{Code: o.Code}, true
	case runtimeapi.OOM:
		return OutOfMemory{}, true
	case runtimeapi.InitError:
		return InitFailed{Type: o.Type}, true
	case runtimeapi.ArchMismatch:
		return WrongArchitecture{Declared: launcher.Architecture(o.Declared), Host: launcher.Architecture(o.Host)}, true
	}
	return nil, false
}

// exitOutcome maps a container exit to the outcome its invocation gets.
func exitOutcome(c launcher.ExitCause) runtimeapi.Outcome {
	switch c := c.(type) {
	case launcher.Exited:
		return runtimeapi.ExitError{Code: c.Code}
	case launcher.OOMKilled:
		return runtimeapi.OOM{}
	case launcher.ArchMismatch:
		return runtimeapi.ArchMismatch{Declared: string(c.Declared), Host: string(c.Host)}
	}
	// Only a nil cause, which the launcher never sends, gets here; Go needs
	// a return after a switch without default.
	return runtimeapi.ExitError{Code: -1}
}

// Key identifies interchangeable environments: same function version,
// generation and container spec. Any code or configuration update bumps the
// function's generation (ARCHITECTURE.md 14.2).
type Key struct {
	Function   aws.ARN
	Version    string
	Generation uint64
	// SpecHash is the SHA-256 of every field of the launcher.Spec.
	SpecHash [32]byte
}

// NewKey derives the key of spec. Every field of the Spec is part of the
// key, the credentials included: the Lambda service must keep a function
// generation's credentials stable, or no two invocations share a warm
// environment.
func NewKey(spec launcher.Spec) Key {
	return Key{Function: spec.Function.ARN(), Version: spec.Function.Version, Generation: spec.Generation, SpecHash: specHash(spec)}
}

// specHash hashes spec field by field: every value a netstring, slices and
// the environment count-prefixed, the environment in key order and the
// Code variant tagged. What it covers never depends on how a type formats
// itself. Nil and empty slices or maps hash alike, as they create the same
// container.
func specHash(spec launcher.Spec) [32]byte {
	var e specEncoder
	f := spec.Function
	e.strings(f.Name, f.Version, f.Account.String(), f.Region.String())
	e.strings(strconv.FormatUint(spec.Generation, 10), spec.Image, string(spec.Architecture))
	switch c := spec.Code.(type) {
	case launcher.ZipCode:
		e.strings("zip", c.Dir, c.Handler)
	case launcher.ImageCode:
		e.strings("image")
		e.list(c.EntryPoint)
		e.list(c.Command)
		e.strings(c.WorkingDirectory)
	case nil:
		e.strings("none")
	}
	e.strings(strconv.Itoa(spec.MemoryMB), strconv.Itoa(spec.EphemeralStorageMB))
	keys := slices.Sorted(maps.Keys(spec.Environment))
	e.list(keys)
	for _, k := range keys {
		e.strings(spec.Environment[k])
	}
	cred := spec.Credentials
	e.strings(string(cred.AccessKeyID), string(cred.SecretAccessKey), string(cred.SessionToken), spec.LogGroup)
	return sha256.Sum256(e.b)
}

// specEncoder writes values as netstrings (<length>:<bytes>), so no two
// sequences of values encode alike.
type specEncoder struct{ b []byte }

func (e *specEncoder) strings(ss ...string) {
	for _, s := range ss {
		e.b = strconv.AppendInt(e.b, int64(len(s)), 10)
		e.b = append(e.b, ':')
		e.b = append(e.b, s...)
	}
}

// list writes a slice as its length, then its elements.
func (e *specEncoder) list(ss []string) {
	e.strings(strconv.Itoa(len(ss)))
	e.strings(ss...)
}

// function names a function version, the unit of the idle cap and of
// generations.
type function struct {
	arn     aws.ARN
	version string
}

func (k Key) function() function { return function{arn: k.Function, version: k.Version} }

// Selector picks the environments Drain removes.
//
//sumtype:decl
type Selector interface{ isSelector() }

// Function selects every environment of a function, all versions and
// generations.
type Function struct{ ARN aws.ARN }

// All selects every environment.
type All struct{}

func (Key) isSelector()      {}
func (Function) isSelector() {}
func (All) isSelector()      {}
