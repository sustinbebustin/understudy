package cloudwatch_test

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// describe reads alarms as Terraform does: by name, metric alarms only.
func (h *harness) describe(names ...string) *cloudwatchapi.DescribeAlarmsOutput {
	h.Helper()
	return must(h, cloudwatchapi.OpDescribeAlarms, &cloudwatchapi.DescribeAlarmsInput{
		AlarmNames: names, AlarmTypes: []cloudwatchapi.AlarmType{cloudwatchapi.AlarmTypeMetricAlarm},
	})
}

func (h *harness) tags(arn string) []cloudwatchapi.Tag {
	h.Helper()
	return must(h, cloudwatchapi.OpListTagsForResource, &cloudwatchapi.ListTagsForResourceInput{ResourceARN: arn}).Tags
}

// numberedTags is n tags with distinct keys k0, k1 and so on.
func numberedTags(n int) []cloudwatchapi.Tag {
	out := make([]cloudwatchapi.Tag, n)
	for i := range out {
		out[i] = cloudwatchapi.Tag{Key: "k" + strconv.Itoa(i), Value: "v"}
	}
	return out
}

// minimalAlarm is the alarm of AWS's recorded minimal DescribeAlarms.
func minimalAlarm(name string) *cloudwatchapi.PutMetricAlarmInput {
	return &cloudwatchapi.PutMetricAlarmInput{
		AlarmName:          name,
		MetricName:         new("metric1"),
		Namespace:          new("ns"),
		Statistic:          new(cloudwatchapi.StatisticSum),
		Period:             new(int32(60)),
		EvaluationPeriods:  new(int32(1)),
		Threshold:          new(30.0),
		ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold),
	}
}

// A minimal alarm reads back as AWS records it: the action lists and
// Dimensions present and empty, ActionsEnabled true, INSUFFICIENT_DATA with
// the initial reason, and nothing it was not given.
func TestDescribeMinimalAlarmMatchesTheRecording(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricAlarm, minimalAlarm("minimal"))
		now := h.now()
		want := cloudwatchapi.MetricAlarm{
			AlarmName:                          new("minimal"),
			AlarmArn:                           new("arn:aws:cloudwatch:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":alarm:minimal"),
			AlarmConfigurationUpdatedTimestamp: new(now),
			ActionsEnabled:                     new(true),
			OKActions:                          []string{},
			AlarmActions:                       []string{},
			InsufficientDataActions:            []string{},
			StateValue:                         new(cloudwatchapi.StateValueInsufficientData),
			StateReason:                        new("Unchecked: Initial alarm creation"),
			StateUpdatedTimestamp:              new(now),
			StateTransitionedTimestamp:         new(now),
			MetricName:                         new("metric1"),
			Namespace:                          new("ns"),
			Statistic:                          new(cloudwatchapi.StatisticSum),
			Dimensions:                         []cloudwatchapi.Dimension{},
			Period:                             new(int32(60)),
			EvaluationPeriods:                  new(int32(1)),
			Threshold:                          new(30.0),
			ComparisonOperator:                 new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold),
		}
		out := h.describe("minimal")
		if len(out.MetricAlarms) != 1 || !reflect.DeepEqual(out.MetricAlarms[0], want) {
			h.Fatalf("DescribeAlarms = %+v\nwant %+v", out.MetricAlarms, want)
		}
		if out.CompositeAlarms == nil || len(out.CompositeAlarms) != 0 {
			h.Errorf("CompositeAlarms = %v, want an empty list", out.CompositeAlarms)
		}
	})
}

// The aws_cloudwatch_metric_alarm sequence of tf-compute.md 6: create with
// tags, read by name, read tags, update keeping state and tags, change tags,
// read again with no difference, destroy, and destroy again.
func TestTerraformAlarmSequenceReadsBackItsConfiguration(t *testing.T) {
	run(t, func(h *harness) {
		create := minimalAlarm("tf-alarm")
		create.ActionsEnabled = new(true)
		create.AlarmActions = []string{"arn:aws:sns:us-east-1:000000000000:alerts"}
		create.AlarmDescription = new("queue depth")
		create.Dimensions = []cloudwatchapi.Dimension{{Name: "QueueName", Value: "jobs"}}
		create.DatapointsToAlarm = new(int32(1))
		create.TreatMissingData = new("missing")
		create.Tags = []cloudwatchapi.Tag{{Key: "env", Value: "dev"}}
		must(h, cloudwatchapi.OpPutMetricAlarm, create)

		read := func() cloudwatchapi.MetricAlarm {
			h.Helper()
			out := h.describe("tf-alarm")
			if len(out.MetricAlarms) != 1 {
				h.Fatalf("DescribeAlarms found %d alarms, want exactly one", len(out.MetricAlarms))
			}
			return out.MetricAlarms[0]
		}
		first := read()
		checkEcho(h, first, create)
		if got := h.tags(*first.AlarmArn); !slices.Equal(got, []cloudwatchapi.Tag{{Key: "env", Value: "dev"}}) {
			h.Errorf("tags = %v", got)
		}
		if again := read(); !reflect.DeepEqual(again, first) {
			h.Errorf("a second read differs:\n%+v\n%+v", again, first)
		}

		// An update overwrites the configuration, keeps the state and
		// ignores Tags.
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "tf-alarm", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "test"})
		h.advance(time.Minute)
		update := *create
		update.Threshold = new(50.0)
		update.Tags = []cloudwatchapi.Tag{{Key: "ignored", Value: "x"}}
		must(h, cloudwatchapi.OpPutMetricAlarm, &update)
		updated := read()
		checkEcho(h, updated, &update)
		if *updated.StateValue != cloudwatchapi.StateValueAlarm || *updated.StateReason != "test" {
			h.Errorf("state after update = %s %q, want ALARM kept", *updated.StateValue, *updated.StateReason)
		}
		if !updated.AlarmConfigurationUpdatedTimestamp.Equal(h.now()) {
			h.Errorf("AlarmConfigurationUpdatedTimestamp = %v, want %v", *updated.AlarmConfigurationUpdatedTimestamp, h.now())
		}

		arn := *updated.AlarmArn
		must(h, cloudwatchapi.OpTagResource, &cloudwatchapi.TagResourceInput{ResourceARN: arn, Tags: []cloudwatchapi.Tag{{Key: "team", Value: "a"}}})
		must(h, cloudwatchapi.OpUntagResource, &cloudwatchapi.UntagResourceInput{ResourceARN: arn, TagKeys: []string{"env"}})
		if got := h.tags(arn); !slices.Equal(got, []cloudwatchapi.Tag{{Key: "team", Value: "a"}}) {
			h.Errorf("tags after update = %v, want team only", got)
		}

		must(h, cloudwatchapi.OpDeleteAlarms, &cloudwatchapi.DeleteAlarmsInput{AlarmNames: []string{"tf-alarm"}})
		if out := h.describe("tf-alarm"); len(out.MetricAlarms) != 0 || out.MetricAlarms == nil {
			h.Errorf("after delete MetricAlarms = %v, want an empty list", out.MetricAlarms)
		}
		// Terraform's destroy accepts only success here (gap-G1.md 4).
		must(h, cloudwatchapi.OpDeleteAlarms, &cloudwatchapi.DeleteAlarmsInput{AlarmNames: []string{"tf-alarm"}})
		if got := h.tags(arn); got == nil || len(got) != 0 {
			h.Errorf("tags of a deleted alarm = %v, want an empty list", got)
		}
	})
}

