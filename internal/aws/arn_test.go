package aws_test

import (
	"errors"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// element draws an ARN element without colons.
func element() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-z0-9\-]{1,12}`)
}

func standardARN() *rapid.Generator[aws.ARN] {
	return rapid.Custom(func(t *rapid.T) aws.ARN {
		return aws.ARN{
			Partition: rapid.SampledFrom([]string{"aws", "aws-cn", "aws-us-gov"}).Draw(t, "partition"),
			Service:   element().Draw(t, "service"),
			Region:    element().Draw(t, "region"),
			Account:   rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"),
			Resource:  rapid.StringMatching(`[a-zA-Z0-9:/_.\-*$]{1,40}`).Draw(t, "resource"),
		}
	})
}

// s3ARN has neither region nor account; the key may hold any text.
func s3ARN() *rapid.Generator[aws.ARN] {
	return rapid.Custom(func(t *rapid.T) aws.ARN {
		resource := element().Draw(t, "bucket")
		if rapid.Bool().Draw(t, "object") {
			resource += "/" + rapid.String().Draw(t, "key")
		}
		return aws.ARN{Partition: "aws", Service: "s3", Resource: resource}
	})
}

// iamARN has no region; the account is 12 digits or "aws" for managed policies.
func iamARN() *rapid.Generator[aws.ARN] {
	return rapid.Custom(func(t *rapid.T) aws.ARN {
		return aws.ARN{
			Partition: "aws",
			Service:   "iam",
			Account:   rapid.OneOf(rapid.Just("aws"), rapid.StringMatching(`[0-9]{12}`)).Draw(t, "account"),
			Resource: rapid.SampledFrom([]string{"role/", "policy/", "user/", "instance-profile/"}).Draw(t, "kind") +
				rapid.StringMatching(`[a-zA-Z0-9+=,.@_/\-]{1,30}`).Draw(t, "name"),
		}
	})
}

func TestARNFormatParseRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		want := rapid.OneOf(standardARN(), s3ARN(), iamARN()).Draw(t, "arn")
		s := want.String()
		got, err := aws.ParseARN(s)
		if err != nil {
			t.Fatalf("ParseARN(%q): %v", s, err)
		}
		if got != want {
			t.Fatalf("ParseARN(%q) = %+v, want %+v", s, got, want)
		}
		if got.String() != s {
			t.Fatalf("String() = %q, want %q", got.String(), s)
		}
	})
}

func TestParseARNKnownForms(t *testing.T) {
	tests := []struct {
		in   string
		want aws.ARN
	}{
		{"arn:aws:sqs:us-east-1:000000000000:orders", aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "orders"}},
		{"arn:aws:s3:::my-bucket/a/b:c", aws.ARN{Partition: "aws", Service: "s3", Resource: "my-bucket/a/b:c"}},
		{"arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole", aws.ARN{Partition: "aws", Service: "iam", Account: "aws", Resource: "policy/service-role/AWSLambdaBasicExecutionRole"}},
		{"arn:aws:lambda:us-east-1:000000000000:function:worker:live", aws.ARN{Partition: "aws", Service: "lambda", Region: "us-east-1", Account: "000000000000", Resource: "function:worker:live"}},
	}
	for _, tt := range tests {
		got, err := aws.ParseARN(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseARN(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
}

func TestParseARNRejects(t *testing.T) {
	tests := []struct {
		in       string
		problem  aws.ARNProblem
		elements int
	}{
		{"", aws.ARNMissingPrefix, 1},
		{"urn:aws:sqs:us-east-1:000000000000:q", aws.ARNMissingPrefix, 6},
		{"arn:aws", aws.ARNTooFewElements, 2},
		{"arn:aws:sqs:us-east-1:000000000000", aws.ARNTooFewElements, 5},
		{"arn::sqs:us-east-1:000000000000:q", aws.ARNEmptyPartition, 6},
		{"arn:aws::us-east-1:000000000000:q", aws.ARNEmptyService, 6},
		{"arn:aws:sqs:us-east-1:000000000000:", aws.ARNEmptyResource, 6},
	}
	for _, tt := range tests {
		_, err := aws.ParseARN(tt.in)
		var target *aws.ARNError
		if !errors.As(err, &target) || target.Problem != tt.problem || target.Elements != tt.elements || target.Input != tt.in {
			t.Errorf("ParseARN(%q) error = %#v, want problem %d with %d elements", tt.in, err, tt.problem, tt.elements)
		}
	}
}
