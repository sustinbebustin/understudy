// Package normalize is the closed, named normalizer set that snapshot diff
// --normalize and the parity replayer share (ARCHITECTURE.md 12, 19). Each
// normalizer masks what it finds with one placeholder, "<" + its name + ">",
// so two values that differ only in an id or a wall-clock reading compare
// equal while a difference in form (an ISO timestamp against an epoch
// number) still shows.
//
// Snapshot selects the masks of persisted state; Exchange selects those of a
// recorded HTTP exchange against understudy's answer: request and host ids,
// timestamps, the masked identity values of the recordings, credentials, the
// account, receipt handles, and the values a parity scenario binds.
//
// Not implemented yet: the rules are stubs, and snapshot still applies its
// own copy of the snapshot set.
package normalize