// checkEcho compares the fields Terraform reads with what it sent.
func checkEcho(h *harness, got cloudwatchapi.MetricAlarm, sent *cloudwatchapi.PutMetricAlarmInput) {
	h.Helper()
	want := cloudwatchapi.MetricAlarm{
		AlarmName: new(sent.AlarmName), ActionsEnabled: sent.ActionsEnabled, AlarmActions: sent.AlarmActions,
		AlarmDescription: sent.AlarmDescription, OKActions: []string{}, InsufficientDataActions: []string{},
		ComparisonOperator: sent.ComparisonOperator, DatapointsToAlarm: sent.DatapointsToAlarm, Dimensions: sent.Dimensions,
		EvaluationPeriods: sent.EvaluationPeriods, MetricName: sent.MetricName, Namespace: sent.Namespace,
		Period: sent.Period, Statistic: sent.Statistic, Threshold: sent.Threshold, TreatMissingData: sent.TreatMissingData,
	}
	echo := cloudwatchapi.MetricAlarm{
		AlarmName: got.AlarmName, ActionsEnabled: got.ActionsEnabled, AlarmActions: got.AlarmActions,
		AlarmDescription: got.AlarmDescription, OKActions: got.OKActions, InsufficientDataActions: got.InsufficientDataActions,
		ComparisonOperator: got.ComparisonOperator, DatapointsToAlarm: got.DatapointsToAlarm, Dimensions: got.Dimensions,
		EvaluationPeriods: got.EvaluationPeriods, MetricName: got.MetricName, Namespace: got.Namespace,
		Period: got.Period, Statistic: got.Statistic, Threshold: got.Threshold, TreatMissingData: got.TreatMissingData,
	}
	if !reflect.DeepEqual(echo, want) {
		h.Errorf("read back %+v\nsent      %+v", echo, want)
	}
	if got.Metrics != nil || got.ExtendedStatistic != nil || got.Unit != nil || got.EvaluateLowSampleCountPercentile != nil || got.ThresholdMetricId != nil {
		h.Errorf("read back members that were never sent: %+v", got)
	}
}

func TestDescribeAlarmsFiltersAndPages(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range []string{"api-errors", "api-latency", "db-cpu", "queue-depth"} {
			in := minimalAlarm(name)
			if name == "db-cpu" {
				in.OKActions = []string{"arn:aws:sns:us-east-1:000000000000:db-ok"}
			}
			must(h, cloudwatchapi.OpPutMetricAlarm, in)
		}
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "queue-depth", StateValue: cloudwatchapi.StateValueOk, StateReason: "fine"})
		names := func(in *cloudwatchapi.DescribeAlarmsInput) []string {
			h.Helper()
			alarms := must(h, cloudwatchapi.OpDescribeAlarms, in).MetricAlarms
			out := make([]string, len(alarms))
			for i, a := range alarms {
				out[i] = *a.AlarmName
			}
			return out
		}
		for _, tc := range []struct {
			name string
			in   cloudwatchapi.DescribeAlarmsInput
			want []string
		}{
			{"all, by name", cloudwatchapi.DescribeAlarmsInput{}, []string{"api-errors", "api-latency", "db-cpu", "queue-depth"}},
			{"names, missing ones skipped", cloudwatchapi.DescribeAlarmsInput{AlarmNames: []string{"db-cpu", "nope"}}, []string{"db-cpu"}},
			{"prefix", cloudwatchapi.DescribeAlarmsInput{AlarmNamePrefix: new("api-")}, []string{"api-errors", "api-latency"}},
			{"state", cloudwatchapi.DescribeAlarmsInput{StateValue: new(cloudwatchapi.StateValueOk)}, []string{"queue-depth"}},
			{"action prefix", cloudwatchapi.DescribeAlarmsInput{ActionPrefix: new("arn:aws:sns:us-east-1:000000000000:db")}, []string{"db-cpu"}},
			{"composite only", cloudwatchapi.DescribeAlarmsInput{AlarmTypes: []cloudwatchapi.AlarmType{cloudwatchapi.AlarmTypeCompositeAlarm}}, nil},
		} {
			if got := names(&tc.in); !slices.Equal(got, tc.want) {
				h.Errorf("%s = %v, want %v", tc.name, got, tc.want)
			}
		}

		var paged []string
		in := cloudwatchapi.DescribeAlarmsInput{MaxRecords: new(int32(3))}
		for range 4 {
			out := must(h, cloudwatchapi.OpDescribeAlarms, &in)
			for _, a := range out.MetricAlarms {
				paged = append(paged, *a.AlarmName)
			}
			if out.NextToken == nil {
				break
			}
			in.NextToken = out.NextToken
		}
		if !slices.Equal(paged, []string{"api-errors", "api-latency", "db-cpu", "queue-depth"}) {
			h.Errorf("pages = %v", paged)
		}
		// "garbage" is valid unpadded base64, so the token must be refused
		// for not being one this service issued, not only for its alphabet.
		for _, token := range []string{"%%", "garbage"} {
			_, err := call(h, cloudwatchapi.OpDescribeAlarms, &cloudwatchapi.DescribeAlarmsInput{NextToken: new(token)})
			wantError(h.T, err, "InvalidNextToken", "The specified NextToken is invalid.")
		}
		_, err := call(h, cloudwatchapi.OpDescribeAlarms, &cloudwatchapi.DescribeAlarmsInput{AlarmNames: []string{"a"}, AlarmNamePrefix: new("a")})
		wantError(h.T, err, "InvalidParameterCombination", "The parameters AlarmNames and AlarmNamePrefix are mutually exclusive and you have specified both.")
	})
}

