package access_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
)

// An unconditional grant on "*" also subsumes a bucket's objects statement
// and an action recorded in several modes on one resource (ARCHITECTURE.md
// 17); the record keeps every use.
func TestPolicyLeavesOutActionsGrantedOnStarImpl(t *testing.T) {
	tests := []struct {
		name    string
		uses    int
		entries func(t *testing.T) []access.Entry
		want    string
	}{
		{
			"an action on * leaves a bucket's objects statement", 2,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(app, access.Direct{}, act("s3", "GetObject")),
					use(app, access.Direct{}, act("s3", "GetObject"), arn(t, "arn:aws:s3:::b1/k1")),
				}
			},
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
		},
		{
			"an action used in two modes on one resource leaves it once", 3,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(app, access.Direct{}, act("sqs", "SendMessage")),
					use(app, access.Direct{}, act("sqs", "SendMessage"), arn(t, queueARN)),
					use(app, access.AsCaller{}, act("sqs", "SendMessage"), arn(t, queueARN)),
				}
			},
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var r access.Recorder
			record(t, &r, tc.entries(t)...)
			if got := userPolicy(t, &r); got != tc.want {
				t.Errorf("Policy =\n%s\nwant\n%s", got, tc.want)
			}
			if got := len(r.Usage(access.Filter{Principal: app})); got != tc.uses {
				t.Errorf("Usage lists %d uses of app, want %d", got, tc.uses)
			}
		})
	}
}
