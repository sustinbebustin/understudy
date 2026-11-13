package cloudwatch_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch"
)

const (
	alertsTopic = "arn:aws:sns:us-east-1:000000000000:alerts"
	fifoTopic   = "arn:aws:sns:us-east-1:000000000000:alerts.fifo"
	onAlarmFn   = "arn:aws:lambda:us-east-1:000000000000:function:on-alarm"
	ec2Stop     = "arn:aws:automate:us-east-1:ec2:stop"
)

// sink is the kernel's DeliverSink in place of the outbox: it keeps every
// delivery Tx.Deliver produces.
type sink struct {
	mu  sync.Mutex
	got []kernel.Delivery
}

func (s *sink) Deliver(d kernel.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, d)
	return nil
}

func (s *sink) all() []kernel.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

// busEvent is one PutServiceEventInternal call the EventBridge stub took.
type busEvent struct {
	in        eventbridgeapi.PutServiceEventInternalInput
	principal identity.Principal
	scope     aws.Scope
}

type bus struct {
	mu  sync.Mutex
	got []busEvent
}

func (b *bus) of(detailType string) []busEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []busEvent
	for i := range b.got {
		if b.got[i].in.DetailType == detailType {
			out = append(out, b.got[i])
		}
	}
	return out
}

// effects are what alarms send out of the service: the deliveries the
// kernel hands to its sink and the calls an EventBridge stub takes.
type effects struct {
	sink *sink
	bus  *bus
}

// runAlarms runs fn against a kernel whose deliveries go to a sink and
// whose EventBridge internal operation is a stub that records its calls.
func runAlarms(t *testing.T, fn func(h *harness, fx *effects)) {
	t.Helper()
	fx := &effects{sink: &sink{}, bus: &bus{}}
	kerneltest.Run(t, func(k *kerneltest.K) {
		k.Stub(eventbridgeapi.OpPutServiceEventInternal, func(_ context.Context, c *kernel.Call, in *eventbridgeapi.PutServiceEventInternalInput) (*eventbridgeapi.PutServiceEventInternalOutput, *awserr.Error) {
			fx.bus.mu.Lock()
			defer fx.bus.mu.Unlock()
			fx.bus.got = append(fx.bus.got, busEvent{in: *in, principal: c.Principal(), scope: c.Scope()})
			return &eventbridgeapi.PutServiceEventInternalOutput{}, nil
		})
		svc := cloudwatch.New(k.Deps(cloudwatchapi.Service()), cloudwatch.Options{})
		fn(&harness{K: k, svc: svc}, fx)
	}, kerneltest.Services(cloudwatchapi.Service(), eventbridgeapi.Service()), kerneltest.Deliver(fx.sink))
}

// settle advances the emulator clock by d, running every evaluation due on
// the way, and waits for the work they start (deliveries, events).
func (h *harness) settle(d time.Duration) {
	h.Helper()
	h.advance(d)
	synctest.Wait()
}

// switchCase reads the service's switch name and returns the expectation
// the test holds for its value: a behavior a switch governs is UNVERIFIED
// or inferred, so the test follows the switch rather than hard-code it.
func switchCase[T any](h *harness, name string, cases map[string]T) T {
	h.Helper()
	for _, e := range h.svc.Switches() {
		if e.Name != name {
			continue
		}
		want, ok := cases[e.Value]
		if !ok {
			h.Fatalf("switch %s = %q, a value this test has no expectation for", name, e.Value)
		}
		return want
	}
	h.Fatalf("the cloudwatch service registers no switch %s (cloudwatch-alarms.md 12.2)", name)
	var unreachable T // Fatalf stops the test goroutine
	return unreachable
}

func followsSwitch(h *harness, name, value string) {
	h.Helper()
	switchCase(h, name, map[string]struct{}{value: {}})
}

// evalAlarm is an evaluable alarm on ns/Load: Sum over 60 seconds, one
// evaluation period, breaching above 10.
func evalAlarm(name string) *cloudwatchapi.PutMetricAlarmInput {
	return &cloudwatchapi.PutMetricAlarmInput{
		AlarmName:          name,
		MetricName:         new("Load"),
		Namespace:          new("ns"),
		Statistic:          new(cloudwatchapi.StatisticSum),
		Period:             new(int32(60)),
		EvaluationPeriods:  new(int32(1)),
		Threshold:          new(10.0),
		ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold),
	}
}

func (h *harness) alarmARN(name string) string {
	return "arn:aws:cloudwatch:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":alarm:" + name
}

func (h *harness) one(name string) cloudwatchapi.MetricAlarm {
	h.Helper()
	out := h.describe(name)
	if len(out.MetricAlarms) != 1 {
		h.Fatalf("DescribeAlarms(%s) found %d alarms", name, len(out.MetricAlarms))
	}
	return out.MetricAlarms[0]
}

// stateChanges are the journaled state changes of resource, as "From->To".
func (h *harness) stateChanges(resource string) []string {
	var out []string
	events := h.Journal.Query(journal.Filter{Kind: journal.KindStateChange, Limit: 1000}).Events
	for i := range events {
		if sc, ok := events[i].Detail.(journal.StateChange); ok && sc.Resource.String() == resource {
			out = append(out, string(sc.From)+"->"+string(sc.To))
		}
	}
	return out
}

