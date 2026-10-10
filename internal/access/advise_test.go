package access_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// parse returns the stored policy in.
func parse(t *testing.T, in string) *policydoc.Document {
	t.Helper()
	doc, err := policydoc.Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse(%s): %v", in, err)
	}
	return &doc
}

// SNS delivers to an SQS queue only when the queue policy allows
// sqs:SendMessage for the topic (ARCHITECTURE.md 16).
func TestAdviseSNSToSQS(t *testing.T) {
	allowTopic := `{"Sid":"topic","Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},
"Action":"sqs:SendMessage","Resource":"` + queueARN + `","Condition":{"ArnEquals":{"aws:SourceArn":"` + topicARN + `"}}}`
	sendFromTopic := `{"Version":"2012-10-17","Statement":[` + allowTopic + `]}`
	denySend := `"Effect":"Deny","Principal":"*","Action":"sqs:SendMessage","Resource":"` + queueARN + `"}`
	denied := `{"Version":"2012-10-17","Statement":[` + allowTopic + `,{"Sid":"DenyTopic",` + denySend + `]}`
	deniedNoSid := `{"Version":"2012-10-17","Statement":[` + allowTopic + `,{` + denySend + `]}`
	tests := []struct {
		name   string
		policy *policydoc.Document
		allow  bool
		// reason holds what a WouldDeny Reason mentions besides the
		// principal, action, source and the attempt understudy makes anyway.
		reason []string
	}{
		{
			name:   "statement for the topic",
			policy: parse(t, sendFromTopic),
			allow:  true,
		},
		{
			name:   "public statement scoped by account",
			policy: parse(t, `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":"*","Action":"sqs:*","Resource":"*","Condition":{"StringEquals":{"aws:SourceAccount":"111111111111"}}}}`),
			allow:  true,
		},
		{
			name:   "no policy",
			policy: nil,
			reason: []string{queueARN + " has no resource policy"},
		},
		{
			name:   "statement for another topic",
			policy: parse(t, strings.Replace(sendFromTopic, topicARN, "arn:aws:sns:us-east-1:111111111111:other", 1)),
			reason: []string{"the resource policy of " + queueARN + " does not allow"},
		},
		{
			name:   "statement for another action",
			policy: parse(t, strings.Replace(sendFromTopic, "sqs:SendMessage", "sqs:ReceiveMessage", 1)),
			reason: []string{"does not allow"},
		},
		{
			name:   "statement for another principal",
			policy: parse(t, strings.Replace(sendFromTopic, "sns.amazonaws.com", "events.amazonaws.com", 1)),
			reason: []string{"does not allow"},
		},
		{
			name:   "explicit Deny with a Sid",
			policy: parse(t, denied),
			reason: []string{`The statement with Sid "DenyTopic"`, "explicitly denies", "cannot override an explicit Deny"},
		},
		{
			name:   "explicit Deny without a Sid",
			policy: parse(t, deniedNoSid),
			reason: []string{"Statement[1] (it has no Sid)", "explicitly denies", "cannot override an explicit Deny"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := access.Advise(tc.policy, sns, act("sqs", "SendMessage"), arn(t, topicARN), arn(t, queueARN))
			switch a := got.(type) {
			case access.Allowed:
				if !tc.allow {
					t.Errorf("Advise = Allowed, want WouldDeny mentioning %q", strings.Join(tc.reason, `", "`))
				}
			case access.WouldDeny:
				if tc.allow {
					t.Errorf("Advise = WouldDeny{%s}, want Allowed", a.Reason)
				}
				for _, want := range append([]string{"sns.amazonaws.com", "sqs:SendMessage", topicARN, "it attempts the delivery anyway"}, tc.reason...) {
					if !strings.Contains(a.Reason, want) {
						t.Errorf("Reason %q does not mention %q", a.Reason, want)
					}
				}
			default:
				t.Fatalf("Advise = %#v", got)
			}
		})
	}
}

// EventBridge invokes a function only with a lambda:InvokeFunction
// permission for the rule; Lambda's AddPermission writes it with ArnLike
// (lambda-api.md 7.3).
func TestAdviseEventBridgeToLambda(t *testing.T) {
	fn := "arn:aws:lambda:us-east-1:111111111111:function:worker"
	rule := "arn:aws:events:us-east-1:111111111111:rule/nightly"
	policy := parse(t, `{"Version":"2012-10-17","Id":"default","Statement":[{"Sid":"events","Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},
"Action":"lambda:InvokeFunction","Resource":"`+fn+`","Condition":{"ArnLike":{"AWS:SourceArn":"`+rule+`"}}}]}`)
	if got := access.Advise(policy, events, act("lambda", "InvokeFunction"), arn(t, rule), arn(t, fn)); got != (access.Allowed{}) {
		t.Errorf("Advise = %#v, want Allowed", got)
	}
	other := arn(t, "arn:aws:events:us-east-1:111111111111:rule/hourly")
	got, ok := access.Advise(policy, events, act("lambda", "InvokeFunction"), other, arn(t, fn)).(access.WouldDeny)
	if !ok {
		t.Fatalf("Advise for another rule = %#v, want WouldDeny", got)
	}
	// A function policy is written through AddPermission, not edited, so
	// the recovery step names it rather than a statement to add.
	for _, want := range []string{
		"aws lambda add-permission", "--function-name " + fn, "--action lambda:InvokeFunction",
		"--principal events.amazonaws.com", "--source-arn " + other.String(), "aws_lambda_permission",
	} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("Reason %q does not mention %q", got.Reason, want)
		}
	}
	if strings.Contains(got.Reason, "Add an Allow statement") {
		t.Errorf("Reason %q tells the user to edit the function policy directly", got.Reason)
	}
}
