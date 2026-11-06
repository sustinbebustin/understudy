package parity

import (
	"errors"
	"strings"
	"testing"
)

// The recorded values each shape case binds, and the scenario they ran in:
// account 123456789012 (masked, testdata/README.md), region us-east-2.
const (
	shapeAccount = "123456789012"
	shapeRegion  = "us-east-2"
	// testdata/parity/kms/KMS-alias/01-duplicate.json TargetKeyId.
	recordedKeyID = "157cb509-e842-4095-b0b6-e97188e0a507"
	mintedKeyID   = "0f0e0d0c-0b0a-4909-8807-060504030201"
	recordedKey   = "arn:aws:kms:us-east-2:123456789012:key/" + recordedKeyID
	mintedKey     = "arn:aws:kms:us-east-2:123456789012:key/" + mintedKeyID
	// testdata/parity/sts/R15/06-assume-ok.json RoleArn: IAM ARNs have no region.
	recordedRole = "arn:aws:iam::123456789012:role/usr-rec-20261014-trust"
	// testdata/parity/sqs/R01/02-recv1.json QueueUrl.
	recordedQueueURL = "https://sqs.us-east-2.amazonaws.com/123456789012/usr-rec-20261014-std"
	mintedQueueURL   = "http://localhost:4566/123456789012/usr-rec-20261014-std"
	recordedQueueARN = "arn:aws:sqs:us-east-2:123456789012:usr-rec-20261014-std"
	// The head of testdata/parity/sqs/R01/02-recv1.json ReceiptHandle.
	recordedHandle = "AQEBMpo34lIChKeT2KqKUxah/cRpUTa4nC4C1LGJM2QE8fL+VoE4vIImc69LtU3Z04jPhsc/Z9QAgGD39CqEBIkTg0DfXbJp=="
	mintedHandle   = "AQEBdW5kZXJzdHVkeS1taW50ZWQtcmVjZWlwdC1oYW5kbGUtMDAwMQ=="
)

func TestCheckBindAcceptsBothSidesOfTheShape(t *testing.T) {
	t.Parallel()
	handle := IDShape{Alphabet: Base64, Min: 36, Max: 1024, Prefix: "AQEB"}
	cases := []struct {
		name      string
		shape     Shape
		aws, ours string
	}{
		{"uuid", UUIDShape{}, recordedKeyID, mintedKeyID},
		{"receipt handle", handle, recordedHandle, mintedHandle},
		{"digits", IDShape{Alphabet: Digits, Min: 12, Max: 12}, shapeAccount, "000000000000"},
		{"hex", IDShape{Alphabet: Hex, Min: 1, Max: 32}, "ae7be26cdaa742ca148068d5ac90eaca", "0f"},
		{"base32", IDShape{Alphabet: Base32, Min: 17, Max: 21, Prefix: "AROA"}, "AROARZPUZDIKAAAAAAAHA", "AROAQ2BCDEFGHIJKLMN"},
		{"alnum", IDShape{Alphabet: Alnum, Min: 6, Max: 6}, "aB3dE6", "Zz09yY"},
		{"arn with a uuid name", ARNShape{Service: "kms", Resource: "key", Name: UUIDShape{}}, recordedKey, mintedKey},
		{"arn with no region", ARNShape{Service: "iam", Resource: "role"}, recordedRole, recordedRole},
		{"arn with no resource type", ARNShape{Service: "sqs"}, recordedQueueARN, recordedQueueARN},
		{"url", URLShape{Path: "/{account}/{name}"}, recordedQueueURL, mintedQueueURL},
	}
	for _, c := range cases {
		if err := CheckBind(c.shape, c.aws, c.ours, shapeAccount, shapeRegion); err != nil {
			t.Errorf("%s: CheckBind(%q, %q): %v; want nil", c.name, c.aws, c.ours, err)
		}
	}
}

