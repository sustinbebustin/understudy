package outbox_test

import (
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Only events-to-events and lambda-to-events deliver to an event bus
// (eventbridge-buses.md 10.4.12, lambda-destinations-layers.md 4.4), only
// lambda-to-s3 to an S3 object (lambda-destinations-layers.md 4.5) and only
// sns-to-http to an HTTP endpoint, so the outbox refuses each new target
// under every other mechanism, naming the mechanism, rather than delivering
// it as something the mechanism does not do (ADR 0015).
// TestEnqueueEventsToEventsChecks covers the bus under events-to-events.
func TestEnqueueRefusesNewTargetsNoMechanismDeliversTo(t *testing.T) {
	delivers := map[string][]delivery.Mechanism{
		"EventBus":     {delivery.EventsToEvents, delivery.LambdaToEvents, delivery.SchedulerToEvents},
		"S3Object":     {delivery.LambdaToS3},
		"HTTPEndpoint": {delivery.SNSToHTTP},
	}
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		hook, err := url.Parse("http://127.0.0.1:8080/hook")
		if err != nil {
			k.Fatal(err)
		}
		targets := map[string]delivery.Target{
			"EventBus":     delivery.EventBus{ARN: arn("events", "event-bus/orders"), Entries: []delivery.EventEntry{{Source: "app", DetailType: "Order", Detail: `{}`}}},
			"S3Object":     delivery.S3Object{Bucket: "audit", Key: "k", Body: []byte("b")},
			"HTTPEndpoint": delivery.HTTPEndpoint{URL: *hook, Body: []byte("b")},
		}
		for name, target := range targets {
			for _, m := range delivery.Mechanisms() {
				if slices.Contains(delivers[name], m) {
					continue
				}
				req := delivery.Request{Source: topic(), Mechanism: m, Target: target, Policy: delivery.None{}}
				_, err := o.Enqueue(req, ref.Cause{}, k.Clock.Now())
				rerr, ok := errors.AsType[*outbox.RequestError](err)
				if !ok || rerr.Field != "Target" {
					k.Errorf("%s under %s: Enqueue = %v, want a *RequestError for Target", name, m, err)
					continue
				}
				if !strings.Contains(rerr.Reason, name) || !strings.Contains(rerr.Reason, m.String()) {
					k.Errorf("%s under %s: reason %q does not name the target kind and the mechanism", name, m, rerr.Reason)
				}
			}
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources = %v, want nothing queued", r)
		}
	})
}