func (h *harness) advisories(kind journal.AdvisoryKind) []journal.Advisory {
	var out []journal.Advisory
	events := h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory, Limit: 1000}).Events
	for i := range events {
		if a, ok := events[i].Detail.(journal.Advisory); ok && a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// evaluationTimer is the armed evaluation timer of the alarm arn.
func (h *harness) evaluationTimer(arn string) (time.Time, bool) {
	for _, a := range h.Kernel.Timers().Scheduler().Snapshot() {
		if a.Key == (clock.TimerKey{Kind: cloudwatch.TimerAlarmEvaluation, ID: arn}) {
			return a.Due.Wire(), true
		}
	}
	return time.Time{}, false
}

func (h *harness) history(in *cloudwatchapi.DescribeAlarmHistoryInput) []cloudwatchapi.AlarmHistoryItem {
	h.Helper()
	out := must(h, cloudwatchapi.OpDescribeAlarmHistory, in)
	if out.AlarmHistoryItems == nil {
		h.Fatal("AlarmHistoryItems is nil, want a list")
	}
	return out.AlarmHistoryItems
}

func summaries(items []cloudwatchapi.AlarmHistoryItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = *it.AlarmName + ": " + *it.HistorySummary
	}
	return out
}

const reasonDate = "02/01/06 15:04:05" // dd/MM/yy HH:mm:ss (cwa 4.3)

const jsonTime = "2006-01-02T15:04:05.000-0700" // printed in UTC as +0000 (cwa 4.3)

func stamp(t time.Time) string { return t.UTC().Format(jsonTime) }