// ChildrenOfAlarmName and ParentsOfAlarmName take no other member but
// MaxRecords and NextToken (the DescribeAlarms documentation).
func TestDescribeAlarmsFamilyFiltersStandAlone(t *testing.T) {
	run(t, func(h *harness) {
		for _, family := range []string{"ChildrenOfAlarmName", "ParentsOfAlarmName"} {
			base := func() cloudwatchapi.DescribeAlarmsInput {
				in := cloudwatchapi.DescribeAlarmsInput{MaxRecords: new(int32(10))}
				if family == "ChildrenOfAlarmName" {
					in.ChildrenOfAlarmName = new("c")
				} else {
					in.ParentsOfAlarmName = new("p")
				}
				return in
			}
			in := base()
			must(h, cloudwatchapi.OpDescribeAlarms, &in)
			for _, tc := range []struct {
				member string
				set    func(*cloudwatchapi.DescribeAlarmsInput)
			}{
				{"AlarmNames", func(in *cloudwatchapi.DescribeAlarmsInput) { in.AlarmNames = []string{"x"} }},
				{"AlarmNamePrefix", func(in *cloudwatchapi.DescribeAlarmsInput) { in.AlarmNamePrefix = new("x") }},
				{"AlarmTypes", func(in *cloudwatchapi.DescribeAlarmsInput) {
					in.AlarmTypes = []cloudwatchapi.AlarmType{cloudwatchapi.AlarmTypeCompositeAlarm}
				}},
				{"StateValue", func(in *cloudwatchapi.DescribeAlarmsInput) { in.StateValue = new(cloudwatchapi.StateValueAlarm) }},
				{"ActionPrefix", func(in *cloudwatchapi.DescribeAlarmsInput) { in.ActionPrefix = new("arn:") }},
			} {
				in := base()
				tc.set(&in)
				_, err := call(h, cloudwatchapi.OpDescribeAlarms, &in)
				wantError(h.T, err, "InvalidParameterCombination", "The parameters "+family+" and "+tc.member+" are mutually exclusive and you have specified both.")
			}
		}
		_, err := call(h, cloudwatchapi.OpDescribeAlarms, &cloudwatchapi.DescribeAlarmsInput{ChildrenOfAlarmName: new("c"), ParentsOfAlarmName: new("p")})
		wantError(h.T, err, "InvalidParameterCombination", "The parameters ChildrenOfAlarmName and ParentsOfAlarmName are mutually exclusive and you have specified both.")
	})
}

// SetAlarmState answers the recorded errors: the Coral enum text as
// ValidationError, and ResourceNotFound without a message, 404 in query
// mode (error-matrix.md 5.13).
func TestSetAlarmState(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "a", StateValue: "INVALID", StateReason: "test"})
		wantError(h.T, err, "ValidationError", "1 validation error detected: Value 'INVALID' at 'stateValue' failed to satisfy constraint: Member must satisfy enum value set: [INSUFFICIENT_DATA, ALARM, OK]")
		_, err = call(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "missing", StateValue: cloudwatchapi.StateValueOk, StateReason: "test"})
		wantError(h.T, err, "ResourceNotFound", "")
		if err.ID.QueryModeStatus != 404 {
			h.Errorf("ResourceNotFound query-mode status = %d, want 404", err.ID.QueryModeStatus)
		}

		must(h, cloudwatchapi.OpPutMetricAlarm, minimalAlarm("a"))
		_, err = call(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "a", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "test", StateReasonData: new("not json")})
		wantError(h.T, err, "InvalidFormat", "Data was not syntactically valid JSON")
		if a := h.describe("a").MetricAlarms[0]; *a.StateValue != cloudwatchapi.StateValueInsufficientData || a.StateReasonData != nil {
			h.Errorf("a refused SetAlarmState stored %s %v", *a.StateValue, a.StateReasonData)
		}
		created := h.now()
		h.advance(time.Minute)
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "a", StateValue: cloudwatchapi.StateValueInsufficientData, StateReason: "same value", StateReasonData: new(`{"k":1}`)})
		a := h.describe("a").MetricAlarms[0]
		switch {
		case *a.StateReason != "same value" || a.StateReasonData == nil || *a.StateReasonData != `{"k":1}`:
			h.Errorf("reason = %q %v", *a.StateReason, a.StateReasonData)
		case !a.StateUpdatedTimestamp.Equal(h.now()):
			h.Errorf("StateUpdatedTimestamp = %v, want %v", *a.StateUpdatedTimestamp, h.now())
		case !a.StateTransitionedTimestamp.Equal(created):
			h.Errorf("StateTransitionedTimestamp moved without a change of value: %v", *a.StateTransitionedTimestamp)
		}
		h.advance(time.Minute)
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "a", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "breach"})
		if a := h.describe("a").MetricAlarms[0]; !a.StateTransitionedTimestamp.Equal(h.now()) || *a.StateValue != cloudwatchapi.StateValueAlarm {
			h.Errorf("after a change of value = %s at %v", *a.StateValue, *a.StateTransitionedTimestamp)
		}
	})
}

