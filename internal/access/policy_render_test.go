package access_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// use is one successful call by principal of action on res in mode.
func use(principal access.Principal, mode access.Mode, action registry.IAMAction, res ...aws.ARN) access.Entry {
	return access.Entry{Principal: principal, Mode: mode, Service: action.Prefix, Actions: []registry.IAMAction{action}, Resources: res, Outcome: access.Succeeded{}, At: t0}
}

// A segment understudy mints renders as the narrowest pattern matching
// every AWS-form id of its type, so a policy rendered from one run allows
// the same calls in a run whose ids differ; the pattern is also the
// grouping key, so N minted resources under the same actions give one
// statement (ARCHITECTURE.md 17; resource ARN formats from the Service
// Authorization Reference tables cited in minted_test.go).
func TestPolicyRendersMintedSegmentsAsWildcards(t *testing.T) {
	const (
		otherKey  = "arn:aws:kms:us-east-1:111111111111:key/0f0e0d0c-0b0a-4908-8706-050403020100"
		aliasARN  = "arn:aws:kms:us-east-1:111111111111:alias/app"
		dbSecret  = "arn:aws:secretsmanager:us-east-1:111111111111:secret:db-a1b2c3" //nolint:gosec // G101: an ARN, not a credential
		dbAgain   = "arn:aws:secretsmanager:us-east-1:111111111111:secret:db-Z9y8X7"
		apiSecret = "arn:aws:secretsmanager:us-east-1:111111111111:secret:api-k3K3k3" //nolint:gosec // G101: an ARN, not a credential
		subA      = topicARN + ":0f1e2d3c-4b5a-4968-8778-695a4b3c2d1e"
		subB      = topicARN + ":1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
		mappingA  = "arn:aws:lambda:us-east-1:111111111111:event-source-mapping:6b2f1c3a-1d2e-4f5a-8b6c-7d8e9f0a1b2c"
		mappingB  = "arn:aws:lambda:us-east-1:111111111111:event-source-mapping:7c3a2d4b-2e3f-4a6b-9c7d-8e9f0a1b2c3d"
		// Lambda's execution-environment streams
		// (internal/service/lambda/destination/testdata/success-sqs.json,
		// log_stream_name).
		envA  = groupARN + ":log-stream:2026/10/26/[$LATEST]0123456789abcdef0123456789abcdef"
		envB  = groupARN + ":log-stream:2026/10/27/[$LATEST]fedcba9876543210fedcba9876543210"
		named = groupARN + ":log-stream:mine"
	)
	tests := []struct {
		name    string
		of      access.Identity
		entries func(t *testing.T) []access.Entry
		want    string
	}{
		{
			"kms keys, one statement; an alias stays exact", app,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(app, access.AsCaller{}, act("kms", "Decrypt"), arn(t, keyARN)),
					use(app, access.Direct{}, act("kms", "Decrypt"), arn(t, otherKey)),
					use(app, access.Direct{}, act("kms", "DescribeKey"), arn(t, aliasARN)),
				}
			},
			`{"Version":"2012-10-17","Statement":[` +
				`{"Effect":"Allow","Action":"kms:Decrypt","Resource":"arn:aws:kms:us-east-1:111111111111:key/*"},` +
				`{"Effect":"Allow","Action":"kms:DescribeKey","Resource":"` + aliasARN + `"}]}`,
		},
		{
			"secrets, one statement per name", app,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(app, access.Direct{}, act("secretsmanager", "GetSecretValue"), arn(t, dbSecret)),
					use(app, access.Direct{}, act("secretsmanager", "GetSecretValue"), arn(t, dbAgain)),
					use(app, access.Direct{}, act("secretsmanager", "GetSecretValue"), arn(t, apiSecret)),
				}
			},
			`{"Version":"2012-10-17","Statement":[` +
				`{"Effect":"Allow","Action":"secretsmanager:GetSecretValue","Resource":"arn:aws:secretsmanager:us-east-1:111111111111:secret:api-??????"},` +
				`{"Effect":"Allow","Action":"secretsmanager:GetSecretValue","Resource":"arn:aws:secretsmanager:us-east-1:111111111111:secret:db-??????"}]}`,
		},
		{
			"sns subscriptions, one statement; the topic stays exact", app,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(app, access.Direct{}, act("sns", "Unsubscribe"), arn(t, subA)),
					use(app, access.Direct{}, act("sns", "Unsubscribe"), arn(t, subB)),
					use(app, access.Direct{}, act("sns", "Publish"), arn(t, topicARN)),
				}
			},
			`{"Version":"2012-10-17","Statement":[` +
				`{"Effect":"Allow","Action":"sns:Publish","Resource":"` + topicARN + `"},` +
				`{"Effect":"Allow","Action":"sns:Unsubscribe","Resource":"` + topicARN + `:*"}]}`,
		},
		{
			"event source mappings, one statement", app,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(app, access.Direct{}, act("lambda", "DeleteEventSourceMapping"), arn(t, mappingA)),
					use(app, access.Direct{}, act("lambda", "DeleteEventSourceMapping"), arn(t, mappingB)),
				}
			},
			`{"Version":"2012-10-17","Statement":[` +
				`{"Effect":"Allow","Action":"lambda:DeleteEventSourceMapping","Resource":"arn:aws:lambda:us-east-1:111111111111:event-source-mapping:*"}]}`,
		},
		{
			"execution-role log streams, one statement; a stream the function names stays exact", worker,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(worker, access.AsExecutionRole{}, act("logs", "PutLogEvents"), arn(t, envA)),
					use(worker, access.AsExecutionRole{}, act("logs", "PutLogEvents"), arn(t, envB)),
					use(worker, access.Direct{}, act("logs", "PutLogEvents"), arn(t, named)),
				}
			},
			`{"Version":"2012-10-17","Statement":[` +
				`{"Effect":"Allow","Action":"logs:PutLogEvents","Resource":"` + groupARN + `:log-stream:*"},` +
				`{"Effect":"Allow","Action":"logs:PutLogEvents","Resource":"` + named + `"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var r access.Recorder
			entries := tc.entries(t)
			record(t, &r, entries...)
			doc, ok := r.Policy(tc.of)
			if !ok {
				t.Fatalf("Policy(%s) reports no policy", tc.of)
			}
			if got := string(policydoc.Encode(doc)); got != tc.want {
				t.Errorf("Policy =\n%s\nwant\n%s", got, tc.want)
			}
			if err := policydoc.ValidateIdentity(doc); err != nil {
				t.Errorf("the rendered policy is not a valid identity policy: %v", err)
			}
			// The record keeps the exact ARNs: only the rendering changes.
			for _, e := range entries {
				found := false
				for _, u := range r.Usage(access.Filter{Principal: tc.of}) {
					if on, isOn := u.Resource.(access.OnResource); isOn && on.ARN == e.Resources[0] {
						found = true
					}
				}
				if !found {
					t.Errorf("Usage lacks the exact ARN %s", e.Resources[0])
				}
			}
		})
	}
}

// An action the document grants on "*" without a condition is left out of
// every other statement, and a statement left with no action is dropped;
// a conditioned grant on "*" subsumes nothing. The record itself is
// unchanged: failed calls stay recorded, since an application's handled
// failures need permissions too (ARCHITECTURE.md 17).
func TestPolicyLeavesOutActionsGrantedOnStar(t *testing.T) {
	stream := "arn:aws:kinesis:us-east-1:111111111111:stream/orders"
	sm := "arn:aws:states:us-east-1:111111111111:stateMachine:m"
	tests := []struct {
		name string
		// uses is how many uses of app the record holds.
		uses    int
		entries func(t *testing.T) []access.Entry
		want    string
	}{
		{
			"an action on * leaves the resource statement, which keeps its other actions", 3,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(app, access.Direct{}, act("sqs", "GetQueueUrl")),
					{Principal: app, Mode: access.Direct{}, Service: "sqs", Actions: []registry.IAMAction{act("sqs", "GetQueueUrl")}, Resources: []aws.ARN{arn(t, queueARN)}, Outcome: access.Failed{Code: "AWS.SimpleQueueService.NonExistentQueue"}, At: t0},
					use(app, access.Direct{}, act("sqs", "SendMessage"), arn(t, queueARN)),
				}
			},
			`{"Version":"2012-10-17","Statement":[` +
				`{"Effect":"Allow","Action":"sqs:GetQueueUrl","Resource":"*"},` +
				`{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"` + queueARN + `"}]}`,
		},
		{
			"a statement left with no action is dropped", 2,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(app, access.Direct{}, act("kinesis", "ListStreams")),
					use(app, access.Direct{}, act("kinesis", "ListStreams"), arn(t, stream)),
				}
			},
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kinesis:ListStreams","Resource":"*"}]}`,
		},
		{
			"an action on * in any mode subsumes a minted pattern too", 3,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(app, access.Direct{}, act("kms", "Decrypt")),
					use(app, access.AsCaller{}, act("kms", "Decrypt"), arn(t, keyARN)),
					use(app, access.AsCaller{}, act("kms", "GenerateDataKey"), arn(t, keyARN)),
				}
			},
			`{"Version":"2012-10-17","Statement":[` +
				`{"Effect":"Allow","Action":"kms:Decrypt","Resource":"*"},` +
				`{"Effect":"Allow","Action":"kms:GenerateDataKey","Resource":"arn:aws:kms:us-east-1:111111111111:key/*"}]}`,
		},
		{
			"a conditioned grant on * subsumes nothing", 3,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					passRole(t, sm, access.AnyResource{}, "states.amazonaws.com"),
					passRole(t, sm, access.OnResource{ARN: arn(t, roleARN)}, ""),
				}
			},
			`{"Version":"2012-10-17","Statement":[` +
				`{"Effect":"Allow","Action":"iam:PassRole","Resource":"*","Condition":{"StringEquals":{"iam:PassedToService":"states.amazonaws.com"}}},` +
				`{"Effect":"Allow","Action":"iam:PassRole","Resource":"` + roleARN + `"},` +
				`{"Effect":"Allow","Action":"states:CreateStateMachine","Resource":"` + sm + `"}]}`,
		},
		{
			"an unconditional grant on * subsumes a conditioned one", 3,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					passRole(t, sm, access.AnyResource{}, ""),
					passRole(t, sm, access.OnResource{ARN: arn(t, roleARN)}, "states.amazonaws.com"),
				}
			},
			`{"Version":"2012-10-17","Statement":[` +
				`{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"},` +
				`{"Effect":"Allow","Action":"states:CreateStateMachine","Resource":"` + sm + `"}]}`,
		},
		{
			"another principal's grant on * subsumes nothing", 1,
			func(t *testing.T) []access.Entry {
				t.Helper()
				return []access.Entry{
					use(worker, access.Direct{}, act("sqs", "GetQueueUrl")),
					use(app, access.Direct{}, act("sqs", "GetQueueUrl"), arn(t, queueARN)),
				}
			},
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:GetQueueUrl","Resource":"` + queueARN + `"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var r access.Recorder
			entries := tc.entries(t)
			record(t, &r, entries...)
			if got := userPolicy(t, &r); got != tc.want {
				t.Errorf("Policy =\n%s\nwant\n%s", got, tc.want)
			}
			// Recording is unchanged: every use is still listed.
			if got := len(r.Usage(access.Filter{Principal: app})); got != tc.uses {
				t.Errorf("Usage lists %d uses of app, want %d: the record keeps the uses the policy leaves out", got, tc.uses)
			}
		})
	}
}

// passRole is a CreateStateMachine by app that passes a role on scope res
// to service ("" for none; iam-enforcement-surfaces.md 12.6).
func passRole(t *testing.T, sm string, res access.Scope, service string) access.Entry {
	t.Helper()
	return access.Entry{
		Principal: app, Mode: access.Direct{}, Service: "states",
		Actions: []registry.IAMAction{act("states", "CreateStateMachine"), act("iam", "PassRole")},
		Pairs: []access.Pair{
			{Action: act("states", "CreateStateMachine"), Resource: access.OnResource{ARN: arn(t, sm)}},
			{Action: act("iam", "PassRole"), Resource: res, PassedToService: service},
		},
		Outcome: access.Succeeded{}, At: t0,
	}
}
