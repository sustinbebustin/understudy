package e2e_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// scheduledEvent is the event a scheduled rule delivers
// (secrets-logs-events-alarms.md 4.3).
type scheduledEvent struct {
	Version    string          `json:"version"`
	ID         string          `json:"id"`
	DetailType string          `json:"detail-type"`
	Source     string          `json:"source"`
	Account    string          `json:"account"`
	Time       string          `json:"time"`
	Region     string          `json:"region"`
	Resources  []string        `json:"resources"`
	Detail     json.RawMessage `json:"detail"`
}

// scheduledEvents reads the events the image worker stored for rule.
func scheduledEvents(t *testing.T, rule string) []scheduledEvent {
	t.Helper()
	out, err := env.s3.ListObjectsV2(t.Context(), &s3.ListObjectsV2Input{Bucket: aws.String(bucketName), Prefix: aws.String("scheduled/" + rule + "/")})
	if err != nil {
		t.Fatalf("s3 ListObjectsV2 scheduled/%s/: %v", rule, err)
	}
	var evs []scheduledEvent
	for _, o := range out.Contents {
		b, ok := getObject(t, aws.ToString(o.Key))
		if !ok {
			continue
		}
		var e scheduledEvent
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatalf("%s is not a scheduled event: %v\n%s", aws.ToString(o.Key), err, b)
		}
		evs = append(evs, e)
	}
	slices.SortFunc(evs, func(a, b scheduledEvent) int { return cmp.Compare(a.Time, b.Time) })
	return evs
}

// TestEventBridgeScheduleInvokesTheImageWorker creates a rate(5 minutes)
// rule targeting the image worker and drives it on the emulator clock: a
// rate rule first fires one period after it is created, with one-minute
// precision, then every period, and each delivery is the documented
// Scheduled Event (secrets-logs-events-alarms.md 4.2, 4.3).
func TestEventBridgeScheduleInvokesTheImageWorker(t *testing.T) {
	p := pipe(t)
	rule := "e2e-heartbeat-" + env.runID
	created := emuNow(t)
	put, err := env.events.PutRule(t.Context(), &eventbridge.PutRuleInput{Name: aws.String(rule), ScheduleExpression: aws.String("rate(5 minutes)"), State: ebtypes.RuleStateEnabled})
	if err != nil {
		t.Fatalf("events PutRule: %v", err)
	}
	ruleARN := aws.ToString(put.RuleArn)
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		if _, err := env.events.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: aws.String(rule), Ids: []string{"image-worker"}}); err != nil {
			t.Errorf("events RemoveTargets (cleanup): %v", err)
		}
		if _, err := env.events.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: aws.String(rule)}); err != nil {
			t.Errorf("events DeleteRule (cleanup): %v", err)
		}
	})
	if _, err := env.lambda.AddPermission(t.Context(), &lambda.AddPermissionInput{
		FunctionName: aws.String(imageWorker), StatementId: aws.String("events-" + rule), Action: aws.String("lambda:InvokeFunction"),
		Principal: aws.String("events.amazonaws.com"), SourceArn: aws.String(ruleARN),
	}); err != nil {
		t.Fatalf("lambda AddPermission for the rule: %v", err)
	}
	if out, err := env.events.PutTargets(t.Context(), &eventbridge.PutTargetsInput{
		Rule: aws.String(rule), Targets: []ebtypes.Target{{Id: aws.String("image-worker"), Arn: aws.String(p.workers[imageWorker].functionARN)}},
	}); err != nil || out.FailedEntryCount != 0 {
		t.Fatalf("events PutTargets: %v (failed entries %+v)", err, out)
	}

	advance(t, 3*time.Minute)
	if evs := scheduledEvents(t, rule); len(evs) != 0 {
		t.Fatalf("the rule fired %d times within 3 minutes of creation; a rate(5 minutes) rule first fires one period after it is created: %+v", len(evs), evs)
	}
	advance(t, 3*time.Minute)
	var evs []scheduledEvent
	eventually(t, "one scheduled event 6 minutes after creation", func() (bool, string) {
		evs = scheduledEvents(t, rule)
		return len(evs) == 1, fmt.Sprintf("%d events", len(evs))
	})
	checkScheduledEvent(t, evs[0], ruleARN, created.Add(4*time.Minute), created.Add(6*time.Minute))

	advance(t, 10*time.Minute)
	eventually(t, "three scheduled events 16 minutes after creation", func() (bool, string) {
		evs = scheduledEvents(t, rule)
		return len(evs) == 3, fmt.Sprintf("%d events", len(evs))
	})
	if evs[0].ID == evs[1].ID || evs[1].ID == evs[2].ID {
		t.Errorf("scheduled events share an id: %s, %s, %s", evs[0].ID, evs[1].ID, evs[2].ID)
	}
	checkScheduledEvent(t, evs[2], ruleARN, created.Add(14*time.Minute), created.Add(16*time.Minute))
}

func checkScheduledEvent(t *testing.T, e scheduledEvent, ruleARN string, from, to time.Time) {
	t.Helper()
	if e.Version != "0" || e.DetailType != "Scheduled Event" || e.Source != "aws.events" || e.Account != env.account || e.Region != region {
		t.Errorf("event %+v, want version 0, detail-type Scheduled Event, source aws.events, account %s, region %s", e, env.account, region)
	}
	if !slices.Equal(e.Resources, []string{ruleARN}) || string(e.Detail) != "{}" {
		t.Errorf("resources %v, detail %s; want [%s] and {}", e.Resources, e.Detail, ruleARN)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`).MatchString(e.Time) {
		t.Errorf("time %q, want RFC 3339 UTC with seconds precision", e.Time)
		return
	}
	at, err := time.Parse(time.RFC3339, e.Time)
	if err != nil || at.Before(from.Truncate(time.Second)) || at.After(to) {
		t.Errorf("time %s, want emulator time between %s and %s", e.Time, from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
}
