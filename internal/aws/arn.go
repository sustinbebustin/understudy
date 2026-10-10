package aws

import (
	"fmt"
	"strings"
)

// ARN is an Amazon Resource Name. Region and Account may be empty: S3 ARNs
// have neither (arn:aws:s3:::bucket/key) and IAM ARNs have no region
// (arn:aws:iam::123456789012:role/name, arn:aws:iam::aws:policy/name). The
// account is kept verbatim because AWS-managed IAM policies use "aws".
type ARN struct {
	Partition string
	Service   string
	Region    string
	Account   string
	// Resource is everything after the fifth colon and may contain colons
	// and slashes (function:name:qualifier, bucket/key).
	Resource string
}

// ARNProblem says why a string is not an ARN.
type ARNProblem uint8

// ARNProblem values.
const (
	ARNMissingPrefix ARNProblem = iota + 1
	ARNTooFewElements
	ARNEmptyPartition
	ARNEmptyService
	ARNEmptyResource
)

// ARNError reports a string that is not an ARN.
type ARNError struct {
	Input   string
	Problem ARNProblem
	// Elements is the number of colon-separated elements the input has.
	Elements int
}

func (e *ARNError) Error() string {
	var why string
	switch e.Problem {
	case ARNMissingPrefix:
		why = `it does not start with "arn:"`
	case ARNTooFewElements:
		why = fmt.Sprintf("an ARN has at least 6 colon-separated elements, this has %d", e.Elements)
	case ARNEmptyPartition:
		why = "the partition is empty"
	case ARNEmptyService:
		why = "the service is empty"
	case ARNEmptyResource:
		why = "the resource is empty"
	default:
		why = "it is malformed"
	}
	return fmt.Sprintf("%q is not an ARN: %s; the form is arn:partition:service:region:account:resource", e.Input, why)
}

// ParseARN splits s into its six elements. Partition, service and resource
// must be non-empty; region and account may be empty.
func ParseARN(s string) (ARN, error) {
	parts := strings.SplitN(s, ":", 6)
	fail := func(p ARNProblem) (ARN, error) {
		return ARN{}, &ARNError{Input: s, Problem: p, Elements: strings.Count(s, ":") + 1}
	}
	if parts[0] != "arn" {
		return fail(ARNMissingPrefix)
	}
	if len(parts) < 6 {
		return fail(ARNTooFewElements)
	}
	a := ARN{Partition: parts[1], Service: parts[2], Region: parts[3], Account: parts[4], Resource: parts[5]}
	switch {
	case a.Partition == "":
		return fail(ARNEmptyPartition)
	case a.Service == "":
		return fail(ARNEmptyService)
	case a.Resource == "":
		return fail(ARNEmptyResource)
	}
	return a, nil
}

// String formats the ARN; for an ARN from ParseARN it returns the input.
func (a ARN) String() string {
	return "arn:" + a.Partition + ":" + a.Service + ":" + a.Region + ":" + a.Account + ":" + a.Resource
}
