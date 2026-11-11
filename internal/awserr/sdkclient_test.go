package awserr_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The suffix each AWS SDK for Java generation appends to a callee's error
// message, as recorded: v1 by Parameter Store for KMS (SSM-22 step 04) and
// by Step Functions for Lambda (SFN-15); v2 by EventBridge for KMS
// (eventbridge-buses.md) and by a function-ARN Task for Lambda (SFN-15).
// The v1 error code is the error's wire code, so KMS's NotFoundException is
// NotFoundException, not its Query code NotFound.
func TestSDKClientMessage(t *testing.T) {
	const id = "8476b5e8-1af2-41b1-b423-ddcfc3d3340e"
	cases := []struct {
		client awserr.SDKClient
		err    *awserr.Error
		want   string
	}{
		{
			awserr.SDKv1("AWSKMS"),
			kmsapi.ErrNotFoundException("Alias arn:aws:kms:us-east-2:123456789012:alias/us1103-none is not found."),
			"Alias arn:aws:kms:us-east-2:123456789012:alias/us1103-none is not found. (Service: AWSKMS; Status Code: 400; Error Code: NotFoundException; Request ID: " + id + "; Proxy: null)",
		},
		{
			awserr.SDKv1("AWSLambda"),
			lambdaapi.ErrTooManyRequestsException("Rate Exceeded."),
			"Rate Exceeded. (Service: AWSLambda; Status Code: 429; Error Code: TooManyRequestsException; Request ID: " + id + "; Proxy: null)",
		},
		{
			awserr.SDKv2("Kms"),
			kmsapi.ErrNotFoundException("Key 'k' does not exist"),
			"Key 'k' does not exist (Service: Kms, Status Code: 400, Request ID: " + id + ") (SDK Attempt Count: 1)",
		},
		{
			awserr.SDKv2("Lambda"),
			lambdaapi.ErrAccessDeniedException("User: u is not authorized to perform: lambda:InvokeFunction"),
			"User: u is not authorized to perform: lambda:InvokeFunction (Service: Lambda, Status Code: 403, Request ID: " + id + ") (SDK Attempt Count: 1)",
		},
	}
	for _, c := range cases {
		if got := c.client.Message(c.err, id); got != c.want {
			t.Errorf("%v.Message = %q, want %q", c.client, got, c.want)
		}
	}
}

// A client prints as the suffix it appends, with placeholders for what the
// error and the call fill in, so a switch holding one reads in the coverage
// report.
func TestSDKClientString(t *testing.T) {
	cases := map[awserr.SDKClient]string{
		awserr.SDKv1("AWSKMS"): "(Service: AWSKMS; Status Code: <status>; Error Code: <code>; Request ID: <id>; Proxy: null)",
		awserr.SDKv2("Kms"):    "(Service: Kms, Status Code: <status>, Request ID: <id>) (SDK Attempt Count: 1)",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}