func TestEnableAndDisableAlarmActions(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricAlarm, minimalAlarm("a"))
		must(h, cloudwatchapi.OpDisableAlarmActions, &cloudwatchapi.DisableAlarmActionsInput{AlarmNames: []string{"a", "missing"}})
		if a := h.describe("a").MetricAlarms[0]; *a.ActionsEnabled {
			h.Error("ActionsEnabled after DisableAlarmActions = true")
		}
		must(h, cloudwatchapi.OpEnableAlarmActions, &cloudwatchapi.EnableAlarmActionsInput{AlarmNames: []string{"a"}})
		if a := h.describe("a").MetricAlarms[0]; !*a.ActionsEnabled {
			h.Error("ActionsEnabled after EnableAlarmActions = false")
		}
	})
}

func TestPutMetricAlarmValidation(t *testing.T) {
	run(t, func(h *harness) {
		for _, tc := range []struct {
			name          string
			change        func(in *cloudwatchapi.PutMetricAlarmInput)
			code, message string
		}{
			{"metric and metrics", func(in *cloudwatchapi.PutMetricAlarmInput) {
				in.Metrics = []cloudwatchapi.MetricDataQuery{{Id: "m1", Expression: new("SUM(METRICS())")}}
			}, "InvalidParameterCombination", "The parameters MetricName and Metrics are mutually exclusive and you have specified both."},
			{
				"statistic both ways", func(in *cloudwatchapi.PutMetricAlarmInput) { in.ExtendedStatistic = new("p99") },
				"InvalidParameterCombination", "The parameters Statistic and ExtendedStatistic are mutually exclusive and you have specified both.",
			},
			{
				"no metric", func(in *cloudwatchapi.PutMetricAlarmInput) { in.MetricName = nil },
				"MissingParameter", "One of the parameters MetricName, Metrics or EvaluationCriteria is required.",
			},
			{"no period", func(in *cloudwatchapi.PutMetricAlarmInput) { in.Period = nil }, "MissingParameter", "The parameter Period is required."},
			{
				"bad period", func(in *cloudwatchapi.PutMetricAlarmInput) { in.Period = new(int32(45)) },
				"InvalidParameterValue", "The parameter Period must be 10, 20, 30 or a multiple of 60.",
			},
			{
				"range over a day", func(in *cloudwatchapi.PutMetricAlarmInput) { in.EvaluationPeriods = new(int32(1441)) },
				"InvalidParameterCombination", "The product of Period and EvaluationPeriods must not exceed 86400 seconds.",
			},
			{
				// 1.2e10 seconds overflows a Duration's nanoseconds.
				"range past a Duration", func(in *cloudwatchapi.PutMetricAlarmInput) {
					in.Period, in.EvaluationPeriods = new(int32(600000)), new(int32(20000))
				},
				"InvalidParameterCombination", "The product of Period and EvaluationPeriods must not exceed 604800 seconds.",
			},
			{
				"too many tags", func(in *cloudwatchapi.PutMetricAlarmInput) { in.Tags = numberedTags(51) },
				"InvalidParameterValue", "A resource can have no more than 50 tags.",
			},
			{"no threshold", func(in *cloudwatchapi.PutMetricAlarmInput) { in.Threshold = nil }, "MissingParameter", "The parameter Threshold is required."},
			{
				"treat missing data", func(in *cloudwatchapi.PutMetricAlarmInput) { in.TreatMissingData = new("zero") },
				"InvalidParameterValue", "The value zero for parameter TreatMissingData is invalid.",
			},
			{
				"control character in the name", func(in *cloudwatchapi.PutMetricAlarmInput) { in.AlarmName = "cpu\nhigh" },
				"InvalidParameterValue", "The value cpu\nhigh for parameter AlarmName is invalid.",
			},
			{
				"DEL in the name", func(in *cloudwatchapi.PutMetricAlarmInput) { in.AlarmName = "cpu\x7f" },
				"InvalidParameterValue", "The value cpu\x7f for parameter AlarmName is invalid.",
			},
			{
				// M out of N needs M <= N (the alarm evaluation documentation).
				"more datapoints than periods", func(in *cloudwatchapi.PutMetricAlarmInput) {
					in.EvaluationPeriods, in.DatapointsToAlarm = new(int32(2)), new(int32(5))
				},
				"InvalidParameterCombination", "DatapointsToAlarm must be less than or equal to EvaluationPeriods.",
			},
			{
				"non-ASCII dimension value", func(in *cloudwatchapi.PutMetricAlarmInput) {
					in.Dimensions = []cloudwatchapi.Dimension{{Name: "Service", Value: "café"}}
				},
				"InvalidParameterValue", "The value café for parameter Dimensions.member.1.Value is invalid.",
			},
		} {
			in := minimalAlarm("v")
			tc.change(in)
			_, err := call(h, cloudwatchapi.OpPutMetricAlarm, in)
			if err == nil {
				h.Errorf("%s: accepted", tc.name)
				continue
			}
			if err.ID.QueryCode != tc.code || err.Message != tc.message {
				h.Errorf("%s = %s %q\nwant %s %q", tc.name, err.ID.QueryCode, err.Message, tc.code, tc.message)
			}
		}
		if out := h.describe("v"); len(out.MetricAlarms) != 0 {
			h.Errorf("a refused alarm was stored: %+v", out.MetricAlarms)
		}

		// A metric math alarm is stored and echoed as sent.
		math := &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: "math", EvaluationPeriods: new(int32(1)), Threshold: new(1.0),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold),
			Metrics: []cloudwatchapi.MetricDataQuery{
				{Id: "e1", Expression: new("m1 * 2"), ReturnData: new(true)},
				{Id: "m1", MetricStat: &cloudwatchapi.MetricStat{Metric: cloudwatchapi.Metric{Namespace: new("ns"), MetricName: new("m")}, Period: 60, Stat: "Sum"}, ReturnData: new(false)},
			},
		}
		must(h, cloudwatchapi.OpPutMetricAlarm, math)
		if got := h.describe("math").MetricAlarms[0].Metrics; !reflect.DeepEqual(got, math.Metrics) {
			h.Errorf("Metrics = %+v\nwant %+v", got, math.Metrics)
		}
	})
}