// The end-to-end case: an alarm with an SNS action on data put now.
// clock advance runs one evaluation per minute; the first sees the point
// and goes to ALARM, publishing one notification; once the point has left
// the evaluation range (cwa 3.4) the alarm goes to INSUFFICIENT_DATA under
// the default missing treatment (cwa 3.5), with no action since it has no
// InsufficientDataActions. Each transition writes the state, a history item
// and a journaled state change (cwa 3.8, 12.3), and evaluation never
// touches the configuration Terraform reads (cwa 12.6).
func TestClockAdvanceEvaluatesAndNotifiesOnce(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		followsSwitch(h, "cloudwatch.alarm.first-evaluation", "next-boundary")
		followsSwitch(h, "cloudwatch.alarm.evaluation-phase", "minute-boundary")
		rng := switchCase(h, "cloudwatch.alarm.evaluation-range", map[string]int{"n-plus-5-for-60s,n-plus-2-otherwise": 1 + 5})
		followsSwitch(h, "cloudwatch.alarm.reason-format", "count-form-unless-datapoints-to-alarm")

		t0 := h.now()
		if !t0.Equal(t0.Truncate(time.Minute)) {
			h.Fatalf("the bubble starts at %v, not on a minute", t0)
		}
		h.put("Load", cloudwatchapi.StandardUnitCount, 20)
		in := evalAlarm("cpu")
		in.AlarmActions = []string{alertsTopic}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		arn := h.alarmARN("cpu")

		// No synchronous evaluation: a describe right after the create
		// shows the initial reason (cwa 3.1, third-party-recorded).
		created := h.one("cpu")
		if *created.StateValue != cloudwatchapi.StateValueInsufficientData || *created.StateReason != "Unchecked: Initial alarm creation" {
			h.Fatalf("after create: %s %q", *created.StateValue, *created.StateReason)
		}
		if due, ok := h.evaluationTimer(arn); !ok || !due.Equal(t0.Add(time.Minute)) {
			h.Fatalf("evaluation timer = %v %v, want armed at %v", due, ok, t0.Add(time.Minute))
		}

		alarmAt := t0.Add(time.Minute)
		gone := t0.Add(time.Duration(rng+1) * time.Minute)
		h.settle(time.Duration(rng+2) * time.Minute)

		got := h.one("cpu")
		wantReason := "Insufficient Data: 1 datapoint was unknown." // cwa 4.1, singular form CWA-08
		if *got.StateValue != cloudwatchapi.StateValueInsufficientData || *got.StateReason != wantReason {
			h.Errorf("after %d minutes: %s %q, want INSUFFICIENT_DATA %q", rng+2, *got.StateValue, *got.StateReason, wantReason)
		}
		if !got.StateUpdatedTimestamp.Equal(gone) || !got.StateTransitionedTimestamp.Equal(gone) {
			h.Errorf("state timestamps = %v %v, want both %v", *got.StateUpdatedTimestamp, *got.StateTransitionedTimestamp, gone)
		}
		if got.StateReasonData == nil {
			h.Fatal("StateReasonData is nil after an evaluation")
		}
		var data struct {
			Version             string           `json:"version"`
			QueryDate           string           `json:"queryDate"`
			RecentDatapoints    []float64        `json:"recentDatapoints"`
			EvaluatedDatapoints []map[string]any `json:"evaluatedDatapoints"`
		}
		if err := json.Unmarshal([]byte(*got.StateReasonData), &data); err != nil {
			h.Fatalf("StateReasonData is not JSON: %v", err)
		}
		if data.Version != "1.0" || data.QueryDate != stamp(gone) || data.RecentDatapoints == nil || len(data.RecentDatapoints) != 0 ||
			!reflect.DeepEqual(data.EvaluatedDatapoints, []map[string]any{{"timestamp": stamp(gone.Add(-time.Minute))}}) {
			h.Errorf("StateReasonData = %s (cwa 4.2)", *got.StateReasonData)
		}
		// cwa 12.6: the configuration echo is unchanged.
		if !got.AlarmConfigurationUpdatedTimestamp.Equal(t0) || *got.ActionsEnabled != true || *got.Threshold != 10 {
			h.Errorf("evaluation changed the configuration: %+v", got)
		}

		if sc := h.stateChanges(arn); !slices.Equal(sc, []string{"INSUFFICIENT_DATA->ALARM", "ALARM->INSUFFICIENT_DATA"}) {
			h.Errorf("journaled state changes = %v", sc)
		}

		items := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new("cpu"), HistoryItemType: new(cloudwatchapi.HistoryItemTypeStateUpdate)})
		if s := summaries(items); !slices.Equal(s, []string{
			"cpu: Alarm updated from ALARM to INSUFFICIENT_DATA", "cpu: Alarm updated from INSUFFICIENT_DATA to ALARM",
		}) {
			h.Fatalf("history = %v, want the two transitions newest first (cwa 8.1, 8.2)", s)
		}
		breach := "Threshold Crossed: 1 datapoint [20.0 (" + t0.Format(reasonDate) + ")] was greater than the threshold (10.0)." // cwa 4.1
		var hd struct {
			NewState struct {
				StateValue      string `json:"stateValue"`
				StateReason     string `json:"stateReason"`
				StateReasonData struct {
					QueryDate           string           `json:"queryDate"`
					StartDate           string           `json:"startDate"`
					Statistic           string           `json:"statistic"`
					Period              int              `json:"period"`
					RecentDatapoints    []float64        `json:"recentDatapoints"`
					Threshold           float64          `json:"threshold"`
					EvaluatedDatapoints []map[string]any `json:"evaluatedDatapoints"`
				} `json:"stateReasonData"`
			} `json:"newState"`
		}
		if err := json.Unmarshal([]byte(*items[1].HistoryData), &hd); err != nil {
			h.Fatalf("HistoryData is not JSON: %v", err)
		}
		rd := hd.NewState.StateReasonData
		if hd.NewState.StateValue != "ALARM" || hd.NewState.StateReason != breach || rd.QueryDate != stamp(alarmAt) ||
			rd.StartDate != stamp(t0) || rd.Statistic != "Sum" || rd.Period != 60 || !slices.Equal(rd.RecentDatapoints, []float64{20}) || rd.Threshold != 10 ||
			!reflect.DeepEqual(rd.EvaluatedDatapoints, []map[string]any{{"timestamp": stamp(t0), "sampleCount": 1.0, "value": 20.0}}) {
			h.Errorf("ALARM history data = %s\nwant reason %q and the reason data of the point at %v", *items[1].HistoryData, breach, t0)
		}
		if !items[1].Timestamp.Equal(alarmAt) || !items[0].Timestamp.Equal(gone) {
			h.Errorf("history timestamps = %v, %v; want %v, %v", *items[1].Timestamp, *items[0].Timestamp, alarmAt, gone)
		}

		deliveries := fx.sink.all()
		if len(deliveries) != 1 {
			h.Fatalf("deliveries = %d, want one notification", len(deliveries))
		}
		d := deliveries[0].Request
		topic, ok := d.Target.(delivery.SNSTopic)
		if !ok || d.Mechanism != delivery.CloudWatchToSNS || d.Source.String() != arn || topic.ARN.String() != alertsTopic {
			h.Fatalf("delivery = %+v, want CloudWatchToSNS from %s to %s", d, arn, alertsTopic)
		}
		if d.Policy != (delivery.None{}) || d.DLQ != nil {
			h.Errorf("delivery policy %v, DLQ %v; want no retry schedule and no DLQ (cwa 12.4)", d.Policy, d.DLQ)
		}
		if topic.Subject != `ALARM: "cpu" in US East (N. Virginia)` || topic.GroupID != "" || topic.DedupID != "" {
			h.Errorf("subject %q, group %q, dedup %q (cwa 5.2, 5.3)", topic.Subject, topic.GroupID, topic.DedupID)
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(topic.Message), &msg); err != nil {
			h.Fatalf("notification is not JSON: %v", err)
		}
		for k, want := range map[string]any{
			"AlarmName": "cpu", "AlarmArn": arn, "NewStateValue": "ALARM", "OldStateValue": "INSUFFICIENT_DATA",
			"NewStateReason": breach, "StateChangeTime": stamp(alarmAt), "Region": "US East (N. Virginia)", "AWSAccountId": "000000000000",
		} {
			if msg[k] != want {
				h.Errorf("notification %s = %v, want %v (cwa 5.1)", k, msg[k], want)
			}
		}
	})
}

// An evaluation that keeps the state writes nothing: no state change, no
// history, no action (cloudwatch.alarm.unchanged-state, cwa 3.8). ignore
// with no data retains (cwa 3.5).
func TestEvaluationThatRetainsWritesNothing(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		followsSwitch(h, "cloudwatch.alarm.unchanged-state", "no-update")
		in := evalAlarm("quiet")
		in.TreatMissingData = new("ignore")
		in.InsufficientDataActions = []string{alertsTopic}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		before := h.one("quiet")
		h.settle(5 * time.Minute)
		if after := h.one("quiet"); !reflect.DeepEqual(after, before) {
			h.Errorf("after five evaluations that retain:\n%+v\nbefore:\n%+v", after, before)
		}
		if sc := h.stateChanges(h.alarmARN("quiet")); len(sc) != 0 {
			h.Errorf("state changes = %v", sc)
		}
		if items := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{HistoryItemType: new(cloudwatchapi.HistoryItemTypeStateUpdate)}); len(items) != 0 {
			h.Errorf("history = %v", summaries(items))
		}
		if d := fx.sink.all(); len(d) != 0 || len(fx.bus.of("CloudWatch Alarm State Change")) != 0 {
			h.Errorf("deliveries %d, state change events %d; want none", len(d), len(fx.bus.of("CloudWatch Alarm State Change")))
		}
		if due, ok := h.evaluationTimer(h.alarmARN("quiet")); !ok || !due.Equal(h.now().Add(time.Minute)) {
			h.Errorf("next evaluation = %v %v, want %v", due, ok, h.now().Add(time.Minute))
		}
	})
}

