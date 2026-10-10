// Command gen is understudy-gen. It generates internal/api/<svc>api from the
// vendored Smithy model, third_party/aws-service-reference/actions.json,
// codegen/overlays/common.json, codegen/overlays/<svc>.json and
// codegen/services/<svc>.json, and the synthetic protocol compliance services
// under internal/protocoltest/gen from third_party/smithy-protocol-tests and
// codegen/overlays/protocoltests.json.
//
//	go run ./internal/cmd/gen -service sqs          # one service
//	go run ./internal/cmd/gen -all                  # every codegen/services file
//	go run ./internal/cmd/gen -corpus               # the compliance services
//	go run ./internal/cmd/gen -all -check           # report drift, write nothing
//
// It works offline from any directory inside the module; go generate runs
// it through the directives in internal/api/generate.go and
// internal/protocoltest/gen/generate.go. The vendored inputs are verified by
// go run ./internal/cmd/vendor -verify, which just generate runs first.
package main