// putAlarmCase is one PutMetricAlarm request a validation test sends and
// the error it wants, or none.
type putAlarmCase struct {
	name          string
	in            *cloudwatchapi.PutMetricAlarmInput
	code, message string
}

// checkPutAlarm sends each case and checks its answer.
func checkPutAlarm(h *harness, cases []putAlarmCase) {
	h.Helper()
	for _, tc := range cases {
		_, err := call(h, cloudwatchapi.OpPutMetricAlarm, tc.in)
		switch {
		case tc.code == "" && err != nil:
			h.Errorf("%s = %s %q, want it accepted", tc.name, err.ID.QueryCode, err.Message)
		case tc.code == "":
		case err == nil:
			h.Errorf("%s: accepted, want %s %q", tc.name, tc.code, tc.message)
		case err.ID.QueryCode != tc.code || err.Message != tc.message:
			h.Errorf("%s = %s %q\nwant %s %q", tc.name, err.ID.QueryCode, err.Message, tc.code, tc.message)
		}
	}
}

// metricStatQuery is a Metrics entry reading metric "ns"/m.
func metricStatQuery(id string, returnData *bool) cloudwatchapi.MetricDataQuery {
	return cloudwatchapi.MetricDataQuery{
		Id: id, ReturnData: returnData,
		MetricStat: &cloudwatchapi.MetricStat{Metric: cloudwatchapi.Metric{Namespace: new("ns"), MetricName: new("m")}, Period: 60, Stat: "Sum"},
	}
}

// expressionQuery is a Metrics entry computing expr.
func expressionQuery(id, expr string, returnData *bool) cloudwatchapi.MetricDataQuery {
	return cloudwatchapi.MetricDataQuery{Id: id, Expression: new(expr), ReturnData: returnData}
}

// mathAlarm is a static-threshold alarm on a Metrics array.
func mathAlarm(name string, metrics ...cloudwatchapi.MetricDataQuery) *cloudwatchapi.PutMetricAlarmInput {
	return &cloudwatchapi.PutMetricAlarmInput{
		AlarmName: name, EvaluationPeriods: new(int32(1)), Threshold: new(1.0),
		ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold),
		Metrics:            metrics,
	}
}

// anomalyAlarm is the documentation's anomaly detection alarm: the metric
// returns data, and so does the band, whose ReturnData is absent.
func anomalyAlarm(name string) *cloudwatchapi.PutMetricAlarmInput {
	return &cloudwatchapi.PutMetricAlarmInput{
		AlarmName: name, EvaluationPeriods: new(int32(2)), ThresholdMetricId: new("t1"),
		ComparisonOperator: new(cloudwatchapi.ComparisonOperatorLessThanLowerOrGreaterThanUpperThreshold),
		Metrics: []cloudwatchapi.MetricDataQuery{
			metricStatQuery("m1", new(true)),
			expressionQuery("t1", "ANOMALY_DETECTION_BAND(m1, 3)", nil),
		},
	}
}

// promQLAlarm is the documentation's PromQL alarm.
func promQLAlarm(name string) *cloudwatchapi.PutMetricAlarmInput {
	return &cloudwatchapi.PutMetricAlarmInput{
		AlarmName:          name,
		EvaluationCriteria: cloudwatchapi.EvaluationCriteriaMemberPromQLCriteria{Value: cloudwatchapi.AlarmPromQLCriteria{Query: "avg(cpu_utilization_percent) > 80"}},
		EvaluationInterval: new(int32(30)),
	}
}

// with returns a copy of in changed by change.
func with(in *cloudwatchapi.PutMetricAlarmInput, change func(*cloudwatchapi.PutMetricAlarmInput)) *cloudwatchapi.PutMetricAlarmInput {
	out := *in
	change(&out)
	return &out
}