// A transition's actions run in list order when ActionsEnabled: a Lambda
// action is an asynchronous invoke of the unqualified function with its
// qualifier; a FIFO topic gets the group and deduplication ids of
// cloudwatch.alarm.sns.fifo-group; an EC2 action is journaled as not run
// and never claimed (cwa 7.2, 12.4, 12.7).
func TestTransitionRunsItsActions(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		followsSwitch(h, "cloudwatch.alarm.sns.fifo-group", "sha256-hex-of-alarm-arn,uuid-dedup-id")
		h.put("Load", cloudwatchapi.StandardUnitCount, 20)
		in := evalAlarm("cpu")
		in.AlarmActions = []string{onAlarmFn + ":prod", ec2Stop, fifoTopic}
		in.OKActions = []string{alertsTopic}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		arn := h.alarmARN("cpu")
		h.settle(time.Minute)

		deliveries := fx.sink.all()
		if len(deliveries) != 2 {
			h.Fatalf("deliveries = %+v, want the Lambda and the FIFO topic", deliveries)
		}
		fn, ok := deliveries[0].Request.Target.(delivery.LambdaFunction)
		if !ok || deliveries[0].Request.Mechanism != delivery.CloudWatchToLambda || fn.ARN.String() != onAlarmFn || fn.Qualifier != "prod" ||
			fn.Invocation != delivery.InvocationEvent || deliveries[0].Request.Policy != (delivery.None{}) {
			h.Fatalf("first delivery = %+v, want an asynchronous CloudWatchToLambda invoke of %s:prod", deliveries[0].Request, onAlarmFn)
		}
		var event struct {
			Source    string `json:"source"`
			AlarmArn  string `json:"alarmArn"`
			Region    string `json:"region"`
			AlarmData struct {
				State struct {
					Value string `json:"value"`
				} `json:"state"`
			} `json:"alarmData"`
		}
		if err := json.Unmarshal(fn.Event, &event); err != nil || event.Source != "aws.cloudwatch" || event.AlarmArn != arn ||
			event.Region != "us-east-1" || event.AlarmData.State.Value != "ALARM" {
			h.Errorf("Lambda event = %s (%v), want the cwa 6.1 event of the ALARM change", fn.Event, err)
		}
		topic, ok := deliveries[1].Request.Target.(delivery.SNSTopic)
		sum := func(s string) string {
			b := sha256.Sum256([]byte(s))
			return hex.EncodeToString(b[:])
		}
		uuidV4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
		if !ok || topic.ARN.String() != fifoTopic || topic.GroupID != sum(arn) || !uuidV4.MatchString(topic.DedupID) {
			h.Errorf("second delivery = %+v, want %s with the FIFO ids", deliveries[1].Request, fifoTopic)
		}
		notRun := h.advisories(journal.AdvisoryAlarmActionNotRun)
		if len(notRun) != 1 || notRun[0].Resource.String() != arn ||
			!strings.HasPrefix(notRun[0].Text, "ec2 action "+ec2Stop+" is not run: understudy does not emulate ") {
			h.Errorf("not-run advisories = %+v", notRun)
		}
	})
}

// ActionsEnabled false keeps the actions from running while the state
// still changes, and EnableAlarmActions runs nothing: actions belong to
// transitions (cwa 7.1). The state change event is sent either way
// (cloudwatch.alarm.events.actions-disabled, CWA-14).
func TestDisabledActionsDoNotRun(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		sent := switchCase(h, "cloudwatch.alarm.events.actions-disabled", map[string]int{"events-sent": 1})
		h.put("Load", cloudwatchapi.StandardUnitCount, 20)
		in := evalAlarm("cpu")
		in.ActionsEnabled = new(false)
		in.AlarmActions = []string{alertsTopic}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		h.settle(time.Minute)
		if got := h.one("cpu"); *got.StateValue != cloudwatchapi.StateValueAlarm {
			h.Fatalf("state = %s, want ALARM", *got.StateValue)
		}
		must(h, cloudwatchapi.OpEnableAlarmActions, &cloudwatchapi.EnableAlarmActionsInput{AlarmNames: []string{"cpu"}})
		synctest.Wait()
		if d := fx.sink.all(); len(d) != 0 {
			h.Errorf("deliveries = %+v, want none", d)
		}
		if got := len(fx.bus.of("CloudWatch Alarm State Change")); got != sent {
			h.Errorf("state change events = %d, want %d", got, sent)
		}
	})
}

