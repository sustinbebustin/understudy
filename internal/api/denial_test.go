package api_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
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
		"sqs":             {evidence.Inferred, "switches sqs.denial.json-identity and sqs.denial.query-text, cases IAM-ENF-01 and IAM-ENF-02"},
		"sns":             {evidence.AWSDoc, "the model's AuthorizationErrorException"},
		"s3":              {evidence.AWSDoc, "the model's AccessDenied and the S3 403 troubleshooting page"},
		"lambda":          {evidence.Inferred, "switch lambda.denial.body, case IAM-ENF-11 (the recorded body is for another account's ARN)"},
		"dynamodb":        {evidence.Inferred, "switch dynamodb.denial.type, case IAM-ENF-04"},
		"eventbridge":     {evidence.Inferred, "switch events.denial.status, case IAM-ENF-08"},
		"cloudwatch":      {evidence.Inferred, "switch cloudwatch.denial.identity, case IAM-ENF-09"},
		"logs":            {evidence.ThirdPartyRecorded, "switch logs.denial.status, case IAM-ENF-07"},
		"iam":             {evidence.Inferred, "switch iam.denial.identity, case IAM-ENF-10"},
		"sts":             {evidence.AWSRecorded, "testdata/parity/sts/R15/06-assume-denied.json"},
		"kms":             {evidence.Inferred, "switch kms.denial.status, case IAM-ENF-05"},
		"secretsmanager":  {evidence.ThirdPartyRecorded, "switch secretsmanager.denial.status, case IAM-ENF-06"},
		"apigateway":      {evidence.Inferred, "no source"},
		"apigatewayv2":    {evidence.Inferred, "no source"},
		"dynamodbstreams": {evidence.Inferred, "no source; follows dynamodb"},
		"kinesis":         {evidence.Inferred, "no source"},
		"scheduler":       {evidence.Inferred, "switch scheduler.denial.status, cases IAM-ENF-37 and scheduler.md SCH-10 step 01"},
		"ses":             {evidence.Inferred, "switch ses.denial.identity, cases IAM-ENF-38 and ses.md SES-28; inferred from STS and IAM"},
		"sesv2":           {evidence.Inferred, "switch ses.denial.identity, cases IAM-ENF-39 and ses.md SES-28"},
		"sfn":             {evidence.Inferred, "switch sfn.denial.status, case SFN-15"},
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
			// Over Query the code is AccessDenied: "An error occurred
			// (AccessDenied) when calling the SendMessage operation"
			// (iam-enforcement-surfaces.md 3.2, aws-doc). Its status and
			// JSON identity are inferred (IAM-ENF-01, IAM-ENF-02).
			"sqs", sqsapi.Service().Denial.Identity,
			func(id awserr.Identity) bool { return id.QueryCode == "AccessDenied" },
			"Query code AccessDenied",
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
