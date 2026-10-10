// Package api holds the generated service packages, one per service:
// internal/api/<svc>api. Their *_gen.go files are never edited by hand;
// hand-written tests sit beside them. Regenerate with just generate, which
// verifies the vendored models and then runs the directives below, one per
// service.
package api

//go:generate go run ../cmd/gen -service sqs
