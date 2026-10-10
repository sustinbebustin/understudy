// Package server runs understudy serve: listeners, the explicit construction of
// every service in wire.go, startup, shutdown and the container sweep.
//
// It is the only package that names every service, every codec and the outbox.
//
// Decisions the architecture leaves open:
//
//   - Config is the serve subset (listen address, admin token, persistence,
//     TLS, clock start, seed, log format and level, ready file); the
//     configuration package maps onto it later. ClockFrozen takes a
//     time.Time, so the command line parses at its boundary without
//     importing the clock. Persistence and TLS are sums whose later variants
//     arrive with their wiring: PersistDir roots the admin snapshot path
//     form, and TLSOff is the only TLS setting.
//   - A non-loopback listen address needs an admin token; without one New
//     refuses with a *ListenError that explains the token and the published
//     container port. The token applies on any address once set.
//   - The admin API (internal/server/admin) serves the /_understudy/v1/
//     prefix; its health endpoint answers 503 {"ready": false, "waiting":
//     ["listener"]} until Serve's listener accepts.
//   - The registry holds all twelve generated descriptors; the eleven
//     services other than SQS are stubs that bind nothing, so each of their
//     operations answers 501 in its protocol's envelope. A service's
//     throttleError and unknownOperationError come from its generated
//     package (apis in wire.go), since the descriptors do not carry them.
//   - Every codec is registered (Codecs). The REST codecs never claim: the
//     edge routes REST requests through the route tables. A request the
//     rpcv2Cbor codec rejects outright is Declined to the edge, which has no
//     claim that answers with the codec's error and so answers it as
//     unclaimed, with the same UnknownOperationException identity.
//   - wire.go hands the admin API each service that implements one of its
//     hooks (draining, hot reload, image pulls, the TTL sweep, subscription
//     tokens) by a type check, so a service task that adds the method is
//     wired without editing wire.go. The Late list is collected the same way.
//   - The snapshot runtime restores id stream positions only when both the
//     archive and this run are seeded, keeps this run's seed, and moves the
//     journal counter forward only, so an event id is never issued twice.
//   - The ready file is written atomically (temporary file and rename) once
//     the listener accepts, and removed at shutdown, so a stale file never
//     names a dead endpoint.
//   - Shutdown cancels every request's context first, so parked long polls
//     end at once, then gives requests in flight five seconds before the
//     kernel stops.
//   - Codecs are adapted to edge.Codec here (codecs.go), since the edge may
//     not import a protocol package.
package server
