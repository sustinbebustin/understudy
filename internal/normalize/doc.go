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
// A normalizer has a member rule, a text rule, or both. A member rule masks
// a whole scalar by the name of the JSON member or XML element holding it
// (Set.Member); a text rule masks what it finds inside free text (Set.Text).
// The bound normalizer has neither: the replayer applies Substitute with the
// scenario's bindings before the rest of the set.
package normalize
