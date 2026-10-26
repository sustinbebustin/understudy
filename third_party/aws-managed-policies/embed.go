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
	// PoliciesFile is the catalogue, the reviewable source of truth.
	PoliciesFile = "policies.json"
	// PoliciesGzipFile is PoliciesFile compressed; the generator derives it
	// from PoliciesFile.
	PoliciesGzipFile = "policies.json.gz"
	SourceFile       = "SOURCE.json"
	LicenseFile      = "LICENSE"
	NoticeFile       = "NOTICE"
)

// The binary embeds every data file in this directory (ADR 0016, amendment
// "managed policies"); internal/policycatalogue/embedded_impl_test.go fails
// when a file the generator writes is missing from this list.
//
//go:embed policies.json SOURCE.json LICENSE NOTICE
var files embed.FS

// FS returns the embedded data files, read-only.
func FS() fs.FS { return files }
