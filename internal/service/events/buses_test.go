package events_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// busPrefix is the ARN of the caller's buses without the name
// (eventbridge-buses.md 3.1: arn:aws:events:<region>:<account>:event-bus/<name>).
const busPrefix = "arn:aws:events:us-east-1:000000000000:event-bus/"

// createBus creates a custom bus with no options and returns its ARN.
func (h *harness) createBus(name string) string {
	h.Helper()
	out := must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: name})
	if out.EventBusArn == nil {
		h.Fatalf("CreateEventBus %s answered no EventBusArn", name)
	}
	return *out.EventBusArn
}

// describeBus describes the bus name, nil for the default bus.
func (h *harness) describeBus(name *string) *eventbridgeapi.DescribeEventBusOutput {
	h.Helper()
	return must(h, eventbridgeapi.OpDescribeEventBus, &eventbridgeapi.DescribeEventBusInput{Name: name})
}

// listBuses lists every bus whose name starts with prefix, following
// NextToken.
func (h *harness) listBuses(prefix *string) []eventbridgeapi.EventBus {
	h.Helper()
	var out []eventbridgeapi.EventBus
	in := eventbridgeapi.ListEventBusesInput{NamePrefix: prefix}
	for range 200 {
		page := must(h, eventbridgeapi.OpListEventBuses, &in)
		out = append(out, page.EventBuses...)
		if page.NextToken == nil {
			return out
		}
		in.NextToken = page.NextToken
	}
	h.Fatal("ListEventBuses did not stop paging")
	return nil
}

