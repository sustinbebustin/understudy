package secretsmanager_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	sm "github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
)

// usesOn lists h's recorded uses of service by the account root as
// "<action> on <resource>", "*" for AnyResource, sorted.
func usesOn(h *harness, service string, keep func(*access.Use) bool) []string {
	h.Helper()
	root, err := access.ParsePrincipal("arn:aws:iam::000000000000:root")
	if err != nil {
		h.Fatal(err)
	}
	var out []string
	uses := h.Access.Usage(access.Filter{Principal: root, Service: service})
	for i := range uses {
		u := &uses[i]
		if !keep(u) {
			continue
		}
		on := "*"
		if r, ok := u.Resource.(access.OnResource); ok {
			on = r.ARN.String()
		}
		out = append(out, u.Action.String()+" on "+on)
	}
	slices.Sort(out)
	return out
}

// CreateSecret is authorized and recorded on the secret it creates: the
// Service Authorization Reference lists the Secret resource type for
// secretsmanager:CreateSecret and secretsmanager:TagResource, so a policy
// rendered from the record grants them on `secret:<name>-??????`, not on
// "*" (ARCHITECTURE.md 17).
func TestCreateSecretIsRecordedOnTheSecret(t *testing.T) {
	run(t, func(h *harness) {
		arn := val(must(h, sm.OpCreateSecret, &sm.CreateSecretInput{
			Name: "app/db", ClientRequestToken: new(h.token()), SecretString: new("x"),
			Tags: []sm.Tag{{Key: new("team"), Value: new("a")}},
		}).ARN)
		got := usesOn(h, "secretsmanager", func(u *access.Use) bool { return u.Action.Name != "GetSecretValue" })
		want := []string{"secretsmanager:CreateSecret on " + arn, "secretsmanager:TagResource on " + arn}
		if !slices.Equal(got, want) {
			t.Errorf("recorded secretsmanager uses = %v, want %v", got, want)
		}
	})
}
