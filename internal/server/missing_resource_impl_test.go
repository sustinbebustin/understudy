package server_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/server"
)

// The coverage report states, per operation, which order a missing
// resource follows under iam.enforce (iam-enforcement-surfaces.md 5.3,
// 12.8; IAM-ENF-14): iam.enforce.missing-resource is deny-first, naming the
// services AWS answers not-found first, each of which reports its own
// switch at resolve-first, so no operation is listed under both. IAM and
// Secrets Manager, which name a missing resource by what the request gave,
// report their by-name switch at deny-first.
func TestTheServerReportsWhichOrderAMissingResourceFollows(t *testing.T) {
	c, _, _ := enforced(t, server.IAMEnforceEnforce)
	data, err := c.Coverage(t.Context(), "json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := coverage.ParseJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]coverage.Switch{}
	for _, s := range report.Switches {
		byName[s.Name] = s
	}
	denyFirst, ok := byName["iam.enforce.missing-resource"]
	if !ok || denyFirst.Value != "deny-first" {
		t.Fatalf("iam.enforce.missing-resource = %+v, want deny-first", denyFirst)
	}
	if !slices.Contains(denyFirst.Ops, "iam.CreateRole") {
		t.Errorf("iam.enforce.missing-resource governs %v, want iam.CreateRole, which step 8 asks about", denyFirst.Ops)
	}
	for _, tc := range []struct{ name, value, op string }{
		{"iam.enforce.missing-resource.by-name", "deny-first", "iam.GetUser"},
		{"iam.enforce.missing-resource.by-name", "deny-first", "iam.GetRole"},
		{"secretsmanager.enforce.missing-resource.by-name", "deny-first", "secretsmanager.GetSecretValue"},
		{"secretsmanager.enforce.missing-resource.by-name", "deny-first", "secretsmanager.DescribeSecret"},
		{"kms.enforce.missing-resource.by-name", "resolve-first", "kms.Encrypt"},
		{"kms.enforce.missing-resource.by-name", "resolve-first", "kms.GetKeyPolicy"},
		{"sqs.enforce.missing-resource", "resolve-first", "sqs.GetQueueAttributes"},
		{"sqs.enforce.missing-resource", "resolve-first", "sqs.SendMessage"},
		{"s3.enforce.missing-resource", "resolve-first", "s3.ListObjectsV2"},
		{"s3.enforce.missing-resource", "resolve-first", "s3.GetObject"},
	} {
		s, ok := byName[tc.name]
		if !ok || s.Value != tc.value || s.Record != "IAM-ENF-14" {
			t.Errorf("%s = %+v, want %s settled by IAM-ENF-14", tc.name, s, tc.value)
			continue
		}
		if !slices.Contains(s.Ops, tc.op) {
			t.Errorf("%s governs %v, want %s", tc.name, s.Ops, tc.op)
		}
		for _, op := range s.Ops {
			if slices.Contains(denyFirst.Ops, op) {
				t.Errorf("%s is listed under both iam.enforce.missing-resource and %s", op, tc.name)
			}
		}
	}
	// Every switch IAM-ENF-14 settled is aws-recorded; those with a
	// service's own order name what was recorded and what is inferred, and
	// the global one names the services that resolve first.
	reasonOf := func(s coverage.Switch) string {
		if no, ok := s.Settles.(fidelity.NoScenario); ok {
			return no.Reason
		}
		return ""
	}
	for _, name := range []string{
		"sqs.enforce.missing-resource", "s3.enforce.missing-resource", "kms.enforce.missing-resource.by-name",
		"iam.enforce.missing-resource.by-name", "secretsmanager.enforce.missing-resource.by-name",
	} {
		s, reason := byName[name], reasonOf(byName[name])
		if s.Label != evidence.AWSRecorded || !strings.Contains(reason, "recorded: ") || !strings.Contains(reason, "inferred: ") ||
			strings.Contains(reason, "unrecorded") || strings.Contains(reason, "waits in the recording queue") {
			t.Errorf("%s = %+v, want aws-recorded, naming the recorded and the inferred operations", name, s)
		}
	}
	if reason := reasonOf(denyFirst); denyFirst.Label != evidence.AWSRecorded || strings.Contains(reason, "unrecorded") ||
		!strings.Contains(reason, "sqs.enforce.missing-resource") || !strings.Contains(reason, "s3.enforce.missing-resource") ||
		!strings.Contains(reason, "kms.enforce.missing-resource.by-name") {
		t.Errorf("iam.enforce.missing-resource = %+v, want aws-recorded, naming its SQS, S3 and KMS exceptions", denyFirst)
	}
	if s := byName["s3.enforce.missing-resource"]; slices.Contains(s.Ops, "s3.CreateBucket") {
		t.Errorf("s3.enforce.missing-resource governs s3.CreateBucket, whose bucket is meant not to exist")
	}
}