// A PromQL alarm starts in OK, needs EvaluationInterval and takes none of
// the members EvaluationCriteria replaces; the other kinds take no
// EvaluationInterval (the PutMetricAlarm documentation).
func TestPromQLAlarm(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricAlarm, promQLAlarm("promql"))
		if a := h.describe("promql").MetricAlarms[0]; *a.StateValue != cloudwatchapi.StateValueOk || *a.StateReason != "Unchecked: Initial alarm creation" {
			h.Errorf("a new PromQL alarm is %s %q, want OK", *a.StateValue, *a.StateReason)
		}
		checkPutAlarm(h, []putAlarmCase{
			{
				"threshold", with(promQLAlarm("p"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.Threshold = new(5.0) }),
				"InvalidParameterCombination", "The parameters EvaluationCriteria and Threshold are mutually exclusive and you have specified both.",
			},
			{
				"evaluation periods", with(promQLAlarm("p"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.EvaluationPeriods = new(int32(1)) }),
				"InvalidParameterCombination", "The parameters EvaluationCriteria and EvaluationPeriods are mutually exclusive and you have specified both.",
			},
			{
				"comparison operator", with(promQLAlarm("p"), func(in *cloudwatchapi.PutMetricAlarmInput) {
					in.ComparisonOperator = new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold)
				}),
				"InvalidParameterCombination", "The parameters EvaluationCriteria and ComparisonOperator are mutually exclusive and you have specified both.",
			},
			{
				"no interval", with(promQLAlarm("p"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.EvaluationInterval = nil }),
				"MissingParameter", "The parameter EvaluationInterval is required.",
			},
			{
				"bad interval", with(promQLAlarm("p"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.EvaluationInterval = new(int32(45)) }),
				"InvalidParameterValue", "The value 45 for parameter EvaluationInterval is invalid.",
			},
			{
				"static with interval", with(minimalAlarm("p"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.EvaluationInterval = new(int32(60)) }),
				"InvalidParameterCombination", "The parameters MetricName and EvaluationInterval are mutually exclusive and you have specified both.",
			},
			{
				"metrics with interval", with(anomalyAlarm("p"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.EvaluationInterval = new(int32(60)) }),
				"InvalidParameterCombination", "The parameters Metrics and EvaluationInterval are mutually exclusive and you have specified both.",
			},
		})
		if out := h.describe("p"); len(out.MetricAlarms) != 0 {
			h.Errorf("a refused alarm was stored: %+v", out.MetricAlarms)
		}
		must(h, cloudwatchapi.OpPutMetricAlarm, minimalAlarm("static"))
		if a := h.describe("static").MetricAlarms[0]; *a.StateValue != cloudwatchapi.StateValueInsufficientData {
			h.Errorf("a new static alarm is %s, want INSUFFICIENT_DATA", *a.StateValue)
		}
	})
}

// The anomaly detection operators belong to alarms on a band named by
// ThresholdMetricId, which take no Threshold; every other operator needs a
// Threshold (the PutMetricAlarm documentation).
func TestPutMetricAlarmThresholdFollowsTheOperator(t *testing.T) {
	run(t, func(h *harness) {
		band := cloudwatchapi.ComparisonOperatorLessThanLowerOrGreaterThanUpperThreshold
		checkPutAlarm(h, []putAlarmCase{
			{"anomaly alarm", anomalyAlarm("ok"), "", ""},
			{
				"static with a band operator", with(minimalAlarm("v"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.ComparisonOperator = new(band) }),
				"InvalidParameterCombination", "The ComparisonOperator LessThanLowerOrGreaterThanUpperThreshold is valid only for alarms based on anomaly detection models, which use the Metrics array and ThresholdMetricId.",
			},
			{
				"static with a threshold metric", with(minimalAlarm("v"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.Threshold, in.ThresholdMetricId = nil, new("t1") }),
				"InvalidParameterCombination", "The parameters MetricName and ThresholdMetricId are mutually exclusive and you have specified both.",
			},
			{
				"band without a threshold metric", with(anomalyAlarm("v"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.ThresholdMetricId = nil }),
				"MissingParameter", "The parameter ThresholdMetricId is required.",
			},
			{
				"band with a threshold", with(anomalyAlarm("v"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.Threshold = new(5.0) }),
				"InvalidParameterCombination", "The parameters ThresholdMetricId and Threshold are mutually exclusive and you have specified both.",
			},
			{
				"threshold metric naming no entry", with(anomalyAlarm("v"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.ThresholdMetricId = new("t2") }),
				"InvalidParameterValue", "The value t2 for parameter ThresholdMetricId is invalid.",
			},
			{
				"math without a threshold", with(mathAlarm("v", metricStatQuery("m1", new(true))), func(in *cloudwatchapi.PutMetricAlarmInput) { in.Threshold = nil }),
				"MissingParameter", "The parameter Threshold is required.",
			},
		})
		if out := h.describe("v"); len(out.MetricAlarms) != 0 {
			h.Errorf("a refused alarm was stored: %+v", out.MetricAlarms)
		}
	})
}