// Alarm kinds understudy cannot evaluate are stored, journaled with an
// Advisory naming why, and never evaluated: no timer, and the state
// PutMetricAlarm gave them stays (cwa 12.1, 12.7). An update
// that makes an alarm evaluable arms its timer; one that makes it not
// evaluable disarms it.
func TestNotEvaluableAlarmIsStoredAndNeverEvaluated(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		h.put("Load", cloudwatchapi.StandardUnitCount, 20)
		in := evalAlarm("p90")
		in.Statistic, in.ExtendedStatistic = nil, new("p90")
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		arn := h.alarmARN("p90")
		adv := h.advisories(journal.AdvisoryAlarmNotEvaluated)
		if len(adv) != 1 || adv[0].Resource.String() != arn || adv[0].Text != "alarm p90 is stored and never evaluated: ExtendedStatistic" {
			h.Errorf("advisories = %+v", adv)
		}
		if due, ok := h.evaluationTimer(arn); ok {
			h.Errorf("a not evaluable alarm has an evaluation timer at %v", due)
		}
		h.settle(3 * time.Minute)
		if got := h.one("p90"); *got.StateValue != cloudwatchapi.StateValueInsufficientData || *got.StateReason != "Unchecked: Initial alarm creation" {
			h.Errorf("state = %s %q, want the initial state kept", *got.StateValue, *got.StateReason)
		}

		must(h, cloudwatchapi.OpPutMetricAlarm, evalAlarm("p90"))
		if due, ok := h.evaluationTimer(arn); !ok || !due.Equal(h.now().Add(time.Minute)) {
			h.Errorf("after an update to an evaluable alarm the timer = %v %v, want %v", due, ok, h.now().Add(time.Minute))
		}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		if due, ok := h.evaluationTimer(arn); ok {
			h.Errorf("after an update to a not evaluable alarm the timer is still armed at %v", due)
		}
		if adv := h.advisories(journal.AdvisoryAlarmNotEvaluated); len(adv) != 2 {
			h.Errorf("advisories = %d, want one per not evaluable PutMetricAlarm", len(adv))
		}
		if d := fx.sink.all(); len(d) != 0 {
			h.Errorf("deliveries = %+v, want none", d)
		}
	})
}

// SetAlarmState to a different value is a transition: state, history,
// journal, actions and the state change event, with the caller's reason
// and both timestamps now (cwa 2.3, 3.8). To the same value it changes
// nothing: AWS answered 200 and kept the first reason, no reason data, both
// timestamps and one StateUpdate item (cloudwatch.alarm.set-same-state;
// CWA-17, recorded 2026-11-03: testdata/parity/cloudwatch/CWA-17).
func TestSetAlarmStateTransitions(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		followsSwitch(h, "cloudwatch.alarm.set-same-state", "no-change")
		in := evalAlarm("cpu")
		in.TreatMissingData = new("ignore")
		in.AlarmActions = []string{alertsTopic}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		arn := h.alarmARN("cpu")
		h.advance(30 * time.Second)
		set := h.now()
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "cpu", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "testing alarm"})
		synctest.Wait()
		got := h.one("cpu")
		if *got.StateValue != cloudwatchapi.StateValueAlarm || *got.StateReason != "testing alarm" || got.StateReasonData != nil ||
			!got.StateUpdatedTimestamp.Equal(set) || !got.StateTransitionedTimestamp.Equal(set) {
			h.Errorf("after SetAlarmState: %s %q %v at %v %v", *got.StateValue, *got.StateReason, got.StateReasonData, *got.StateUpdatedTimestamp, *got.StateTransitionedTimestamp)
		}
		if sc := h.stateChanges(arn); !slices.Equal(sc, []string{"INSUFFICIENT_DATA->ALARM"}) {
			h.Errorf("state changes = %v", sc)
		}
		transitions := &cloudwatchapi.DescribeAlarmHistoryInput{HistoryItemType: new(cloudwatchapi.HistoryItemTypeStateUpdate)}
		items := h.history(transitions)
		// localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6336-L6380
		if len(items) != 1 || *items[0].HistoryData != `{"version":"1.0","oldState":{"stateValue":"INSUFFICIENT_DATA","stateReason":"Unchecked: Initial alarm creation"},"newState":{"stateValue":"ALARM","stateReason":"testing alarm"}}` {
			h.Errorf("history = %v", summaries(items))
		}
		if d := fx.sink.all(); len(d) != 1 {
			h.Errorf("deliveries = %d, want the ALARM action", len(d))
		} else if topic, ok := d[0].Request.Target.(delivery.SNSTopic); !ok || !strings.Contains(topic.Message, `"NewStateReason":"testing alarm"`) {
			// localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L2482-L2519
			h.Errorf("delivery = %+v, want a notification with the caller's reason", d[0].Request)
		}
		if ev := fx.bus.of("CloudWatch Alarm State Change"); len(ev) != 1 {
			h.Errorf("state change events = %d, want one", len(ev))
		}

		h.advance(10 * time.Second)
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "cpu", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "again", StateReasonData: new(`{"k":1}`)})
		synctest.Wait()
		got = h.one("cpu")
		if *got.StateReason != "testing alarm" || got.StateReasonData != nil ||
			!got.StateUpdatedTimestamp.Equal(set) || !got.StateTransitionedTimestamp.Equal(set) {
			h.Errorf("after SetAlarmState to the same value: %q %v at %v %v", *got.StateReason, got.StateReasonData, *got.StateUpdatedTimestamp, *got.StateTransitionedTimestamp)
		}
		if n := len(h.history(transitions)); n != 1 || len(fx.sink.all()) != 1 || len(fx.bus.of("CloudWatch Alarm State Change")) != 1 {
			h.Errorf("SetAlarmState to the same value wrote history, ran an action or sent an event")
		}
	})
}

