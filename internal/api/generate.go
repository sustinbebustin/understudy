// Package api holds the generated service packages, one per service:
// internal/api/<svc>api. Their *_gen.go files are never edited by hand;
// hand-written tests sit beside them. Regenerate with just generate, which
// verifies the vendored models and then runs the directives below, one per
// service.
package api

//go:generate go run ../cmd/gen -service cloudwatch
//go:generate go run ../cmd/gen -service dynamodb
//go:generate go run ../cmd/gen -service eventbridge
//go:generate go run ../cmd/gen -service iam
//go:generate go run ../cmd/gen -service kms
//go:generate go run ../cmd/gen -service lambda
//go:generate go run ../cmd/gen -service logs
//go:generate go run ../cmd/gen -service s3
//go:generate go run ../cmd/gen -service secretsmanager
//go:generate go run ../cmd/gen -service sns
//go:generate go run ../cmd/gen -service sqs
//go:generate go run ../cmd/gen -service sts
