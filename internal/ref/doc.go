// Package ref holds the leaf identifier types shared across the emulator:
// ServiceID, OperationID, RuleID, EventID, Cause, Via, Origin and its
// variants, ProtocolID and ListenerKind. It may import only the aws package.
//
// deps_test.go holds the repository's import-direction table: the allowed
// edges of ARCHITECTURE.md section 2 as data, plus the edges listed there
// beyond section 2, each with its reason. A new package needs a row there.
package ref