func busNames(buses []eventbridgeapi.EventBus) []string {
	out := make([]string, len(buses))
	for i, b := range buses {
		out[i] = deref(b.Name)
	}
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// switchValue is the value of the EventBridge fidelity switch name. A test
// of a behavior no AWS evidence fixes reads what understudy chose from the
// switch rather than repeating it (ARCHITECTURE.md principle 2).
func (h *harness) switchValue(name string) string {
	h.Helper()
	for _, e := range h.svc.Switches() {
		if e.Name == name {
			return e.Value
		}
	}
	h.Fatalf("Switches() lists no %s; switches = %+v", name, h.svc.Switches())
	return ""
}

// refused reports whether err is a client fault: a 400 the caller caused
// (eventbridge-buses.md 2.3: every EventBridge error but InternalException
// is a client error, HTTP 400).
func refused(err *awserr.Error) bool {
	return err != nil && err.ID.Status == 400 && err.ID.Fault == awserr.Sender
}

// The bus operations are bound with the coverage marks and notes of
// eventbridge-buses.md 10.1, verbatim; partner sources, archives, replays,
// connections, API destinations and endpoints stay unbound and answer 501
// (eventbridge-buses.md 10.1, 10.7.23). The calls that take a bus by ARN
// and answer NotImplemented for another account's or region's bus are
// Partial: AWS accepts such a bus there ("Use these parameters to reference
// cross-account or cross-Region event buses through the APIs",
// eb-event-bus-perms.html, eventbridge-buses.md 4.4; 10.1 as amended
// 2026-10-26).
func TestBusOperationsCoverage(t *testing.T) {
	run(t, func(h *harness) {
		const foreign = "another account's or region's bus answers NotImplemented"
		// A bus policy is evaluated as the other stored resource policies
		// are (iam-enforcement-surfaces.md 12.7).
		const evaluated = "evaluated for the calls and deliveries that name the bus under iam.enforce=advise, which journals each denial, and enforced under iam.enforce=enforce"
		want := map[string]registry.BindCoverage{
			"CreateEventBus":        registry.Partial{Note: "custom buses; EventSourceName (partner buses) answers NotImplemented; KmsKeyIdentifier, DeadLetterConfig and LogConfig are stored and echoed, never applied: events are not encrypted, dead-lettered or logged"},
			"UpdateEventBus":        registry.StoredOnly{Note: "Description, KmsKeyIdentifier, DeadLetterConfig and LogConfig are stored and echoed; events are not encrypted, dead-lettered or logged"},
			"DescribeEventBus":      registry.Partial{Note: foreign},
			"ListEventBuses":        registry.Full{},
			"DeleteEventBus":        registry.Full{},
			"PutPermission":         registry.Partial{Note: "statements are stored in the bus Policy that DescribeEventBus returns, " + evaluated},
			"RemovePermission":      registry.Partial{Note: "statements are removed from the bus Policy, which is " + evaluated},
			"PutRule":               registry.Partial{Note: "default and custom buses of the caller's account and region; ScheduleExpression only on the default bus, as AWS; RoleArn is stored, not used; a rule on another account's or region's bus answers NotImplemented"},
			"PutTargets":            registry.Partial{Note: "Lambda function, SQS queue and SNS topic targets in the rule's region, and event bus targets in any served account and region; RoleArn, RetryPolicy and DeadLetterConfig are stored, and the retry policy and dead-letter queue apply to deliveries; RoleArn is not assumed, so under iam.enforce=enforce every function, queue and topic delivery is decided for events.amazonaws.com against the target's policy, and one it refuses is not retried and goes to the dead-letter queue with ERROR_CODE NO_PERMISSIONS (events.delivery.denied-code), else is dropped; no topic policy is added (events.put-targets.sns-policy); a target with HttpParameters, KinesisParameters, EcsParameters, BatchParameters, RunCommandParameters, RedshiftDataParameters, SageMakerPipelineParameters or AppSyncParameters, or an event bus target of the rule's account with Input, InputPath or InputTransformer, fails its entry with NotImplemented; a rule on another account's or region's bus answers NotImplemented"},
			"PutEvents":             registry.Partial{Note: "default and custom buses in the caller's region, its own or another account's by ARN; EndpointId and TraceHeader are accepted and not used"},
			"TagResource":           registry.Full{},
			"UntagResource":         registry.Full{},
			"ListTagsForResource":   registry.Full{},
			"DescribeRule":          registry.Partial{Note: foreign},
			"DeleteRule":            registry.Partial{Note: foreign},
			"EnableRule":            registry.Partial{Note: foreign},
			"DisableRule":           registry.Partial{Note: foreign},
			"ListRules":             registry.Partial{Note: foreign},
			"ListRuleNamesByTarget": registry.Partial{Note: foreign},
			"ListTargetsByRule":     registry.Partial{Note: foreign},
			"RemoveTargets":         registry.Partial{Note: foreign},
			// The internal operation AWS services' events arrive by
			// (cloudwatch-alarms.md 12.4).
			"PutServiceEventInternal": registry.Full{},
			"TestEventPattern":        registry.Full{},
		}
		for _, op := range eventbridgeapi.Service().Operations {
			status := h.Kernel.Bindings().Status(op)
			cov, listed := want[op.ID.Name]
			if !listed {
				if _, unbound := status.(kernel.Unbound); !unbound {
					t.Errorf("%s is %#v, want unbound (eventbridge-buses.md 10.1)", op.ID.Name, status)
				}
				continue
			}
			if bound, ok := status.(kernel.Bound); !ok || bound.Coverage != cov {
				t.Errorf("%s is %#v, want bound as %#v", op.ID.Name, status, cov)
			}
		}
		for name, err := range map[string]*awserr.Error{
			"ListArchives":        errOf(call(h, eventbridgeapi.OpListArchives, &eventbridgeapi.ListArchivesInput{})),
			"ListReplays":         errOf(call(h, eventbridgeapi.OpListReplays, &eventbridgeapi.ListReplaysInput{})),
			"ListConnections":     errOf(call(h, eventbridgeapi.OpListConnections, &eventbridgeapi.ListConnectionsInput{})),
			"ListApiDestinations": errOf(call(h, eventbridgeapi.OpListApiDestinations, &eventbridgeapi.ListApiDestinationsInput{})),
			"ListEndpoints":       errOf(call(h, eventbridgeapi.OpListEndpoints, &eventbridgeapi.ListEndpointsInput{})),
			"ListEventSources":    errOf(call(h, eventbridgeapi.OpListEventSources, &eventbridgeapi.ListEventSourcesInput{})),
		} {
			if err == nil || err.ID.Status != 501 {
				t.Errorf("%s = %v, want 501", name, err)
			}
		}
	})
}

func errOf[Out any](_ *Out, err *awserr.Error) *awserr.Error { return err }

// Every behavior of the bus operations chosen without AWS-recorded evidence
// is a switch, with the default, label and settling record of
// eventbridge-buses.md 10.5, a ref into that note and no parity scenario
// that settles it yet (section 12 records each).
func TestBusSwitchesNameEveryUnrecordedChoice(t *testing.T) {
	run(t, func(h *harness) {
		type sw struct {
			value  string
			label  evidence.Label
			record string
			ops    []string
		}
		want := map[string]sw{
			"events.delete-bus.with-rules":                {"rejected", evidence.AWSDoc, "EBB-01", []string{"eventbridge.DeleteEventBus"}},
			"events.update-bus.omitted-members":           {"kept", evidence.Inferred, "EBB-04", []string{"eventbridge.UpdateEventBus"}},
			"events.bus.kms-key-check":                    {"none", evidence.Inferred, "EBB-05", []string{"eventbridge.CreateEventBus", "eventbridge.UpdateEventBus"}},
			"events.bus-policy.remove-last-statement":     {"policy-removed", evidence.Inferred, "EBB-06", []string{"eventbridge.RemovePermission"}},
			"events.bus-policy.remove-all-without-policy": {"ResourceNotFoundException", evidence.Inferred, "EBB-06", []string{"eventbridge.RemovePermission"}},
			"events.list-buses.order":                     {"by-name", evidence.Inferred, "EBB-07", []string{"eventbridge.ListEventBuses"}},
			"events.default-bus.creation-time":            {"first-use", evidence.Inferred, "EBB-07", []string{"eventbridge.DescribeEventBus", "eventbridge.ListEventBuses"}},
			"events.bus-target.same-account-event-id":     {"kept", evidence.Inferred, "EBB-08", []string{"eventbridge.PutEvents"}},
			"events.bus-target.cross-account-role":        {"required", evidence.AWSDoc, "EBB-09", []string{"eventbridge.PutTargets"}},
		}
		got := map[string]fidelity.Entry{}
		for _, e := range h.svc.Switches() {
			got[e.Name] = e
		}
		for name, w := range want {
			e, ok := got[name]
			if !ok {
				t.Errorf("Switches() lists no %s", name)
				continue
			}
			if e.Value != w.value || e.Label != w.label || e.Record != w.record || !slices.Equal(e.Ops, w.ops) {
				t.Errorf("%s = %+v, want value %q, label %s, record %s, ops %v", name, e, w.value, w.label, w.record, w.ops)
			}
			if !strings.HasPrefix(e.Ref, "docs/research/eventbridge-buses.md#") {
				t.Errorf("%s Ref = %q, want an anchor in docs/research/eventbridge-buses.md", name, e.Ref)
			}
			if _, ok := e.Settles.(fidelity.NoScenario); !ok {
				t.Errorf("%s Settles = %#v, want NoScenario until %s is recorded", name, e.Settles, w.record)
			}
		}
	})
}

// CreateEventBus answers the bus ARN, in the caller's account and region,
// and echoes Description only when one was sent
// (eventbridge-buses.md 3.1, third-party-recorded:
// localstack@8b9a79f tests/aws/services/events/test_events.snapshot.json:L361-L368
// against L559-L565). A fresh bus describes as {Arn, CreationTime,
// LastModifiedTime, Name} plus Description, with no Policy, KMS key,
// dead-letter or log member (eventbridge-buses.md 3.2, third-party-recorded
// L384-L394, L580-L589), and lists with the same members.
func TestCreateEventBusAnswers(t *testing.T) {
	run(t, func(h *harness) {
		created := h.Clock.Now().Wire()
		out := must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: "described", Description: new("test bus")})
		if deref(out.EventBusArn) != busPrefix+"described" || deref(out.Description) != "test bus" ||
			out.KmsKeyIdentifier != nil || out.DeadLetterConfig != nil || out.LogConfig != nil {
			t.Errorf("CreateEventBus with a description = %+v", out)
		}
		plain := must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: "plain"})
		if deref(plain.EventBusArn) != busPrefix+"plain" || plain.Description != nil {
			t.Errorf("CreateEventBus without a description = %+v, want the ARN alone", plain)
		}

		for name, description := range map[string]*string{"described": new("test bus"), "plain": nil} {
			d := h.describeBus(new(name))
			if deref(d.Name) != name || deref(d.Arn) != busPrefix+name || deref(d.Description) != deref(description) ||
				(description == nil) != (d.Description == nil) {
				t.Errorf("DescribeEventBus %s = %+v", name, d)
			}
			if d.CreationTime == nil || !d.CreationTime.Equal(created) || d.LastModifiedTime == nil || !d.LastModifiedTime.Equal(created) {
				t.Errorf("DescribeEventBus %s times = %v, %v; want both the create instant %v", name, d.CreationTime, d.LastModifiedTime, created)
			}
			if d.Policy != nil || d.KmsKeyIdentifier != nil || d.DeadLetterConfig != nil || d.LogConfig != nil || d.ManagedBy != nil {
				t.Errorf("DescribeEventBus %s = %+v, want no Policy, KmsKeyIdentifier, DeadLetterConfig, LogConfig or ManagedBy", name, d)
			}
		}
		listed := h.listBuses(new("described"))
		if len(listed) != 1 {
			t.Fatalf("ListEventBuses described = %+v, want one bus", listed)
		}
		if b := listed[0]; deref(b.Name) != "described" || deref(b.Arn) != busPrefix+"described" || deref(b.Description) != "test bus" ||
			b.Policy != nil || b.CreationTime == nil || b.LastModifiedTime == nil || b.ManagedBy != nil {
			t.Errorf("listed bus = %+v", b)
		}
	})
}

