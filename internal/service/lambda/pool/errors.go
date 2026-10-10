package pool

import "fmt"

// CapacityError reports the global environment cap reached with every
// environment busy. The Lambda service answers the invocation as throttled.
type CapacityError struct {
	Cap int
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("all %d Lambda execution environments are busy; retry later or raise the pool's global cap", e.Cap)
}

// ClosedError reports an Acquire after the pool drained for shutdown.
type ClosedError struct{}

func (*ClosedError) Error() string {
	return "the Lambda environment pool is shut down and takes no new invocations"
}

// LeaseError reports an Invoke or Release on an environment the caller
// does not hold: released already, or never leased.
type LeaseError struct {
	Env string
}

func (e *LeaseError) Error() string {
	return fmt.Sprintf("Lambda environment %s is not leased to the caller in a state that allows this; acquire an environment first and release it once", e.Env)
}
