// Package server runs understudy serve: listeners, the explicit construction of
// every service in wire.go, startup, shutdown and the container sweep.
//
// It is the only package that names every service, every codec and the outbox.
//
// Decisions the architecture leaves open:
//
//   - Config is the serve subset (listen address, clock start, seed, log
//     format and level, ready file); the configuration package maps onto it
//     later. ClockFrozen takes a time.Time, so the command line parses at
//     its boundary without importing the clock.
//   - health.go serves GET /_understudy/v1/health until the admin API takes
//     the prefix over: 503 {"ready": false, "waiting": ["listener"]} until
//     Serve's listener accepts, then 200 with the version, uptime, the
//     services wired and the model pin. Other admin paths answer the admin
//     error shape with not_found.
//   - The ready file is written atomically (temporary file and rename) once
//     the listener accepts, and removed at shutdown, so a stale file never
//     names a dead endpoint.
//   - Shutdown cancels every request's context first, so parked long polls
//     end at once, then gives requests in flight five seconds before the
//     kernel stops.
//   - Codecs are adapted to edge.Codec here (codecs.go), since the edge may
//     not import a protocol package.
package server