// DescribeEventBus echoes a bus's KmsKeyIdentifier, DeadLetterConfig and
// LogConfig exactly as sent, explicit NONE and OFF included, which
// Terraform's second plan relies on (eventbridge-buses.md 8.1, 10.6.15,
// TF-ACC bus_test.go L114, L126, L335-L339). Under
// events.bus.kms-key-check, a key is stored without a check.
func TestCreateEventBusStoresSettingsAsSent(t *testing.T) {
	run(t, func(h *harness) {
		const key = "arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab"
		const dlq = "arn:aws:sqs:us-east-1:000000000000:bus-dlq"
		switch v := h.switchValue("events.bus.kms-key-check"); v {
		case "none":
		default:
			h.Fatalf("no expectation for events.bus.kms-key-check = %q", v)
		}
		must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{
			Name: "settings", KmsKeyIdentifier: new(key), DeadLetterConfig: &eventbridgeapi.DeadLetterConfig{Arn: new(dlq)},
			LogConfig: &eventbridgeapi.LogConfig{IncludeDetail: new(eventbridgeapi.IncludeDetailNone), Level: new(eventbridgeapi.LevelOff)},
		})
		d := h.describeBus(new("settings"))
		if deref(d.KmsKeyIdentifier) != key || d.DeadLetterConfig == nil || deref(d.DeadLetterConfig.Arn) != dlq ||
			d.LogConfig == nil || d.LogConfig.IncludeDetail == nil || *d.LogConfig.IncludeDetail != eventbridgeapi.IncludeDetailNone ||
			d.LogConfig.Level == nil || *d.LogConfig.Level != eventbridgeapi.LevelOff {
			t.Errorf("DescribeEventBus = %+v, want the key, dead-letter queue and NONE/OFF log config as sent", d)
		}
	})
}

