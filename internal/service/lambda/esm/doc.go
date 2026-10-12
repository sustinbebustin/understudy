// Package esm holds the pure parts of Lambda's SQS event source mappings:
// the SQS record a function receives, batch assembly under the 6 MB
// payload limit, filter criteria under Lambda's pattern profile, and the
// reading of partial batch responses. The poller that drives them runs in
// package lambda (esm_runtime.go), where its decisions are listed.
package esm
