package api_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// TestEveryServiceNamesItsDenialIdentity: each generated descriptor carries
// the error its service answers an authorization refusal with, in place of
// the one shared com.amazon.coral.service#AccessDeniedException 403, and
// the evidence label of iam-enforcement-surfaces.md 12.3 for it. A row's
// label is the weakest behind its code and status; the message text is a
// separate switch (12.8). Where the label is inferred or
// third-party-recorded the identity itself is not asserted here: the
// recording case named beside it settles it.
func TestEveryServiceNamesItsDenialIdentity(t *testing.T) {
	// grounds names the 12.3 row's switch and recording case, or its source.
	// The services after sts and secretsmanager were added after the note:
	// 12.3 has no source for any of them, so none is stronger than inferred.
	type row struct {
		label   evidence.Label
		grounds string
	}
	labels := map[ref.ServiceID]row{
		"sqs":             {evidence.AWSRecorded, "testdata/parity/sqs/IAM-ENF/01-get-attrs.json and 02-query.json"},
		"sns":             {evidence.AWSDoc, "the model's AuthorizationErrorException"},
		"s3":              {evidence.AWSDoc, "the model's AccessDenied and the S3 403 troubleshooting page"},
		"lambda":          {evidence.AWSRecorded, "testdata/parity/lambda/IAM-ENF/11-get.json"},
		"dynamodb":        {evidence.AWSRecorded, "testdata/parity/dynamodb/IAM-ENF/04-describe.json"},
		"eventbridge":     {evidence.AWSRecorded, "testdata/parity/events/IAM-ENF/08-describe.json"},
		"cloudwatch":      {evidence.Inferred, "switch cloudwatch.denial.identity, case IAM-ENF-09"},
		"logs":            {evidence.ThirdPartyRecorded, "switch logs.denial.status, case IAM-ENF-07"},
		"iam":             {evidence.AWSRecorded, "testdata/parity/iam/IAM-ENF/10-get-role.json"},
		"sts":             {evidence.AWSRecorded, "testdata/parity/sts/R15/06-assume-denied.json"},
		"kms":             {evidence.AWSRecorded, "testdata/parity/kms/IAM-ENF/05-describe.json"},
		"secretsmanager":  {evidence.ThirdPartyRecorded, "switch secretsmanager.denial.status, case IAM-ENF-06"},
		"apigateway":      {evidence.Inferred, "no source"},
		"apigatewayv2":    {evidence.Inferred, "no source"},
		"dynamodbstreams": {evidence.Inferred, "no source; follows dynamodb"},
		"kinesis":         {evidence.AWSRecorded, "testdata/parity/kinesis/IAM-ENF/36-kinesis.json"},
		"scheduler":       {evidence.AWSRecorded, "testdata/parity/scheduler/SCH/10-01-denied-get.json"},
		"ses":             {evidence.Inferred, "switch ses.denial.identity, cases IAM-ENF-38 and ses.md SES-28; inferred from STS and IAM"},
		"sesv2":           {evidence.Inferred, "switch ses.denial.identity, cases IAM-ENF-39 and ses.md SES-28"},
		"sfn":             {evidence.AWSRecorded, "testdata/parity/sfn/SFN-15/01-denied-list.json"},
		"ssm":             {evidence.Inferred, "no source"},
	}
	for _, svc := range generated() {
		d := svc.Denial
		if d.Identity.Shape == "" {
			t.Errorf("%s names no denial identity (dev.understudy#denialError in its overlay)", svc.ID)
			continue
		}
		want, ok := labels[svc.ID]
		if !ok {
			t.Errorf("%s: no label in this test's table; add the service's iam-enforcement-surfaces.md 12.3 row", svc.ID)
			continue
		}
		if d.Label != want.label {
			t.Errorf("%s denial label = %q, want %q (iam-enforcement-surfaces.md 12.3: %s)", svc.ID, d.Label, want.label, want.grounds)
		}
	}
}