// CreateEventBus refuses a name the bus already has
// (ResourceAlreadyExistsException "Event bus <name> already exists.",
// eventbridge-buses.md 3.1, third-party-recorded
// localstack@8b9a79f tests/aws/services/events/test_events.snapshot.json:L742-L755),
// the name default and a name with a "/" ("You can't use the name default
// for a custom event bus", "Custom event bus names can't contain the /
// character", aws-doc API_CreateEventBus, eventbridge-buses.md 2.2; their
// texts are unrecorded, EBB-02), more than 50 tags (API_TagResource), and a
// partner bus, which understudy does not implement and does not fake as a
// custom bus (eventbridge-buses.md 10.7.20). A refused call creates nothing.
func TestCreateEventBusRefusals(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("orders")
		_, err := call(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: "orders"})
		wantError(t, err, "ResourceAlreadyExistsException", "Event bus orders already exists.")

		for _, name := range []string{"default", "team/orders"} {
			if _, err := call(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: name}); !refused(err) {
				t.Errorf("CreateEventBus %q = %v, want a client error", name, err)
			}
		}

		tags := make([]eventbridgeapi.Tag, 51)
		for i := range tags {
			tags[i] = eventbridgeapi.Tag{Key: "k" + strconv.Itoa(i), Value: "v"}
		}
		_, err = call(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: "tagged", Tags: tags})
		wantError(t, err, "ValidationException", "Tags are not valid. Reason: a resource can have at most 50 tags, and this request would leave 51.")

		const partner = "aws.partner/example.com/123/orders"
		_, err = call(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: partner, EventSourceName: new(partner)})
		wantError(t, err, "NotImplemented", "understudy does not implement partner event buses, so CreateEventBus with EventSourceName created nothing. "+
			"Create a custom bus without EventSourceName, or run 'understudy coverage --service eventbridge' for what is supported.")

		if got := busNames(h.listBuses(nil)); !slices.Equal(got, []string{"default", "orders"}) && !slices.Equal(got, []string{"orders", "default"}) {
			t.Errorf("buses after the refused calls = %v, want default and orders only", got)
		}
	})
}

// Bus names are per account and region: one name exists in several
// regions at once (eventbridge-buses.md 3.1, third-party-recorded
// test_events.snapshot.json:L605-L676), and in another account.
func TestBusNamesArePerScope(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("orders")
		h.as("000000000000", "eu-west-1", func() {
			if got := h.createBus("orders"); got != "arn:aws:events:eu-west-1:000000000000:event-bus/orders" {
				t.Errorf("eu-west-1 bus ARN = %s", got)
			}
		})
		h.as("111111111111", "us-east-1", func() {
			if got := h.createBus("orders"); got != "arn:aws:events:us-east-1:111111111111:event-bus/orders" {
				t.Errorf("other account's bus ARN = %s", got)
			}
		})
	})
}

// An account holds 100 Classic event buses per region (aws-doc
// https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-quota.html,
// eventbridge-buses.md 7): CreateEventBus past that answers
// LimitExceededException, an error the model declares for it
// (eventbridge-buses.md 2.3), and creates nothing. Whether the default bus
// counts is unrecorded (EBB-02), so the refusal may come at the 100th or
// the 101st custom bus, never earlier.
func TestEventBusQuota(t *testing.T) {
	run(t, func(h *harness) {
		for i := range 99 {
			h.createBus("bus-" + strconv.Itoa(i))
		}
		var err *awserr.Error
		created := 99
		for i := 99; i < 101 && err == nil; i++ {
			if _, err = call(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: "bus-" + strconv.Itoa(i)}); err == nil {
				created++
			}
		}
		if err == nil || err.ID.JSONType != "LimitExceededException" {
			t.Fatalf("CreateEventBus past the quota = %v, want LimitExceededException", err)
		}
		if n := len(h.listBuses(new("bus-"))); n != created {
			t.Errorf("%d custom buses listed, want the %d created", n, created)
		}
	})
}

