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

//go:embed policies.json
var files embed.FS

// FS returns the embedded data files, read-only. Not implemented yet: it
// holds only policies.json, the empty catalogue.
func FS() fs.FS { return files }