// TestDocumentedDenialIdentities pins the identities iam-enforcement-surfaces.md
// 12.3 labels aws-doc or aws-recorded.
func TestDocumentedDenialIdentities(t *testing.T) {
	cases := []struct {
		svc   string
		got   awserr.Identity
		check func(id awserr.Identity) bool
		want  string
	}{
		{
			// The model's AuthorizationErrorException: awsQueryError
			// AuthorizationError, 403; Type Sender
			// (iam-enforcement-surfaces.md 3.1, 3.2, aws-doc).
			"sns", snsapi.Service().Denial.Identity,
			func(id awserr.Identity) bool {
				return id == snsapi.IdentityAuthorizationErrorException() &&
					id.Shape == "com.amazonaws.sns#AuthorizationErrorException" && id.QueryCode == "AuthorizationError" &&
					id.Status == http.StatusForbidden && id.Fault == awserr.Sender
			},
			"com.amazonaws.sns#AuthorizationErrorException, AuthorizationError, 403, Sender",
		},
		{
			// The model's AccessDenied, httpError 403
			// (iam-enforcement-surfaces.md 3.1, 3.2, aws-doc).
			"s3", s3api.Service().Denial.Identity,
			func(id awserr.Identity) bool {
				return id == s3api.IdentityAccessDenied() && id.Shape == "com.amazonaws.s3#AccessDenied" &&
					id.Shape.Name() == "AccessDenied" && id.Status == http.StatusForbidden
			},
			"com.amazonaws.s3#AccessDenied, 403",
		},
		{
			// <Type>Sender</Type><Code>AccessDenied</Code>, status 403
			// (testdata/parity/sts/R15/06-assume-denied.json, aws-recorded);
			// the shape is com.amazonaws.sts#AccessDenied
			// (iam-enforcement-surfaces.md 12.3, last row).
			"sts", stsapi.Service().Denial.Identity,
			func(id awserr.Identity) bool {
				return id == stsapi.IdentityAccessDenied() && id.Shape == "com.amazonaws.sts#AccessDenied" &&
					id.QueryCode == "AccessDenied" && id.Status == http.StatusForbidden && id.Fault == awserr.Sender
			},
			"com.amazonaws.sts#AccessDenied, AccessDenied, 403, Sender",
		},
		{
			// HTTP/1.1 400 with __type
			// com.amazon.coral.service#AccessDeniedException
			// (testdata/parity/sfn/SFN-15/01-denied-list.json, aws-recorded).
			// AWS's message key there is "Message", which the shared Coral
			// shape does not spell; that is not asserted here.
			"sfn", sfnapi.Service().Denial.Identity,
			func(id awserr.Identity) bool {
				return id == sfnapi.IdentityAccessDeniedException() && id.JSONType == "com.amazon.coral.service#AccessDeniedException" &&
					id.Status == http.StatusBadRequest
			},
			"com.amazon.coral.service#AccessDeniedException, 400",
		},
		{
			// 403, x-amzn-query-error AccessDenied;Sender and __type
			// com.amazon.coral.service#AccessDeniedException over JSON
			// (testdata/parity/sqs/IAM-ENF/01-get-attrs.json); Type Sender,
			// Code AccessDenied over Query
			// (testdata/parity/sqs/IAM-ENF/02-query.json), aws-recorded.
			"sqs", sqsapi.Service().Denial.Identity,
			func(id awserr.Identity) bool {
				return id.JSONType == "com.amazon.coral.service#AccessDeniedException" && id.QueryCode == "AccessDenied" &&
					id.Status == http.StatusForbidden && id.Fault == awserr.Sender
			},
			"com.amazon.coral.service#AccessDeniedException, AccessDenied, 403, Sender",
		},
		// The JSON services below answer the capital "Message" key; whether
		// the shape spells it is the message-key follow-up, not asserted here.
		{
			// 400, __type com.amazon.coral.service#AccessDeniedException
			// (testdata/parity/dynamodb/IAM-ENF/04-describe.json, aws-recorded).
			"dynamodb", dynamodbapi.Service().Denial.Identity,
			func(id awserr.Identity) bool {
				return id.JSONType == "com.amazon.coral.service#AccessDeniedException" && id.Status == http.StatusBadRequest
			},
			"com.amazon.coral.service#AccessDeniedException, 400",
		},
		{
			// 400, __type AccessDeniedException
			// (testdata/parity/kms/IAM-ENF/05-describe.json, aws-recorded).
			"kms", kmsapi.Service().Denial.Identity,
			func(id awserr.Identity) bool {
				return id.JSONType == "AccessDeniedException" && id.Status == http.StatusBadRequest
			},
			"AccessDeniedException, 400",
		},
		{
			// 400, __type AccessDeniedException
			// (testdata/parity/events/IAM-ENF/08-describe.json, aws-recorded).
			"eventbridge", eventbridgeapi.Service().Denial.Identity,
			func(id awserr.Identity) bool {
				return id.JSONType == "AccessDeniedException" && id.Status == http.StatusBadRequest
			},
			"AccessDeniedException, 400",
		},
		{
			// 403, <Type>Sender</Type><Code>AccessDenied</Code> in the IAM
			// namespace (testdata/parity/iam/IAM-ENF/10-get-role.json, aws-recorded).
			"iam", iamapi.Service().Denial.Identity,
			func(id awserr.Identity) bool {
				return id.QueryCode == "AccessDenied" && id.Status == http.StatusForbidden && id.Fault == awserr.Sender
			},
			"AccessDenied, 403, Sender",
		},
		{
			// 403, x-amzn-ErrorType AccessDeniedException
			// (testdata/parity/lambda/IAM-ENF/11-get.json, aws-recorded).
			"lambda", lambdaapi.Service().Denial.Identity,
			func(id awserr.Identity) bool {
				return id.Shape.Name() == "AccessDeniedException" && id.Status == http.StatusForbidden
			},
			"AccessDeniedException, 403",
		},
	}
	for _, c := range cases {
		if !c.check(c.got) {
			t.Errorf("%s denial identity = %+v, want %s", c.svc, c.got, c.want)
		}
	}
}

// DynamoDB's developer guide lists AccessDeniedException under "HTTP status
// code 400" (iam-enforcement-surfaces.md 3.2, aws-doc), so its generic
// AccessDeniedException, the one fault rules inject, answers 400. Whether
// an IAM denial uses that identity is inferred (IAM-ENF-04).
func TestDynamoDBAccessDeniedExceptionIs400(t *testing.T) {
	if id := dynamodbapi.IdentityAccessDeniedException(); id.Status != http.StatusBadRequest || id.QueryCode != "AccessDeniedException" {
		t.Errorf("dynamodb AccessDeniedException = %+v, want status 400", id)
	}
}
