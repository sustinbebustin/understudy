package server_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/server"
)

// The coverage report states, per operation, which order a missing
// resource follows under iam.enforce (iam-enforcement-surfaces.md 5.3,
// 12.8): iam.enforce.missing-resource is deny-first for the operations
// asked at step 8 only, and each service whose operations find their
// resource by name reports <service>.enforce.missing-resource.by-name at
// resolve-first for exactly those, so no operation is listed under both.
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
	for _, tc := range []struct{ name, op string }{
		{"iam.enforce.missing-resource.by-name", "iam.GetUser"},
		{"iam.enforce.missing-resource.by-name", "iam.GetRole"},
		{"kms.enforce.missing-resource.by-name", "kms.Encrypt"},
		{"secretsmanager.enforce.missing-resource.by-name", "secretsmanager.GetSecretValue"},
	} {
		s, ok := byName[tc.name]
		if !ok || s.Value != "resolve-first" || s.Record != "IAM-ENF-14" {
			t.Errorf("%s = %+v, want resolve-first settled by IAM-ENF-14", tc.name, s)
			continue
		}
		if !slices.Contains(s.Ops, tc.op) {
			t.Errorf("%s governs %v, want %s", tc.name, s.Ops, tc.op)
		}
		for _, op := range s.Ops {
			if slices.Contains(denyFirst.Ops, op) {
				t.Errorf("%s is listed as both deny-first and resolve-first", op)
			}
		}
	}
}