// A bus that does not exist is not found by name or ARN
// (ResourceNotFoundException "Event bus <name> does not exist.",
// testdata/parity/events/R07/04-describe-event-bus-missing.json:L31,
// aws-recorded). Another account's or region's bus, named by ARN, is not
// one understudy manages for the caller (eventbridge-buses.md 10.1).
func TestDescribeEventBusMissingAndForeign(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range []string{"custom", busPrefix + "custom"} {
			_, err := call(h, eventbridgeapi.OpDescribeEventBus, &eventbridgeapi.DescribeEventBusInput{Name: new(name)})
			wantError(t, err, "ResourceNotFoundException", "Event bus custom does not exist.")
		}
		for _, foreign := range []struct{ bus, account, region string }{
			{"arn:aws:events:us-east-1:111111111111:event-bus/shared", "111111111111", "us-east-1"},
			{"arn:aws:events:eu-west-1:000000000000:event-bus/default", "000000000000", "eu-west-1"},
		} {
			_, err := call(h, eventbridgeapi.OpDescribeEventBus, &eventbridgeapi.DescribeEventBusInput{Name: new(foreign.bus)})
			wantError(t, err, "NotImplemented", "understudy manages rules, targets and buses only on the caller's own event buses, and "+
				foreign.bus+" belongs to account "+foreign.account+" in "+foreign.region+
				". Call with that account's credentials in that region. Run 'understudy coverage --service eventbridge' for what is supported.")
		}
	})
}

// ListEventBuses lists every bus, the default bus included (aws-doc
// API_ListEventBuses, eventbridge-buses.md 3.3), in the order
// events.list-buses.order names; NamePrefix is a plain prefix
// (third-party-recorded test_events.snapshot.json:L797-L828); an empty
// result is a present, empty list (L401-L407); a deleted bus is not listed
// (L590-L602).
func TestListEventBuses(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range []string{"zeta", "alpha", "mid-b", "mid-a"} {
			h.createBus(name)
		}
		got := busNames(h.listBuses(nil))
		switch v := h.switchValue("events.list-buses.order"); v {
		case "by-name":
			if want := []string{"alpha", "default", "mid-a", "mid-b", "zeta"}; !slices.Equal(got, want) {
				t.Errorf("ListEventBuses = %v, want %v by name", got, want)
			}
		default:
			t.Fatalf("no expectation for events.list-buses.order = %q", v)
		}
		if got := busNames(h.listBuses(new("mid-"))); !slices.Contains(got, "mid-a") || !slices.Contains(got, "mid-b") || len(got) != 2 {
			t.Errorf("ListEventBuses mid- = %v, want mid-a and mid-b", got)
		}
		if got := busNames(h.listBuses(new("def"))); !slices.Equal(got, []string{"default"}) {
			t.Errorf("ListEventBuses def = %v, want the default bus", got)
		}
		must(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "zeta"})
		if slices.Contains(busNames(h.listBuses(nil)), "zeta") {
			t.Error("a deleted bus is still listed")
		}
		empty := must(h, eventbridgeapi.OpListEventBuses, &eventbridgeapi.ListEventBusesInput{NamePrefix: new("none")})
		if empty.EventBuses == nil || len(empty.EventBuses) != 0 || empty.NextToken != nil {
			t.Errorf("ListEventBuses none = %#v, want an empty list", empty)
		}
	})
}

// Six buses created in name order page as AWS paged them: Limit=3 answers
// the first three and a NextToken, and the next call with Limit=5 the last
// three and no NextToken (third-party-recorded
// localstack@8b9a79f tests/aws/services/events/test_events.snapshot.json:L830-L886).
// A token understudy did not issue is refused.
func TestListEventBusesPages(t *testing.T) {
	run(t, func(h *harness) {
		for i := range 6 {
			h.createBus("p-" + strconv.Itoa(i))
		}
		first := must(h, eventbridgeapi.OpListEventBuses, &eventbridgeapi.ListEventBusesInput{NamePrefix: new("p-"), Limit: new(int32(3))})
		if got := busNames(first.EventBuses); !slices.Equal(got, []string{"p-0", "p-1", "p-2"}) || first.NextToken == nil {
			t.Fatalf("first page = %v, next %v", got, first.NextToken)
		}
		second := must(h, eventbridgeapi.OpListEventBuses, &eventbridgeapi.ListEventBusesInput{NamePrefix: new("p-"), Limit: new(int32(5)), NextToken: first.NextToken})
		if got := busNames(second.EventBuses); !slices.Equal(got, []string{"p-3", "p-4", "p-5"}) || second.NextToken != nil {
			t.Errorf("second page = %v, next %v", got, second.NextToken)
		}
		if _, err := call(h, eventbridgeapi.OpListEventBuses, &eventbridgeapi.ListEventBusesInput{NextToken: new("bogus")}); !refused(err) {
			t.Errorf("ListEventBuses with a foreign token = %v, want a client error", err)
		}
	})
}