// An alarm's Metrics array holds at most 20 entries, 10 of each form, with
// valid Ids, periods and statistics, and exactly one returning data (the
// MetricDataQuery documentation).
func TestPutMetricAlarmMetricsArrayRules(t *testing.T) {
	run(t, func(h *harness) {
		no := new(false)
		// queries is n entries of one form, with Ids prefix0, prefix1 and
		// so on.
		queries := func(n int, prefix string, form func(id string) cloudwatchapi.MetricDataQuery) []cloudwatchapi.MetricDataQuery {
			out := make([]cloudwatchapi.MetricDataQuery, n)
			for i := range out {
				out[i] = form(fmt.Sprintf("%s%d", prefix, i))
			}
			return out
		}
		stats := func(n int) []cloudwatchapi.MetricDataQuery {
			return queries(n, "m", func(id string) cloudwatchapi.MetricDataQuery { return metricStatQuery(id, no) })
		}
		exprs := func(n int) []cloudwatchapi.MetricDataQuery {
			return queries(n, "e", func(id string) cloudwatchapi.MetricDataQuery { return expressionQuery(id, "m0 * 2", no) })
		}
		result := expressionQuery("result", "m0 * 2", new(true))
		badPeriod := metricStatQuery("m1", new(true))
		badPeriod.MetricStat.Period = 45
		badStat := metricStatQuery("m1", new(true))
		badStat.MetricStat.Stat = "median"
		hourlyResult := expressionQuery("e1", "m1 * 2", new(true))
		hourlyResult.Period = new(int32(3600))
		badDimension := metricStatQuery("m1", new(true))
		badDimension.MetricStat.Metric.Dimensions = []cloudwatchapi.Dimension{{Name: ":shard", Value: "1"}}
		checkPutAlarm(h, []putAlarmCase{
			{"one entry, ReturnData absent", mathAlarm("ok", metricStatQuery("m1", nil)), "", ""},
			{
				"as many datapoints as periods", with(mathAlarm("ok", metricStatQuery("m1", nil)), func(in *cloudwatchapi.PutMetricAlarmInput) {
					in.EvaluationPeriods, in.DatapointsToAlarm = new(int32(3)), new(int32(3))
				}),
				"", "",
			},
			{
				"more datapoints than periods", with(mathAlarm("v", metricStatQuery("m1", nil)), func(in *cloudwatchapi.PutMetricAlarmInput) {
					in.EvaluationPeriods, in.DatapointsToAlarm = new(int32(3)), new(int32(4))
				}),
				"InvalidParameterCombination", "DatapointsToAlarm must be less than or equal to EvaluationPeriods.",
			},
			{
				"dimension name starting with a colon", mathAlarm("v", badDimension),
				"InvalidParameterValue", "The value :shard for parameter Metrics.member.1.MetricStat.Metric.Dimensions.member.1.Name is invalid.",
			},
			{"ten of each form", mathAlarm("ok", slices.Concat(stats(10), exprs(9), []cloudwatchapi.MetricDataQuery{result})...), "", ""},
			{
				"21 entries", mathAlarm("v", slices.Concat(exprs(20), []cloudwatchapi.MetricDataQuery{result})...),
				"InvalidParameterValue", "The collection Metrics must not have a size greater than 20.",
			},
			{
				"11 metric stats", mathAlarm("v", slices.Concat(stats(11), []cloudwatchapi.MetricDataQuery{result})...),
				"InvalidParameterValue", "The Metrics array can contain at most 10 entries with a MetricStat.",
			},
			{
				"11 expressions", mathAlarm("v", slices.Concat(exprs(10), []cloudwatchapi.MetricDataQuery{result})...),
				"InvalidParameterValue", "The Metrics array can contain at most 10 entries with an Expression.",
			},
			{
				"none returns data", mathAlarm("v", metricStatQuery("m1", no), expressionQuery("e1", "m1 * 2", no)),
				"InvalidParameterValue", "Exactly one entry of the Metrics array must have ReturnData set to true; 0 do.",
			},
			{
				"two return data, one by default", mathAlarm("v", metricStatQuery("m1", nil), expressionQuery("e1", "m1 * 2", new(true))),
				"InvalidParameterValue", "Exactly one entry of the Metrics array must have ReturnData set to true; 2 do.",
			},
			{
				"Id form", mathAlarm("v", metricStatQuery("M1", new(true))),
				"InvalidParameterValue", "The value M1 for parameter Metrics.member.1.Id is invalid.",
			},
			{
				"Id reused", mathAlarm("v", metricStatQuery("m1", no), expressionQuery("m1", "m1 * 2", new(true))),
				"InvalidParameterValue", "The value m1 for parameter Metrics.member.2.Id is not unique.",
			},
			{
				"neither form", mathAlarm("v", cloudwatchapi.MetricDataQuery{Id: "m1", ReturnData: new(true)}),
				"MissingParameter", "One of the parameters Metrics.member.1.MetricStat or Metrics.member.1.Expression is required.",
			},
			{
				"period", mathAlarm("v", badPeriod),
				"InvalidParameterValue", "The value 45 for parameter Metrics.member.1.MetricStat.Period is invalid.",
			},
			{
				"statistic", mathAlarm("v", badStat),
				"InvalidParameterValue", "The value median for parameter Metrics.member.1.MetricStat.Stat is invalid.",
			},
			{
				"range over a day", with(mathAlarm("v", metricStatQuery("m1", nil)), func(in *cloudwatchapi.PutMetricAlarmInput) { in.EvaluationPeriods = new(int32(1441)) }),
				"InvalidParameterCombination", "The product of Period and EvaluationPeriods must not exceed 86400 seconds.",
			},
			{
				"range over a week, by the expression's period", with(mathAlarm("v", metricStatQuery("m1", no), hourlyResult), func(in *cloudwatchapi.PutMetricAlarmInput) {
					in.EvaluationPeriods = new(int32(169))
				}),
				"InvalidParameterCombination", "The product of Period and EvaluationPeriods must not exceed 604800 seconds.",
			},
			{
				"a week by the expression's period", with(mathAlarm("ok", metricStatQuery("m1", no), hourlyResult), func(in *cloudwatchapi.PutMetricAlarmInput) {
					in.EvaluationPeriods = new(int32(168))
				}),
				"", "",
			},
		})
		if out := h.describe("v"); len(out.MetricAlarms) != 0 {
			h.Errorf("a refused alarm was stored: %+v", out.MetricAlarms)
		}
	})
}

// extendedStat draws a statistic of the grammar AWS documents beyond the
// basic five.
func extendedStat() *rapid.Generator[string] {
	decimals := func(t *rapid.T) string {
		n := rapid.IntRange(0, 10).Draw(t, "decimals")
		if n == 0 {
			return ""
		}
		return "." + rapid.StringOfN(rapid.RuneFrom([]rune("0123456789")), n, n, n).Draw(t, "fraction")
	}
	percentage := rapid.Custom(func(t *rapid.T) string {
		if rapid.Bool().Draw(t, "hundred") {
			return "100"
		}
		return strconv.Itoa(rapid.IntRange(0, 99).Draw(t, "whole")) + decimals(t)
	})
	value := rapid.Custom(func(t *rapid.T) string {
		return strconv.Itoa(rapid.IntRange(0, 1_000_000).Draw(t, "whole")) + decimals(t)
	})
	bounds := func(t *rapid.T, bound *rapid.Generator[string]) string {
		lower, upper := bound.Draw(t, "lower"), bound.Draw(t, "upper")
		switch rapid.IntRange(0, 2).Draw(t, "omit") {
		case 1:
			lower = ""
		case 2:
			upper = ""
		}
		return "(" + lower + ":" + upper + ")"
	}
	return rapid.OneOf(
		rapid.Just("IQM"),
		rapid.Custom(func(t *rapid.T) string {
			return rapid.SampledFrom([]string{"p", "tm", "tc", "ts", "wm"}).Draw(t, "prefix") + percentage.Draw(t, "n")
		}),
		rapid.Custom(func(t *rapid.T) string {
			bound := value
			if rapid.Bool().Draw(t, "percent") {
				bound = rapid.Custom(func(t *rapid.T) string { return percentage.Draw(t, "p") + "%" })
			}
			return rapid.SampledFrom([]string{"TM", "TC", "TS", "WM"}).Draw(t, "prefix") + bounds(t, bound)
		}),
		rapid.Custom(func(t *rapid.T) string { return "PR" + bounds(t, value) }),
	)
}