// The set state lasts "only until the next alarm comparison occurs"
// (cwa 2.3, aws-doc): the next evaluation re-derives it, unless ignore with
// no data retains it, as AWS recorded (localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L5990-L6078).
func TestNextEvaluationRederivesASetState(t *testing.T) {
	runAlarms(t, func(h *harness, _ *effects) {
		must(h, cloudwatchapi.OpPutMetricAlarm, evalAlarm("missing"))
		ignore := evalAlarm("ignore")
		ignore.TreatMissingData = new("ignore")
		must(h, cloudwatchapi.OpPutMetricAlarm, ignore)
		for _, name := range []string{"missing", "ignore"} {
			must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: name, StateValue: cloudwatchapi.StateValueAlarm, StateReason: "testing alarm"})
		}
		h.settle(time.Minute)
		if got := h.one("missing"); *got.StateValue != cloudwatchapi.StateValueInsufficientData {
			h.Errorf("missing treatment after the next evaluation = %s, want INSUFFICIENT_DATA", *got.StateValue)
		}
		h.settle(5 * time.Minute)
		if got := h.one("ignore"); *got.StateValue != cloudwatchapi.StateValueAlarm || *got.StateReason != "testing alarm" {
			h.Errorf("ignore treatment = %s %q, want the set state kept", *got.StateValue, *got.StateReason)
		}
	})
}

// A snapshot keeps alarms and their history, and Rearm after a load arms
// each evaluable alarm's next evaluation, so evaluation resumes.
func TestSnapshotLoadRearmsEvaluation(t *testing.T) {
	runAlarms(t, func(h *harness, _ *effects) {
		h.put("Load", cloudwatchapi.StandardUnitCount, 20)
		in := evalAlarm("cpu")
		in.TreatMissingData = new("ignore")
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "cpu", StateValue: cloudwatchapi.StateValueOk, StateReason: "set"})
		arn := h.alarmARN("cpu")
		saved := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{})
		if got, want := summaries(saved), []string{"cpu: Alarm updated from INSUFFICIENT_DATA to OK", `cpu: Alarm "cpu" created`}; !slices.Equal(got, want) {
			h.Fatalf("history = %v, want %v", got, want)
		}

		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if items := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{}); len(items) != 0 {
			h.Errorf("history after Reset = %v", summaries(items))
		}
		h.Kernel.Timers().Scheduler().Clear()
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		// Both item types survive, each keeping its HistoryItemType.
		if items := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{}); !reflect.DeepEqual(items, saved) {
			h.Errorf("history after Import = %+v\nwant %+v", items, saved)
		}
		if due, ok := h.evaluationTimer(arn); ok {
			h.Fatalf("Import armed a timer at %v; Rearm arms them", due)
		}
		h.svc.Rearm(h.Clock.Now())
		if due, ok := h.evaluationTimer(arn); !ok || !due.Equal(h.now().Add(time.Minute)) {
			h.Fatalf("after Rearm the timer = %v %v, want %v", due, ok, h.now().Add(time.Minute))
		}
		h.settle(time.Minute)
		if got := h.one("cpu"); *got.StateValue != cloudwatchapi.StateValueAlarm {
			h.Errorf("after Rearm and an advance: %s, want ALARM from the stored point", *got.StateValue)
		}
	})
}

// DeleteAlarms disarms the evaluation timer and keeps the history: "CloudWatch
// retains the history of an alarm even if you delete the alarm" (cwa 8.1,
// aws-doc). History older than 30 days on the emulator clock is gone
// ("CloudWatch preserves alarm history for 30 days", cwa 8.1, aws-doc).
func TestHistoryOutlivesTheAlarmForThirtyDays(t *testing.T) {
	runAlarms(t, func(h *harness, _ *effects) {
		must(h, cloudwatchapi.OpPutMetricAlarm, evalAlarm("cpu"))
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "cpu", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "set"})
		must(h, cloudwatchapi.OpDeleteAlarms, &cloudwatchapi.DeleteAlarmsInput{AlarmNames: []string{"cpu"}})
		if due, ok := h.evaluationTimer(h.alarmARN("cpu")); ok {
			h.Errorf("a deleted alarm's timer is armed at %v", due)
		}
		kept := []string{`cpu: Alarm "cpu" deleted`, "cpu: Alarm updated from INSUFFICIENT_DATA to ALARM", `cpu: Alarm "cpu" created`}
		if got := summaries(h.history(&cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new("cpu")})); !slices.Equal(got, kept) {
			h.Errorf("history after delete = %v, want %v (cwa 8.3)", got, kept)
		}
		h.settle(29 * day)
		if got := summaries(h.history(&cloudwatchapi.DescribeAlarmHistoryInput{})); !slices.Equal(got, kept) {
			h.Errorf("history after 29 days = %v, want %v", got, kept)
		}
		h.settle(2 * day)
		if items := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{}); len(items) != 0 {
			h.Errorf("history after 31 days = %v, want none", summaries(items))
		}
	})
}