// UpdateEventBus replaces each member it sends and keeps or clears the
// ones it omits as events.update-bus.omitted-members says
// (eventbridge-buses.md 3.4). Description "" unsets the description, the
// only way Terraform has to unset it, and reads back absent or empty
// (TF bus.go L218-L223, TF-ACC bus_test.go L64-L71). A LogConfig of NONE
// and OFF is echoed (TF-ACC bus_test.go L335-L339). A missing bus is not
// found (ResourceNotFoundException, declared for UpdateEventBus,
// eventbridge-buses.md 2.3; its text is unrecorded, EBB-04).
func TestUpdateEventBus(t *testing.T) {
	run(t, func(h *harness) {
		const key = "arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab"
		const dlq = "arn:aws:sqs:us-east-1:000000000000:bus-dlq"
		created := h.Clock.Now().Wire()
		must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{
			Name: "u", Description: new("d1"), KmsKeyIdentifier: new(key), DeadLetterConfig: &eventbridgeapi.DeadLetterConfig{Arn: new(dlq)},
			LogConfig: &eventbridgeapi.LogConfig{IncludeDetail: new(eventbridgeapi.IncludeDetailFull), Level: new(eventbridgeapi.LevelInfo)},
		})
		h.advance(time.Minute)
		out := must(h, eventbridgeapi.OpUpdateEventBus, &eventbridgeapi.UpdateEventBusInput{Name: new("u"), Description: new("d2")})
		if deref(out.Arn) != busPrefix+"u" || deref(out.Name) != "u" || deref(out.Description) != "d2" {
			t.Errorf("UpdateEventBus = %+v", out)
		}
		d := h.describeBus(new("u"))
		if deref(d.Description) != "d2" {
			t.Errorf("Description after the update = %v, want d2", d.Description)
		}
		switch v := h.switchValue("events.update-bus.omitted-members"); v {
		case "kept":
			if deref(d.KmsKeyIdentifier) != key || d.DeadLetterConfig == nil || deref(d.DeadLetterConfig.Arn) != dlq ||
				d.LogConfig == nil || d.LogConfig.Level == nil || *d.LogConfig.Level != eventbridgeapi.LevelInfo {
				t.Errorf("DescribeEventBus after an update omitting them = %+v, want the key, dead-letter queue and log config kept", d)
			}
		case "cleared":
			if d.KmsKeyIdentifier != nil || d.DeadLetterConfig != nil || d.LogConfig != nil {
				t.Errorf("DescribeEventBus after an update omitting them = %+v, want them cleared", d)
			}
		default:
			t.Fatalf("no expectation for events.update-bus.omitted-members = %q", v)
		}
		if d.CreationTime == nil || !d.CreationTime.Equal(created) {
			t.Errorf("CreationTime after the update = %v, want the create instant %v", d.CreationTime, created)
		}

		must(h, eventbridgeapi.OpUpdateEventBus, &eventbridgeapi.UpdateEventBusInput{Name: new("u"), Description: new("")})
		if d := h.describeBus(new("u")); deref(d.Description) != "" {
			t.Errorf("Description after an update to \"\" = %q, want it unset", deref(d.Description))
		}
		must(h, eventbridgeapi.OpUpdateEventBus, &eventbridgeapi.UpdateEventBusInput{
			Name: new("u"), LogConfig: &eventbridgeapi.LogConfig{IncludeDetail: new(eventbridgeapi.IncludeDetailNone), Level: new(eventbridgeapi.LevelOff)},
		})
		if d := h.describeBus(new("u")); d.LogConfig == nil || d.LogConfig.IncludeDetail == nil || *d.LogConfig.IncludeDetail != eventbridgeapi.IncludeDetailNone ||
			d.LogConfig.Level == nil || *d.LogConfig.Level != eventbridgeapi.LevelOff {
			t.Errorf("LogConfig after an update to NONE/OFF = %+v", d.LogConfig)
		}

		_, err := call(h, eventbridgeapi.OpUpdateEventBus, &eventbridgeapi.UpdateEventBusInput{Name: new("missing"), Description: new("x")})
		if err == nil || err.ID.JSONType != "ResourceNotFoundException" {
			t.Errorf("UpdateEventBus of a missing bus = %v, want ResourceNotFoundException", err)
		}
	})
}

