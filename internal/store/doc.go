// Package store provides the state containers every service uses: Gate,
// Cell, Partitioned, Waiters, WaitKey and the snapshot Section.
//
// It defines no effect types and imports only internal/aws, for the key
// codecs every service shares: ScopeKeys, AccountKeys and StringKeys. Owning
// them here keeps the snapshot layout of a key type identical across
// services.
//
// Lock order is gate, then one cell, then leaf locks; Partitioned's map lock
// is never held while a cell lock is taken. Update and Read take the gate's
// shared side. Export, Import and Reset take no gate lock, because the
// snapshot cut, load and reset call them while holding Gate.Exclusive.
//
// Section paths are relative to the owning service's directory: the key's
// encoding plus ".json". The snapshot layer adds "services/<svc>/". Two
// optional interfaces on the state's pointer type steer Export: Zeroer skips
// a cell, and BlobHolder fills Section.Blobs. A state implementing either
// pins it with a compile-time assertion, so a renamed method fails the build
// instead of silently dropping cells or blobs from a snapshot.
package store
