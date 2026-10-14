package cli

import (
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/config"
)

// The DynamoDB verdict comes from typed state, never from the wording of
// the server's reason: a running server's health wins over the
// configuration, Docker down is a warning whose fix is Docker's, and
// without a server the configuration decides.
func TestDynamoDBCheckReadsTypedState(t *testing.T) {
	up := admin.ServiceStatus{Status: admin.Enabled}
	disabled := admin.ServiceStatus{Status: admin.Disabled, Reason: "some reason"}
	cases := []struct {
		name      string
		mode      config.DynamoDBMode
		health    *admin.Health
		want      checkStatus
		dockerFix bool
	}{
		{"running", config.DynamoDBEnabled{}, &admin.Health{Docker: up, DynamoDBLocal: up}, checkOK, false},
		{"disabled by the server's configuration", config.DynamoDBEnabled{}, &admin.Health{Docker: up, DynamoDBLocal: disabled}, checkSkip, false},
		{"start failed", config.DynamoDBEnabled{}, &admin.Health{Docker: up, DynamoDBLocal: admin.ServiceStatus{Status: admin.Degraded, Reason: "pull failed"}}, checkSkip, false},
		{"docker down", config.DynamoDBEnabled{}, &admin.Health{Docker: admin.ServiceStatus{Status: admin.Disabled}, DynamoDBLocal: disabled}, checkWarn, true},
		{"no server, enabled", config.DynamoDBEnabled{}, nil, checkOK, false},
		{"no server, disabled", config.DynamoDBDisabled{}, nil, checkSkip, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dynamoDBCheck(tc.mode, tc.health)
			if got.Status != tc.want || (got.Fix != "") != tc.dockerFix {
				t.Errorf("dynamoDBCheck = %+v, want status %s with a Docker fix: %t", got, tc.want, tc.dockerFix)
			}
		})
	}
}