// PutMetricAlarm and DeleteAlarms send the configuration change events,
// and a transition the state change event, to the default bus of the
// alarm's scope as the CloudWatch service principal, with source
// aws.cloudwatch and the alarm ARN as the one resource (cwa 6.2, 12.4).
func TestAlarmEventsGoToTheDefaultBus(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		h.put("Load", cloudwatchapi.StandardUnitCount, 20)
		must(h, cloudwatchapi.OpPutMetricAlarm, evalAlarm("cpu"))
		update := evalAlarm("cpu")
		update.Threshold = new(15.0)
		must(h, cloudwatchapi.OpPutMetricAlarm, update)
		h.settle(time.Minute)
		must(h, cloudwatchapi.OpDeleteAlarms, &cloudwatchapi.DeleteAlarmsInput{AlarmNames: []string{"cpu"}})
		synctest.Wait()
		arn := h.alarmARN("cpu")

		config := fx.bus.of("CloudWatch Alarm Configuration Change")
		ops := make([]string, 0, len(config))
		for _, e := range config {
			var d struct {
				Operation             string          `json:"operation"`
				PreviousConfiguration json.RawMessage `json:"previousConfiguration"`
			}
			if err := json.Unmarshal([]byte(e.in.Detail), &d); err != nil {
				h.Fatalf("configuration change detail is not JSON: %v", err)
			}
			ops = append(ops, d.Operation)
			if (d.Operation == "update") != (d.PreviousConfiguration != nil) {
				h.Errorf("%s detail previousConfiguration = %s, want it on update only", d.Operation, d.PreviousConfiguration)
			}
		}
		if !slices.Equal(ops, []string{"create", "update", "delete"}) {
			h.Errorf("configuration change operations = %v", ops)
		}
		state := fx.bus.of("CloudWatch Alarm State Change")
		if len(state) != 1 {
			h.Fatalf("state change events = %d, want one", len(state))
		}
		for _, e := range append(config, state...) {
			if e.in.Source != "aws.cloudwatch" || !slices.Equal(e.in.Resources, []string{arn}) || e.scope != h.Scope ||
				e.principal != (identity.ServicePrincipal{Name: "cloudwatch.amazonaws.com"}) {
				h.Errorf("event = %+v, want source aws.cloudwatch, resources [%s], scope %s, as cloudwatch.amazonaws.com", e, arn, h.Scope)
			}
		}
		var detail struct {
			AlarmName string `json:"alarmName"`
			State     struct {
				Value string `json:"value"`
			} `json:"state"`
			PreviousState struct {
				Value string `json:"value"`
			} `json:"previousState"`
		}
		if err := json.Unmarshal([]byte(state[0].in.Detail), &detail); err != nil || detail.AlarmName != "cpu" ||
			detail.State.Value != "ALARM" || detail.PreviousState.Value != "INSUFFICIENT_DATA" {
			h.Errorf("state change detail = %s (%v)", state[0].in.Detail, err)
		}
	})
}

// Without EventBridge in the kernel, alarm events are skipped and the
// client sees no error.
func TestAlarmEventsSkippedWithoutEventBridge(t *testing.T) {
	run(t, func(h *harness) {
		if _, err := call(h, cloudwatchapi.OpPutMetricAlarm, evalAlarm("cpu")); err != nil {
			h.Fatalf("PutMetricAlarm without EventBridge = %s %s", err.ID.QueryCode, err.Message)
		}
		if _, err := call(h, cloudwatchapi.OpDeleteAlarms, &cloudwatchapi.DeleteAlarmsInput{AlarmNames: []string{"cpu"}}); err != nil {
			h.Fatalf("DeleteAlarms without EventBridge = %s %s", err.ID.QueryCode, err.Message)
		}
	})
}

// The bindings carry cwa 12.1's statuses and notes verbatim; composite
// alarms, mute rules, anomaly detectors and log alarms stay unbound.
func TestAlarmCoverage(t *testing.T) {
	run(t, func(h *harness) {
		status := func(op *registry.Operation) kernel.Status { return h.Kernel.Bindings().Status(op) }
		for op, want := range map[*registry.Operation]registry.BindCoverage{
			cloudwatchapi.OpPutMetricAlarm.Descriptor():          registry.Partial{Note: "alarms on a Statistic with a sliding window are evaluated on the emulator clock; alarms on an ExtendedStatistic, Metrics, a ThresholdMetricId or EvaluationCriteria, or with a WallClockWindow, are stored and never evaluated; SNS and Lambda actions run, and EC2, Auto Scaling, Systems Manager and investigation actions are journaled and never run; EvaluationState is never reported"},
			cloudwatchapi.OpSetAlarmState.Descriptor():           registry.Partial{Note: "the state is set and its actions run when it changes; the next evaluation re-derives it, except on alarms that are never evaluated (see PutMetricAlarm)"},
			cloudwatchapi.OpEnableAlarmActions.Descriptor():      registry.Full{},
			cloudwatchapi.OpDisableAlarmActions.Descriptor():     registry.Full{},
			cloudwatchapi.OpDescribeAlarmHistory.Descriptor():    registry.Partial{Note: "StateUpdate, ConfigurationUpdate and SNS actions' Action items, kept 30 days on the emulator clock and after the alarm is deleted; Lambda actions write no Action item, since no recording shows one, and AlarmContributorId selects nothing; inferred: EnableAlarmActions writes an update item as DisableAlarmActions does, the item's alarm object carries alarmDescription, unit, datapointsToAlarm, evaluateLowSampleCountPercentile and thresholdMetricId only when set and leaves out Metrics, EvaluationCriteria, EvaluationWindow, EvaluationInterval and WarmUpConfiguration, and an Action item's email text shows a description, dimensions, a unit, an ExtendedStatistic and DatapointsToAlarm in unrecorded forms"},
			cloudwatchapi.OpDescribeAlarmsForMetric.Descriptor(): registry.Full{},
			cloudwatchapi.OpGetMetricStatistics.Descriptor():     registry.Partial{Note: "Statistics only: ExtendedStatistics answers 501; points are kept until reset, not aged out by resolution"},
		} {
			if got := status(op); got != (kernel.Bound{Coverage: want}) {
				h.Errorf("%s = %#v, want %#v", op.ID, got, want)
			}
		}
		for _, op := range []*registry.Operation{
			cloudwatchapi.OpPutCompositeAlarm.Descriptor(), cloudwatchapi.OpPutAlarmMuteRule.Descriptor(),
			cloudwatchapi.OpPutAnomalyDetector.Descriptor(), cloudwatchapi.OpPutLogAlarm.Descriptor(),
		} {
			if got := status(op); got != (kernel.Unbound{}) {
				h.Errorf("%s = %#v, want unbound (cwa 12.1)", op.ID, got)
			}
		}
	})
}