// DeleteEventBus answers 200 with an empty body for a bus that does not
// exist (eventbridge-buses.md 3.5, third-party-recorded
// localstack@8b9a79f tests/aws/services/events/test_events.py:L576) and
// refuses the default bus (ValidationException "Cannot delete event bus
// default.", third-party-recorded test_events.snapshot.json:L782-L795). A
// deleted bus takes its tags with it: its ARN is not found
// (test_events_tags.snapshot.json:L233-L246) and a bus recreated with its
// name has no tags (L469-L479).
func TestDeleteEventBus(t *testing.T) {
	run(t, func(h *harness) {
		out, err := call(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "never-created"})
		if err != nil || out == nil || *out != (eventbridgeapi.DeleteEventBusOutput{}) {
			t.Errorf("DeleteEventBus of a missing bus = %+v, %v; want an empty output", out, err)
		}
		_, err = call(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "default"})
		wantError(t, err, "ValidationException", "Cannot delete event bus default.")

		arn := h.createBus("gone")
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: arn, Tags: []eventbridgeapi.Tag{{Key: "k", Value: "v"}}})
		must(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "gone"})
		_, err = call(h, eventbridgeapi.OpDescribeEventBus, &eventbridgeapi.DescribeEventBusInput{Name: new("gone")})
		wantError(t, err, "ResourceNotFoundException", "Event bus gone does not exist.")
		_, err = call(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: arn})
		wantError(t, err, "ResourceNotFoundException", "Event bus gone does not exist.")
		h.createBus("gone")
		if tags := must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: arn}).Tags; len(tags) != 0 {
			t.Errorf("tags of the recreated bus = %v, want none", tags)
		}
	})
}

// "All rules associated with this event bus need to be deleted" before
// DeleteEventBus (aws-doc API_DeleteEventBus, eventbridge-buses.md 3.5);
// what AWS does with a bus that still has rules is unrecorded (EBB-01), so
// understudy follows events.delete-bus.with-rules.
func TestDeleteEventBusWithRules(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("busy")
		must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "r", EventBusName: new("busy"), EventPattern: new(`{"source": ["s"]}`)})
		_, err := call(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "busy"})
		switch v := h.switchValue("events.delete-bus.with-rules"); v {
		case "rejected":
			if !refused(err) {
				t.Errorf("DeleteEventBus of a bus with a rule = %v, want a client error", err)
			}
			h.describeBus(new("busy"))
			must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: new("busy")})
		case "cascade":
			if err != nil {
				t.Fatalf("DeleteEventBus of a bus with a rule = %v, want it deleted with its rules", err)
			}
			h.createBus("busy")
			_, err := call(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: new("busy")})
			wantError(t, err, "ResourceNotFoundException", "Rule r does not exist on EventBus busy.")
		default:
			t.Fatalf("no expectation for events.delete-bus.with-rules = %q", v)
		}
	})
}

// The default bus reports CreationTime and LastModifiedTime like a custom
// bus (eventbridge-buses.md 3.6, third-party-recorded
// test_events.snapshot.json:L1029-L1033). Which instant is unrecorded
// (EBB-07); understudy reports the one events.default-bus.creation-time
// names, in DescribeEventBus and ListEventBuses alike.
func TestDefaultBusTimes(t *testing.T) {
	run(t, func(h *harness) {
		v := h.switchValue("events.default-bus.creation-time")
		if v != "first-use" {
			t.Fatalf("no expectation for events.default-bus.creation-time = %q", v)
		}
		times := func(when string) (created, modified time.Time) {
			t.Helper()
			d := h.describeBus(nil)
			if d.CreationTime == nil || d.LastModifiedTime == nil {
				t.Fatalf("%s: DescribeEventBus default = %+v, want CreationTime and LastModifiedTime", when, d)
			}
			listed := h.listBuses(new("default"))
			if len(listed) != 1 || listed[0].CreationTime == nil || !listed[0].CreationTime.Equal(*d.CreationTime) ||
				listed[0].LastModifiedTime == nil || !listed[0].LastModifiedTime.Equal(*d.LastModifiedTime) {
				t.Errorf("%s: ListEventBuses default = %+v, want the times DescribeEventBus gives", when, listed)
			}
			return *d.CreationTime, *d.LastModifiedTime
		}
		h.advance(10 * time.Second)
		// A scope nothing has written to reports the call's own instant.
		if c, m := times("before any write"); !c.Equal(h.Clock.Now().Wire()) || !m.Equal(c) {
			t.Errorf("before any write: times %v, %v; want both the call instant %v", c, m, h.Clock.Now().Wire())
		}
		h.advance(10 * time.Second)
		firstUse := h.Clock.Now().Wire()
		h.patternRule("r", `{"source": ["s"]}`)
		h.advance(10 * time.Second)
		if c, m := times("after the first write"); !c.Equal(firstUse) || !m.Equal(firstUse) {
			t.Errorf("after the first write: times %v, %v; want both the first write's instant %v", c, m, firstUse)
		}
		// Tagging the default bus, which stores it, keeps its creation
		// time.
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: busPrefix + "default", Tags: []eventbridgeapi.Tag{{Key: "k", Value: "v"}}})
		h.advance(10 * time.Second)
		if c, _ := times("after tagging"); !c.Equal(firstUse) {
			t.Errorf("after tagging: CreationTime %v, want the first write's instant %v", c, firstUse)
		}
	})
}