// A value that does not have the shape fails the step's bind, naming the
// side that broke it.
func TestCheckBindRefusesAValueOutsideTheShape(t *testing.T) {
	t.Parallel()
	handle := IDShape{Alphabet: Base64, Min: 36, Max: 1024, Prefix: "AQEB"}
	kmsKey := ARNShape{Service: "kms", Resource: "key", Name: UUIDShape{}}
	queueURL := URLShape{Path: "/{account}/{name}"}
	cases := []struct {
		name      string
		shape     Shape
		aws, ours string
		side      string
	}{
		{"uuid upper case", UUIDShape{}, recordedKeyID, strings.ToUpper(mintedKeyID), "understudy"},
		{"uuid too short", UUIDShape{}, recordedKeyID[:8], mintedKeyID, "aws"},
		{"id without its prefix", handle, recordedHandle, "XQEB" + mintedHandle[4:], "understudy"},
		{"id shorter than min", handle, recordedHandle, "AQEBabc", "understudy"},
		{"id longer than max", IDShape{Alphabet: Digits, Min: 12, Max: 12}, shapeAccount, "1234567890123", "understudy"},
		{"id outside its alphabet", handle, recordedHandle, mintedHandle[:40] + "!", "understudy"},
		{"hex is lower case", IDShape{Alphabet: Hex, Min: 1, Max: 32}, "ae7b", "AE7B", "understudy"},
		{"base32 has no 1", IDShape{Alphabet: Base32, Min: 1, Max: 32}, "ABC2", "ABC1", "understudy"},
		{"arn of another service", kmsKey, recordedKey, strings.Replace(mintedKey, ":kms:", ":sqs:", 1), "understudy"},
		{"arn in another region", kmsKey, recordedKey, strings.Replace(mintedKey, "us-east-2", "us-west-2", 1), "understudy"},
		{"arn in another account", kmsKey, recordedKey, strings.Replace(mintedKey, shapeAccount, "000000000000", 1), "understudy"},
		{"arn of another resource type", kmsKey, recordedKey, "arn:aws:kms:us-east-2:123456789012:alias/" + mintedKeyID, "understudy"},
		{"arn with an empty name", kmsKey, recordedKey, "arn:aws:kms:us-east-2:123456789012:key/", "understudy"},
		{"arn whose name breaks its shape", kmsKey, recordedKey, "arn:aws:kms:us-east-2:123456789012:key/k1", "understudy"},
		{"arn with a type where none is", ARNShape{Service: "sqs"}, recordedQueueARN, "arn:aws:sqs:us-east-2:123456789012:queue/x", "understudy"},
		{"not an arn", kmsKey, recordedKeyID, mintedKey, "aws"},
		{"url in another account", queueURL, recordedQueueURL, "http://localhost:4566/000000000000/usr-rec-20261014-std", "understudy"},
		{"url with an extra segment", queueURL, recordedQueueURL, "http://localhost:4566/123456789012/a/b", "understudy"},
		{"url that is not absolute", queueURL, recordedQueueURL, "/123456789012/usr-rec-20261014-std", "understudy"},
		{"url that is not http", queueURL, recordedQueueURL, "ftp://localhost/123456789012/usr-rec-20261014-std", "understudy"},
	}
	for _, c := range cases {
		err := CheckBind(c.shape, c.aws, c.ours, shapeAccount, shapeRegion)
		var se *ShapeError
		if !errors.As(err, &se) {
			t.Errorf("%s: CheckBind(%q, %q) error = %v; want a *ShapeError", c.name, c.aws, c.ours, err)
			continue
		}
		want := c.ours
		if c.side == "aws" {
			want = c.aws
		}
		if se.Side != c.side || se.Value != want || se.Reason == "" {
			t.Errorf("%s: ShapeError{Side: %q, Value: %q, Reason: %q}; want side %q, value %q and a reason", c.name, se.Side, se.Value, se.Reason, c.side, want)
		}
	}
}

// Across sides: an ARN whose name has no shape must name the same resource
// on both sides, and a URL must have the same path on both.
func TestCheckBindComparesWhatTheShapeDoesNotFree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		shape     Shape
		aws, ours string
	}{
		{"arn names", ARNShape{Service: "iam", Resource: "role"}, recordedRole, "arn:aws:iam::123456789012:role/other"},
		{"url paths", URLShape{Path: "/{account}/{name}"}, recordedQueueURL, "http://localhost:4566/123456789012/other"},
	}
	for _, c := range cases {
		if err := CheckBind(c.shape, c.aws, c.ours, shapeAccount, shapeRegion); err == nil {
			t.Errorf("%s: CheckBind(%q, %q) = nil; want an error", c.name, c.aws, c.ours)
		}
		// The same value on both sides binds.
		if err := CheckBind(c.shape, c.aws, c.aws, shapeAccount, shapeRegion); err != nil {
			t.Errorf("%s: CheckBind(%q, itself): %v; want nil", c.name, c.aws, err)
		}
	}
}
