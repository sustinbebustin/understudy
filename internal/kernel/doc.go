// Package kernel is the one pipeline every call runs through, whether it came
// from an SDK or from a cross-service delivery.
//
// It holds Call, Handler, Op, Binder, Bindings, Dispatch, Invoke, Write, Tx,
// the timer kinds, the work tracker and the Stateful view used by snapshots.
// It never imports a codec or the snapshot package.
package kernel
