// Package understudy runs understudy, the local AWS emulator, as a
// Testcontainers for Go module (testcontainers.md 8).
//
// For a Go test that needs no container, understudytest runs understudy in
// process and stays the recommendation (testcontainers.md 9.4). This module
// is for suites already on testcontainers-go that need understudy as a
// container, for example beside an application container on a shared
// Docker network.
//
// Run publishes port 4566 on 127.0.0.1 only, waits for
// GET /_understudy/v1/health to answer 200 with "ready": true, and labels
// every container understudy starts with the session's Ryuk filter labels,
// so Ryuk also reaps understudy's Lambda and DynamoDB Local containers.
//
// The package is experimental until understudy 1.0.
package understudy
