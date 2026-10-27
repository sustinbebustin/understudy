package server_test

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// A principal whose only recorded call is sts:GetCallerIdentity has no
// policy to write, since that action needs no permission and the rendered
// document leaves it out (iam-enforcement-surfaces.md 5.1, 12.6), though
// GET /iam/usage lists the call. GET /iam/policy answers 404 saying so,
// not that no call is recorded; a principal with no call at all keeps that
// message.
func TestIAMPolicySaysWhenTheRecordedCallsNeedNoPermission(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	u := accessKey(t, endpoint, "u")
	if got := query(t, u, endpoint, "sts", url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}); got.status != http.StatusOK {
		t.Fatalf("GetCallerIdentity = %d %s", got.status, got.body)
	}
	usage, err := c.IAMUsage(t.Context(), admin.UsageQuery{Principal: userU})
	if err != nil || len(usage) != 1 || usage[0].Action != "sts:GetCallerIdentity" {
		t.Fatalf("IAMUsage = %+v, %v; want the one sts:GetCallerIdentity use", usage, err)
	}
	for principal, want := range map[string]string{
		userU:                                   "the calls recorded for " + userU + " need no permission (sts:GetCallerIdentity), so there is no policy to write",
		"arn:aws:iam::000000000000:user/nobody": "no call by arn:aws:iam::000000000000:user/nobody is recorded, so there is no policy to write",
	} {
		_, err := c.IAMPolicy(t.Context(), principal)
		var e *admin.Error
		if !errors.As(err, &e) || e.Code != admin.NotFound || e.Message != want {
			t.Errorf("IAMPolicy(%s) = %v, want NotFound %q", principal, err, want)
		}
	}
}