// Service.Switches registers one switch per row of cwa 12.2 with its
// default and label, referenced to the research note, plus
// cloudwatch.alarm.actions.denied.
func TestAlarmSwitches(t *testing.T) {
	run(t, func(h *harness) {
		want := map[string]struct {
			value string
			label evidence.Label
		}{
			"cloudwatch.alarm.evaluation-cadence":              {"minute,10s-for-high-resolution,hourly-past-one-day", evidence.AWSDoc},
			"cloudwatch.alarm.evaluation-phase":                {"minute-boundary", evidence.Inferred},
			"cloudwatch.alarm.first-evaluation":                {"next-boundary", evidence.AWSRecorded},
			"cloudwatch.alarm.window-anchor":                   {"evaluation-minute", evidence.AWSDoc},
			"cloudwatch.alarm.mixed-units":                     {"first-unit-by-name", evidence.Inferred},
			"cloudwatch.alarm.evaluation-range":                {"n-plus-5-for-60s,n-plus-2-otherwise", evidence.Inferred},
			"cloudwatch.alarm.unaged-breach":                   {"alarm-if-aged-in-range-else-retain", evidence.AWSRecorded},
			"cloudwatch.alarm.unchanged-state":                 {"no-update", evidence.AWSRecorded},
			"cloudwatch.alarm.set-same-state":                  {"no-change", evidence.AWSRecorded},
			"cloudwatch.alarm.dynamodb-missing-data":           {"as-configured", evidence.AWSRecorded},
			"cloudwatch.alarm.warm-up-end":                     {"every-period-has-data", evidence.AWSRecorded},
			"cloudwatch.alarm.reason-format":                   {"count-form-unless-datapoints-to-alarm", evidence.AWSRecorded},
			"cloudwatch.alarm.number-format":                   {"java-double", evidence.AWSRecorded},
			"cloudwatch.alarm.sns.unset-fields":                {"description-null,treat-missing-data-empty,unit-null,low-sample-empty,datapoints-to-alarm-omitted,mute-window-false", evidence.AWSRecorded},
			"cloudwatch.alarm.sns.subject":                     {"state-quoted-name-in-region-long-name,100-chars", evidence.AWSRecorded},
			"cloudwatch.alarm.sns.fifo-group":                  {"sha256-hex-of-alarm-arn,uuid-dedup-id", evidence.AWSRecorded},
			"cloudwatch.alarm.events.actions-disabled":         {"events-sent", evidence.AWSRecorded},
			"cloudwatch.alarm.events.metric-id":                {"one-per-configuration", evidence.AWSRecorded},
			"cloudwatch.alarm.history.page-size":               {"50", evidence.AWSRecorded},
			"cloudwatch.get-metric-statistics.datapoint-limit": {"floor-unrounded", evidence.AWSRecorded},
			"cloudwatch.get-metric-statistics.order":           {"timestamp-ascending,then-unit", evidence.Inferred},
			"cloudwatch.alarm.actions.denied":                  {"dropped", evidence.AWSRecorded},
		}
		seen := map[string]int{}
		for _, e := range h.svc.Switches() {
			w, ok := want[e.Name]
			if !ok {
				continue
			}
			seen[e.Name]++
			if e.Value != w.value || e.Label != w.label {
				h.Errorf("%s = %q %s, want %q %s", e.Name, e.Value, e.Label, w.value, w.label)
			}
			if !strings.HasPrefix(e.Ref, "docs/research/cloudwatch-alarms.md#") || len(e.Ops) == 0 || e.Settles == nil {
				h.Errorf("%s: Ref %q, Ops %v, Settles %v; want the research note, its operations and what settles it", e.Name, e.Ref, e.Ops, e.Settles)
			}
		}
		for name := range want {
			if seen[name] != 1 {
				h.Errorf("Switches registers %s %d times, want once", name, seen[name])
			}
		}
	})
}

// The mechanisms of alarm deliveries name the CloudWatchAlarm via and their
// target services.
func TestAlarmMechanisms(t *testing.T) {
	for m, target := range map[delivery.Mechanism]string{delivery.CloudWatchToSNS: "sns", delivery.CloudWatchToLambda: "lambda"} {
		if m.Via() != ref.CloudWatchAlarm || string(m.Target()) != target {
			t.Errorf("%s: Via %s, Target %s; want %s, %s", m, m.Via(), m.Target(), ref.CloudWatchAlarm, target)
		}
	}
}