// Tags work on every bus, the default one included, and on rules of a
// custom bus; untagging an absent key succeeds; writes answer empty and
// reads list the tags (eventbridge-buses.md 5.5, third-party-recorded
// localstack@8b9a79f tests/aws/services/events/test_events_tags.snapshot.json:L248-L297
// for the default bus, L401-L451 for a custom-bus rule; tags passed to
// CreateEventBus are listed back, L205-L231). A missing bus, by ARN, is not
// found by Tag, List and Untag alike (L127-L160), and a missing rule of a
// custom bus names that bus (L190-L203).
func TestTagsOnBusesAndTheirRules(t *testing.T) {
	run(t, func(h *harness) {
		tagsOf := func(arn string) []eventbridgeapi.Tag {
			t.Helper()
			return must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: arn}).Tags
		}
		two := []eventbridgeapi.Tag{{Key: "tag1", Value: "value1"}, {Key: "tag2", Value: "value2"}}
		one := []eventbridgeapi.Tag{{Key: "tag1", Value: "value1"}}

		def := busPrefix + "default"
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: def, Tags: two})
		if got := tagsOf(def); !slices.Equal(got, two) {
			t.Errorf("default bus tags = %v, want %v", got, two)
		}
		must(h, eventbridgeapi.OpUntagResource, &eventbridgeapi.UntagResourceInput{ResourceARN: def, TagKeys: []string{"tag2"}})
		if got := tagsOf(def); !slices.Equal(got, one) {
			t.Errorf("default bus tags after untagging tag2 = %v, want %v", got, one)
		}
		must(h, eventbridgeapi.OpUntagResource, &eventbridgeapi.UntagResourceInput{ResourceARN: def, TagKeys: []string{"never-set"}})

		created := must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: "tagged", Tags: two})
		if got := tagsOf(deref(created.EventBusArn)); !slices.Equal(got, two) {
			t.Errorf("tags passed to CreateEventBus = %v, want %v", got, two)
		}

		rule := deref(must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "r", EventBusName: new("tagged"), EventPattern: new(`{"source": ["s"]}`)}).RuleArn)
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: rule, Tags: two})
		must(h, eventbridgeapi.OpUntagResource, &eventbridgeapi.UntagResourceInput{ResourceARN: rule, TagKeys: []string{"tag2", "never-set"}})
		if got := tagsOf(rule); !slices.Equal(got, one) {
			t.Errorf("custom-bus rule tags = %v, want %v", got, one)
		}

		missing := busPrefix + "missing"
		_, err := call(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: missing, Tags: one})
		wantError(t, err, "ResourceNotFoundException", "Event bus missing does not exist.")
		_, err = call(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: missing})
		wantError(t, err, "ResourceNotFoundException", "Event bus missing does not exist.")
		_, err = call(h, eventbridgeapi.OpUntagResource, &eventbridgeapi.UntagResourceInput{ResourceARN: missing, TagKeys: []string{"tag1"}})
		wantError(t, err, "ResourceNotFoundException", "Event bus missing does not exist.")

		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r", EventBusName: new("tagged")})
		_, err = call(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: rule})
		wantError(t, err, "ResourceNotFoundException", "Rule r does not exist on EventBus tagged.")
	})
}

// ruleARNOn is the ARN of rule name on the caller's bus: rule/<name> on the
// default bus, rule/<bus>/<name> on a custom one (eventbridge-buses.md 5.2,
// third-party-recorded test_events.snapshot.json:L1549).
func ruleARNOn(bus, name string) string {
	if bus == "default" {
		return ruleARN + name
	}
	return ruleARN + bus + "/" + name
}

// mustAccount parses s, failing the test on a malformed account id.
func mustAccount(h *harness, s string) aws.AccountID {
	h.Helper()
	a, err := aws.ParseAccountID(s)
	if err != nil {
		h.Fatal(err)
	}
	return a
}

// mustARN parses s, failing the test on a malformed ARN.
func (h *harness) mustARN(s string) aws.ARN {
	h.Helper()
	a, err := aws.ParseARN(s)
	if err != nil {
		h.Fatal(err)
	}
	return a
}
