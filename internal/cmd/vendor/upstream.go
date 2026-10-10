package main

import (
	"crypto/sha1" //nolint:gosec // git names blobs by SHA-1; this checks integrity against the upstream tree, not security
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// The pinned upstream: aws/api-models-aws has no tags or releases, so the pin
// is a commit (smithy-models.md 1.1).
const (
	upstreamRepo  = "https://github.com/aws/api-models-aws"
	upstreamOwner = "aws/api-models-aws"
	pinnedCommit  = "1cfdfb4bb9ea858e65623e66d73af04bec353201"
	upstreamSPDX  = "Apache-2.0"
	manifestName  = "UPSTREAM.json"
)

// model is one pinned model file: its upstream directory name and path.
type model struct {
	Dir  string
	Path string
}

// pinnedModels are the twelve phase 1 models of smithy-models.md 3.1, sorted
// by directory name.
func pinnedModels() []model {
	return []model{
		{Dir: "cloudwatch", Path: "models/cloudwatch/service/2010-08-01/cloudwatch-2010-08-01.json"},
		{Dir: "cloudwatch-logs", Path: "models/cloudwatch-logs/service/2014-03-28/cloudwatch-logs-2014-03-28.json"},
		{Dir: "dynamodb", Path: "models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json"},
		{Dir: "eventbridge", Path: "models/eventbridge/service/2015-10-07/eventbridge-2015-10-07.json"},
		{Dir: "iam", Path: "models/iam/service/2010-05-08/iam-2010-05-08.json"},
		{Dir: "kms", Path: "models/kms/service/2014-11-01/kms-2014-11-01.json"},
		{Dir: "lambda", Path: "models/lambda/service/2015-03-31/lambda-2015-03-31.json"},
		{Dir: "s3", Path: "models/s3/service/2006-03-01/s3-2006-03-01.json"},
		{Dir: "secrets-manager", Path: "models/secrets-manager/service/2017-10-17/secrets-manager-2017-10-17.json"},
		{Dir: "sns", Path: "models/sns/service/2010-03-31/sns-2010-03-31.json"},
		{Dir: "sqs", Path: "models/sqs/service/2012-11-05/sqs-2012-11-05.json"},
		{Dir: "sts", Path: "models/sts/service/2011-06-15/sts-2011-06-15.json"},
	}
}

// legalPaths are the upstream files that carry the licence, copied beside the
// models.
func legalPaths() []string { return []string{"LICENSE", "NOTICE"} }

// Manifest is UPSTREAM.json: where the vendored files came from and how to
// check them.
type Manifest struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	// Retrieved is the UTC date of the -fetch run, YYYY-MM-DD.
	Retrieved string `json:"retrieved"`
	License   string `json:"license"`
	Legal     []File `json:"legal"`
	Files     []File `json:"files"`
}

// File records one vendored file. Path is both the upstream path and the
// path under the vendor directory.
type File struct {
	Dir         string `json:"dir,omitempty"`
	Path        string `json:"path"`
	Bytes       int64  `json:"bytes"`
	GitBlobSHA1 string `json:"gitBlobSha1"`
	SHA256      string `json:"sha256"`
}

// gitBlobSHA1 is what git hash-object prints: the SHA-1 of "blob <len>\x00"
// followed by the content.
func gitBlobSHA1(content []byte) string {
	h := sha1.New() //nolint:gosec // see the import
	h.Write([]byte("blob " + strconv.Itoa(len(content)) + "\x00"))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// record describes content found at path.
func record(dir, path string, content []byte) File {
	return File{
		Dir:         dir,
		Path:        path,
		Bytes:       int64(len(content)),
		GitBlobSHA1: gitBlobSHA1(content),
		SHA256:      sha256Hex(content),
	}
}
