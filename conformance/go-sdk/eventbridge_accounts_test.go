package gosdk_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// Research: eventbridge-buses.md 6.1 and 6.3 for events sent from one
// account to a custom bus of another, 4.1 for the statement that lets the
// sender in.

// senderAccount sends events to buses of the suite's own account. A 12-digit
// access key id signs as the root of that account, with the secret "test"
// (docs/configuration.md).
const senderAccount = "222222222222"

// senderConfig is awsCfg signing as senderAccount.
func senderConfig() aws.Config {
	cfg := awsCfg.Copy()
	cfg.Credentials = credentials.NewStaticCredentialsProvider(senderAccount, "test", "")
	return cfg
}

// receivingBus creates a custom bus in the suite's account whose policy lets
// senderAccount put events to it, since on AWS the receiving bus must allow
// the sender (eventbridge-buses.md 6.3), and a rule on it that sends what
// matches pattern to a new queue. It returns the bus ARN and the queue URL.
func receivingBus(t *testing.T, pattern string) (busARN, queueURL string) {
	t.Helper()
	bus := name(t, "receiver")
	out := createBus(t, &eventbridge.CreateEventBusInput{Name: aws.String(bus)})
	_, err := eventbridge.NewFromConfig(awsCfg).PutPermission(t.Context(), &eventbridge.PutPermissionInput{
		EventBusName: aws.String(bus), StatementId: aws.String("sender"),
		Action: aws.String("events:PutEvents"), Principal: aws.String(senderAccount),
	})
	ok(t, err, "PutPermission for the sender account")
	queueURL = createQueue(t, name(t, "eb-receiver"), nil)
	rule := putBusRule(t, bus, name(t, "in"), pattern, types.Target{Id: aws.String("q"), Arn: aws.String(queueArn(t, queueURL))})
	// The queue must let the rule deliver (iam-enforcement-surfaces.md 8.2,
	// 8.4), as for every rule target in these suites.
	allowQueueDelivery(t, queueURL, "events.amazonaws.com", rule)
	return aws.ToString(out.EventBusArn), queueURL
}

