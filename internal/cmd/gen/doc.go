// Command gen is understudy-gen: it generates internal/api/<svc>api from the
// vendored Smithy model, codegen/overlays/common.json,
// codegen/overlays/<svc>.json and codegen/services/<svc>.json.
//
//	go run ./internal/cmd/gen -service sqs          # one service
//	go run ./internal/cmd/gen -all                  # every codegen/services file
//	go run ./internal/cmd/gen -all -check           # report drift, write nothing
//
// It works offline from any directory inside the module; go generate runs
// it from internal/api through the directives in internal/api/generate.go.
// The vendored models are verified by go run ./internal/cmd/vendor -verify,
// which just generate runs first.
package main