// ExtendedStatistic takes the statistics AWS documents and refuses any
// other name (CloudWatch statistics definitions).
func TestPutMetricAlarmExtendedStatistic(t *testing.T) {
	run(t, func(h *harness) {
		alarm := func(s string) *cloudwatchapi.PutMetricAlarmInput {
			return with(minimalAlarm("x"), func(in *cloudwatchapi.PutMetricAlarmInput) { in.Statistic, in.ExtendedStatistic = nil, new(s) })
		}
		valid := []string{
			"p90", "p99.9", "tm90", "tc90", "ts90", "wm98", "IQM", "TM(2%:98%)", "TM(150:1000)", "TM(:95%)", "TS(80%:)",
			"TC(0.005:0.030)", "TC(80:500)", "TC(:0.5)", "WM(10%:90%)", "PR(:300)", "PR(100:2000)", "PR(10:)",
		}
		invalid := []string{
			"p101", "median", "P99", "Tm90", "tm", "p", "p.5", "p9.", "p99.12345678901", "IQM2", "TM()", "TM(:)", "TM(10%:500)",
			"TM(10%:90%", "TM10%:90%)", "TM(1:2:3)", "TM(101%:)", "PR(10%:20%)", "pr(1:2)", "tm(10%:90%)", "TM(-1:5)", "Sum",
		}
		cases := make([]putAlarmCase, 0, len(valid)+len(invalid))
		for _, s := range valid {
			cases = append(cases, putAlarmCase{s, alarm(s), "", ""})
		}
		for _, s := range invalid {
			cases = append(cases, putAlarmCase{s, alarm(s), "InvalidParameterValue", "The value " + s + " for parameter ExtendedStatistic is invalid."})
		}
		checkPutAlarm(h, cases)

		rapid.Check(bubbleTB{h.T}, func(t *rapid.T) {
			s := extendedStat().Draw(t, "stat")
			if _, err := call(h, cloudwatchapi.OpPutMetricAlarm, alarm(s)); err != nil {
				t.Fatalf("%s = %s %q, want it accepted", s, err.ID.QueryCode, err.Message)
			}
			bad := s + rapid.SampledFrom([]string{"x", "%", ")", ":", "("}).Draw(t, "suffix")
			if _, err := call(h, cloudwatchapi.OpPutMetricAlarm, alarm(bad)); err == nil || err.ID.QueryCode != "InvalidParameterValue" {
				t.Fatalf("%s = %v, want InvalidParameterValue", bad, err)
			}
		})
	})
}

func TestTagResourceNamesOnlyAlarmsOfTheCallersScope(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricAlarm, minimalAlarm("a"))
		arn := *h.describe("a").MetricAlarms[0].AlarmArn
		other := "arn:aws:cloudwatch:eu-west-1:" + h.Scope.Account.String() + ":alarm:a"
		_, err := call(h, cloudwatchapi.OpTagResource, &cloudwatchapi.TagResourceInput{ResourceARN: other, Tags: []cloudwatchapi.Tag{{Key: "k", Value: "v"}}})
		wantError(h.T, err, "ResourceNotFoundException", "The resource "+other+" does not exist.")

		_, err = call(h, cloudwatchapi.OpTagResource, &cloudwatchapi.TagResourceInput{ResourceARN: arn, Tags: numberedTags(51)})
		wantError(h.T, err, "InvalidParameterValue", "A resource can have no more than 50 tags.")
		if got := h.tags(arn); len(got) != 0 {
			h.Errorf("a refused TagResource stored %v", got)
		}
	})
}

// A snapshot round trip keeps alarms, their union members included, and
// their echo.
func TestSnapshotRoundTripKeepsTheEcho(t *testing.T) {
	run(t, func(h *harness) {
		in := minimalAlarm("a")
		in.EvaluationWindow = cloudwatchapi.EvaluationWindowMemberWallClockWindow{Value: cloudwatchapi.WallClockWindow{Timezone: new("UTC")}}
		in.Tags = []cloudwatchapi.Tag{{Key: "k", Value: "v"}}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		must(h, cloudwatchapi.OpPutMetricAlarm, minimalAlarm("plain"))
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{{MetricName: "m", Value: new(1.0)}}})
		before := h.describe()
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if got := h.describe(); len(got.MetricAlarms) != 0 {
			h.Fatalf("after Reset = %+v", got.MetricAlarms)
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		if after := h.describe(); !reflect.DeepEqual(after, before) {
			h.Errorf("after Import:\n%+v\nbefore:\n%+v", after, before)
		}
		if got := h.tags(*before.MetricAlarms[0].AlarmArn); !slices.Equal(got, []cloudwatchapi.Tag{{Key: "k", Value: "v"}}) {
			h.Errorf("tags after Import = %v", got)
		}
		if got := must(h, cloudwatchapi.OpListMetrics, &cloudwatchapi.ListMetricsInput{}).Metrics; len(got) != 1 {
			h.Errorf("metrics after Import = %+v", got)
		}
	})
}