// senderTargetRole creates, as senderAccount's root, a role EventBridge may
// assume whose inline policy allows events:PutEvents on busARN, as a
// cross-account event bus target needs (iam-enforcement-surfaces.md 8.4;
// aws-doc eb-event-bus-perms.html, eventbridge-buses.md 6.3), and deletes
// it when the test ends. It returns the role's ARN. The IAM loop renders
// policies only for roles of the suite's account, so this role carries its
// own.
func senderTargetRole(t *testing.T, busARN string) string {
	t.Helper()
	client := iam.NewFromConfig(senderConfig())
	roleName := name(t, "events-role")
	out, err := client.CreateRole(t.Context(), &iam.CreateRoleInput{RoleName: aws.String(roleName), AssumeRolePolicyDocument: aws.String(eventsTrustPolicy)})
	ok(t, err, "CreateRole "+roleName+" in the sender account")
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"events:PutEvents","Resource":"` + busARN + `"}]}`
	_, err = client.PutRolePolicy(t.Context(), &iam.PutRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("put-events"), PolicyDocument: aws.String(policy)})
	ok(t, err, "PutRolePolicy on "+roleName)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("put-events")})
		_, _ = client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
	})
	return aws.ToString(out.Role.Arn)
}

// receivedEvent waits for the one event the queue should get and decodes it.
func receivedEvent(t *testing.T, queueURL string) ebEvent {
	t.Helper()
	msgs := receive(t, queueURL, 1, 20*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("the receiving bus's queue received %d messages, want 1", len(msgs))
	}
	var ev ebEvent
	ok(t, json.Unmarshal([]byte(aws.ToString(msgs[0].Body)), &ev), "decoding the received event")
	return ev
}

func TestEventBridgeAcrossAccounts(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	if acct := account(t); acct == senderAccount {
		t.Fatalf("the suite runs as account %s, the sender's; the cases need two accounts", acct)
	}
	sender := eventbridge.NewFromConfig(senderConfig())

	t.Run("PutEvents to another account's bus", func(t *testing.T) {
		source := "conformance." + name(t, "app")
		busARN, queue := receivingBus(t, `{"source":["`+source+`"]}`)
		out, err := sender.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []types.PutEventsRequestEntry{
			{Source: aws.String(source), DetailType: aws.String("Order Placed"), Detail: aws.String(`{"n":1}`), EventBusName: aws.String(busARN)},
		}})
		ok(t, err, "PutEvents from the sender account")
		if out.FailedEntryCount != 0 || len(out.Entries) != 1 {
			t.Fatalf("PutEvents = FailedEntryCount %d, Entries %+v", out.FailedEntryCount, out.Entries)
		}
		// The event carries the sender's account, not the bus owner's
		// (eventbridge-buses.md 6.1, third-party-recorded).
		ev := receivedEvent(t, queue)
		if ev.ID != aws.ToString(out.Entries[0].EventId) || ev.Account != senderAccount || ev.Region != region() || ev.Source != source || ev.DetailType != "Order Placed" {
			t.Errorf("received event = %+v; want id %s, account %s, region %s", ev, aws.ToString(out.Entries[0].EventId), senderAccount, region())
		}
		wantJSONEqual(t, "received detail", string(ev.Detail), `{"n":1}`)
	})

	t.Run("event bus target in another account", func(t *testing.T) {
		source := "conformance." + name(t, "app")
		pattern := `{"source":["` + source + `"]}`
		busARN, queue := receivingBus(t, pattern)

		bus, rule := name(t, "sender"), name(t, "fwd")
		_, err := sender.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{Name: aws.String(bus)})
		ok(t, err, "CreateEventBus in the sender account")
		t.Cleanup(func() {
			_, _ = sender.DeleteEventBus(context.Background(), &eventbridge.DeleteEventBusInput{Name: aws.String(bus)})
		})
		_, err = sender.PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(rule), EventBusName: aws.String(bus), EventPattern: aws.String(pattern)})
		ok(t, err, "PutRule in the sender account")
		t.Cleanup(func() {
			ctx := context.Background()
			_, _ = sender.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: aws.String(rule), EventBusName: aws.String(bus), Ids: []string{"bus"}})
			_, _ = sender.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(rule), EventBusName: aws.String(bus)})
		})
		// A cross-account event bus target takes a role (aws-doc
		// eb-cross-account.html, eventbridge-buses.md 6.3), which the
		// sender creates.
		role := senderTargetRole(t, busARN)
		put, err := sender.PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: aws.String(rule), EventBusName: aws.String(bus), Targets: []types.Target{
			{Id: aws.String("bus"), Arn: aws.String(busARN), RoleArn: aws.String(role)},
		}})
		ok(t, err, "PutTargets with the other account's bus")
		if put.FailedEntryCount != 0 {
			t.Fatalf("PutTargets FailedEntries = %+v", put.FailedEntries)
		}

		out, err := sender.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []types.PutEventsRequestEntry{
			{Source: aws.String(source), DetailType: aws.String("Order Placed"), Detail: aws.String(`{"n":2}`), EventBusName: aws.String(bus)},
		}})
		ok(t, err, "PutEvents to the sender's bus")
		if out.FailedEntryCount != 0 || len(out.Entries) != 1 {
			t.Fatalf("PutEvents = FailedEntryCount %d, Entries %+v", out.FailedEntryCount, out.Entries)
		}
		// Within one region the receiving account gets the event with the
		// sender's id, account and region (eventbridge-buses.md 6.3,
		// third-party-recorded).
		ev := receivedEvent(t, queue)
		if ev.ID != aws.ToString(out.Entries[0].EventId) || ev.Account != senderAccount || ev.Region != region() || ev.Source != source || ev.DetailType != "Order Placed" {
			t.Errorf("forwarded event = %+v; want id %s, account %s, region %s", ev, aws.ToString(out.Entries[0].EventId), senderAccount, region())
		}
		wantJSONEqual(t, "forwarded detail", string(ev.Detail), `{"n":2}`)
	})
}
