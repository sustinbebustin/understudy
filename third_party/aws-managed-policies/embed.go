// Package awsmanagedpolicies embeds the AWS managed policy catalogue that
// internal/cmd/managedpolicies writes beside it (ADR 0016, amendment
// "managed policies"). The data files are a third-party input licensed
// separately from the code; internal/policycatalogue reads them.
package awsmanagedpolicies

import (
	"embed"
	"io/fs"
)

// The data files, as the generator names them.
const (
	PoliciesFile = "policies.json"
	SourceFile   = "SOURCE.json"
	LicenseFile  = "LICENSE"
	NoticeFile   = "NOTICE"
)

// The binary embeds every data file in this directory (ADR 0016, amendment
// "managed policies"). Until the maintainer's first -fetch run is committed,
// policies.json holding the empty catalogue is the only one; that commit adds
// SOURCE.json, LICENSE and NOTICE to this list, and
// internal/policycatalogue/embedded_impl_test.go fails until it does.
//
//go:embed policies.json
var files embed.FS

// FS returns the embedded data files, read-only.
func FS() fs.FS { return files }
