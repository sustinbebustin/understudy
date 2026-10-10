// Package ref holds the leaf identifier types shared across the emulator:
// ServiceID, OperationID, RuleID, EventID, Cause, Via, Origin, ProtocolID and
// ListenerKind. It may import only the aws package; today it imports nothing
// in the repository.
//
// deps_test.go holds the repository's import-direction table: the allowed
// edges of ARCHITECTURE.md section 2 as data, plus the edges the phase 1 plan
// specifies, each naming its task. A new package needs a row there.
package ref
