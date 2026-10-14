// Package gosdk is the aws-sdk-go-v2 conformance suite for understudy.
//
// The suite drives a running understudy through the real AWS SDK for Go v2
// clients with their default settings, and asserts the behavior AWS
// documents and that recordings against real AWS show, as collected in
// docs/research. It never asserts an understudy-specific quirk.
//
// The tests live in one file per service, named <service>_test.go. Run
// them through run.sh, which builds understudy, starts it and points the
// SDK at it through scripts/sealed; see README.md.
package gosdk
