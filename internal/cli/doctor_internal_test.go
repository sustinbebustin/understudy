package cli

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/config"
)

// The licence verdict on a running server comes from typed state, never
// from the wording of the server's reason: a disabled DynamoDB Local with
// Docker up warns with the accept flag unless the configuration disables
// DynamoDB.
func TestLicenceCheckReadsTypedState(t *testing.T) {
	up := admin.ServiceStatus{Status: admin.Enabled}
	// The reason names neither the flag nor the variable.
	disabled := admin.ServiceStatus{Status: admin.Disabled, Reason: "waiting for a decision"}
	cases := []struct {
		name    string
		licence config.Licence
		health  admin.Health
		want    checkStatus
		fixFlag bool
	}{
		{"awaiting", config.AwaitingLicence{}, admin.Health{Docker: up, DynamoDBLocal: disabled}, checkWarn, true},
		{"accepted here, not by the server", config.LicenceAccepted{}, admin.Health{Docker: up, DynamoDBLocal: disabled}, checkWarn, true},
		{"disabled", config.DynamoDBDisabled{}, admin.Health{Docker: up, DynamoDBLocal: disabled}, checkSkip, false},
		{"running", config.AwaitingLicence{}, admin.Health{Docker: up, DynamoDBLocal: up}, checkOK, false},
		{"start failed", config.LicenceAccepted{}, admin.Health{Docker: up, DynamoDBLocal: admin.ServiceStatus{Status: admin.Degraded, Reason: "pull failed"}}, checkSkip, false},
		{"docker down", config.AwaitingLicence{}, admin.Health{Docker: admin.ServiceStatus{Status: admin.Disabled}, DynamoDBLocal: disabled}, checkWarn, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := licenceCheck(tc.licence, &tc.health)
			if got.Status != tc.want || strings.Contains(got.Fix, "--"+config.AcceptLicenceFlag) != tc.fixFlag {
				t.Errorf("licenceCheck = %+v, want status %s with the accept flag in the fix: %t", got, tc.want, tc.fixFlag)
			}
		})
	}
}
