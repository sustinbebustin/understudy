package gosdk_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// Research: docs/research/phase1/secrets-logs-events-alarms.md section 5,
// wire-cbor.md (the Go client speaks rpcv2Cbor to CloudWatch),
// error-matrix.md 5.13.

// alarmInput is a minimal static-threshold alarm.
func alarmInput(alarmName string) *cloudwatch.PutMetricAlarmInput {
	return &cloudwatch.PutMetricAlarmInput{
		AlarmName: aws.String(alarmName), Namespace: aws.String("Conformance"), MetricName: aws.String("Errors"),
		Statistic: types.StatisticSum, Period: aws.Int32(60), EvaluationPeriods: aws.Int32(1),
		Threshold: aws.Float64(5), ComparisonOperator: types.ComparisonOperatorGreaterThanThreshold,
	}
}

// putAlarm creates an alarm and deletes it when the test ends.
func putAlarm(t *testing.T, in *cloudwatch.PutMetricAlarmInput) {
	t.Helper()
	client := cloudwatch.NewFromConfig(awsCfg)
	_, err := client.PutMetricAlarm(t.Context(), in)
	ok(t, err, "PutMetricAlarm "+aws.ToString(in.AlarmName))
	t.Cleanup(func() {
		_, _ = client.DeleteAlarms(context.Background(), &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{aws.ToString(in.AlarmName)}})
	})
}

// describeAlarm returns the one metric alarm named alarmName.
func describeAlarm(t *testing.T, client *cloudwatch.Client, alarmName string) types.MetricAlarm {
	t.Helper()
	out, err := client.DescribeAlarms(t.Context(), &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{alarmName}})
	ok(t, err, "DescribeAlarms")
	if len(out.MetricAlarms) != 1 {
		t.Fatalf("DescribeAlarms(%s) = %d metric alarms, want 1", alarmName, len(out.MetricAlarms))
	}
	return out.MetricAlarms[0]
}

func TestCloudWatchAlarms(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := cloudwatch.NewFromConfig(awsCfg)
	acct := account(t)
	alarmName := name("alarm")
	in := alarmInput(alarmName)
	in.Tags = []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}}
	putAlarm(t, in)
	alarmArn := "arn:aws:cloudwatch:" + region() + ":" + acct + ":alarm:" + alarmName

	t.Run("DescribeAlarms", func(t *testing.T) {
		a := describeAlarm(t, client, alarmName)
		if aws.ToString(a.AlarmArn) != alarmArn {
			t.Errorf("AlarmArn = %q, want %q", aws.ToString(a.AlarmArn), alarmArn)
		}
		if a.StateValue != types.StateValueInsufficientData || aws.ToString(a.StateReason) != "Unchecked: Initial alarm creation" ||
			a.StateUpdatedTimestamp == nil || a.StateTransitionedTimestamp == nil || a.AlarmConfigurationUpdatedTimestamp == nil {
			t.Errorf("initial state = %q %q, timestamps %v %v %v", a.StateValue, aws.ToString(a.StateReason), a.StateUpdatedTimestamp, a.StateTransitionedTimestamp, a.AlarmConfigurationUpdatedTimestamp)
		}
		if !aws.ToBool(a.ActionsEnabled) || a.Statistic != types.StatisticSum || aws.ToInt32(a.Period) != 60 || aws.ToFloat64(a.Threshold) != 5 ||
			a.ComparisonOperator != types.ComparisonOperatorGreaterThanThreshold || aws.ToString(a.Namespace) != "Conformance" || aws.ToString(a.MetricName) != "Errors" {
			t.Errorf("alarm configuration = %+v", a)
		}
		if a.TreatMissingData != nil || a.Unit != "" || a.AlarmDescription != nil || a.DatapointsToAlarm != nil {
			t.Errorf("unset fields present: TreatMissingData %v, Unit %q, AlarmDescription %v, DatapointsToAlarm %v", a.TreatMissingData, a.Unit, a.AlarmDescription, a.DatapointsToAlarm)
		}
		none, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{name("missing")}})
		ok(t, err, "DescribeAlarms of a missing name (empty, not an error)")
		if len(none.MetricAlarms) != 0 {
			t.Errorf("DescribeAlarms of a missing name = %+v", none.MetricAlarms)
		}
		byPrefix, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNamePrefix: aws.String(alarmName)})
		ok(t, err, "DescribeAlarms AlarmNamePrefix")
		if len(byPrefix.MetricAlarms) != 1 {
			t.Errorf("DescribeAlarms AlarmNamePrefix = %d alarms, want 1", len(byPrefix.MetricAlarms))
		}
	})

	t.Run("SetAlarmState", func(t *testing.T) {
		_, err := client.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{AlarmName: aws.String(alarmName), StateValue: types.StateValueAlarm, StateReason: aws.String("conformance test")})
		ok(t, err, "SetAlarmState ALARM")
		a := describeAlarm(t, client, alarmName)
		if a.StateValue != types.StateValueAlarm || aws.ToString(a.StateReason) != "conformance test" {
			t.Errorf("state after SetAlarmState = %q %q", a.StateValue, aws.ToString(a.StateReason))
		}
		_, err = client.PutMetricAlarm(ctx, alarmInput(alarmName))
		ok(t, err, "PutMetricAlarm update")
		if a := describeAlarm(t, client, alarmName); a.StateValue != types.StateValueAlarm {
			t.Errorf("state after updating the alarm = %q, want ALARM kept", a.StateValue)
		}
		_, err = client.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{AlarmName: aws.String(name("missing")), StateValue: types.StateValueOk, StateReason: aws.String("x")})
		errAs[*types.ResourceNotFound](t, err, "SetAlarmState on a missing alarm")
		wantStatus(t, err, "SetAlarmState on a missing alarm", 404)
		_, err = client.SetAlarmState(ctx, &cloudwatch.SetAlarmStateInput{AlarmName: aws.String(alarmName), StateValue: types.StateValue("INVALID"), StateReason: aws.String("x")})
		apiErr(t, err, "SetAlarmState with an invalid state", "ValidationError")
		wantMessage(t, err, "SetAlarmState with an invalid state", "1 validation error detected: Value 'INVALID' at 'stateValue' failed to satisfy constraint: Member must satisfy enum value set: [INSUFFICIENT_DATA, ALARM, OK]")
	})

	t.Run("alarm actions", func(t *testing.T) {
		_, err := client.DisableAlarmActions(ctx, &cloudwatch.DisableAlarmActionsInput{AlarmNames: []string{alarmName}})
		ok(t, err, "DisableAlarmActions")
		if a := describeAlarm(t, client, alarmName); aws.ToBool(a.ActionsEnabled) {
			t.Errorf("ActionsEnabled after DisableAlarmActions = true")
		}
		_, err = client.EnableAlarmActions(ctx, &cloudwatch.EnableAlarmActionsInput{AlarmNames: []string{alarmName}})
		ok(t, err, "EnableAlarmActions")
		if a := describeAlarm(t, client, alarmName); !aws.ToBool(a.ActionsEnabled) {
			t.Errorf("ActionsEnabled after EnableAlarmActions = false")
		}
	})

	t.Run("tags", func(t *testing.T) {
		out, err := client.ListTagsForResource(ctx, &cloudwatch.ListTagsForResourceInput{ResourceARN: aws.String(alarmArn)})
		ok(t, err, "ListTagsForResource")
		if len(out.Tags) != 1 || aws.ToString(out.Tags[0].Key) != "team" || aws.ToString(out.Tags[0].Value) != "platform" {
			t.Errorf("tags from PutMetricAlarm = %+v", out.Tags)
		}
		_, err = client.TagResource(ctx, &cloudwatch.TagResourceInput{ResourceARN: aws.String(alarmArn), Tags: []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}}})
		ok(t, err, "TagResource")
		_, err = client.UntagResource(ctx, &cloudwatch.UntagResourceInput{ResourceARN: aws.String(alarmArn), TagKeys: []string{"team"}})
		ok(t, err, "UntagResource")
		out, err = client.ListTagsForResource(ctx, &cloudwatch.ListTagsForResourceInput{ResourceARN: aws.String(alarmArn)})
		ok(t, err, "ListTagsForResource")
		if len(out.Tags) != 1 || aws.ToString(out.Tags[0].Key) != "env" {
			t.Errorf("tags after tag and untag = %+v, want env=test", out.Tags)
		}
		_, err = client.PutMetricAlarm(ctx, alarmInput(alarmName))
		ok(t, err, "PutMetricAlarm update without tags")
		out, err = client.ListTagsForResource(ctx, &cloudwatch.ListTagsForResourceInput{ResourceARN: aws.String(alarmArn)})
		ok(t, err, "ListTagsForResource")
		if len(out.Tags) != 1 {
			t.Errorf("tags after an update = %+v, want them kept", out.Tags)
		}
	})

	t.Run("DeleteAlarms", func(t *testing.T) {
		other := name("doomed")
		_, err := client.PutMetricAlarm(ctx, alarmInput(other))
		ok(t, err, "PutMetricAlarm")
		_, err = client.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: []string{other, name("never-existed")}})
		ok(t, err, "DeleteAlarms with one existing and one missing name")
		out, err := client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{AlarmNames: []string{other}})
		ok(t, err, "DescribeAlarms")
		if len(out.MetricAlarms) != 0 {
			t.Errorf("alarm still present after DeleteAlarms")
		}
		tags, err := client.ListTagsForResource(ctx, &cloudwatch.ListTagsForResourceInput{ResourceARN: aws.String("arn:aws:cloudwatch:" + region() + ":" + acct + ":alarm:" + other)})
		ok(t, err, "ListTagsForResource of a deleted alarm (empty, not an error)")
		if len(tags.Tags) != 0 {
			t.Errorf("tags of a deleted alarm = %+v", tags.Tags)
		}
	})
}

func TestCloudWatchMetrics(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := cloudwatch.NewFromConfig(awsCfg)
	namespace := "Conformance/" + name("ns")
	dims := []types.Dimension{{Name: aws.String("Service"), Value: aws.String("api")}}
	at := time.Now().Add(-5 * time.Minute).Truncate(time.Minute).Add(10 * time.Second)

	_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(namespace), MetricData: []types.MetricDatum{
		{MetricName: aws.String("Latency"), Dimensions: dims, Timestamp: aws.Time(at), Value: aws.Float64(1), Unit: types.StandardUnitMilliseconds},
		{MetricName: aws.String("Latency"), Dimensions: dims, Timestamp: aws.Time(at.Add(5 * time.Second)), Value: aws.Float64(2), Unit: types.StandardUnitMilliseconds},
		{MetricName: aws.String("Latency"), Dimensions: dims, Timestamp: aws.Time(at.Add(10 * time.Second)), Values: []float64{3}, Counts: []float64{1}, Unit: types.StandardUnitMilliseconds},
	}})
	ok(t, err, "PutMetricData")

	t.Run("GetMetricData statistics", func(t *testing.T) {
		metric := &types.Metric{Namespace: aws.String(namespace), MetricName: aws.String("Latency"), Dimensions: dims}
		var queries []types.MetricDataQuery
		for _, stat := range []string{"Sum", "Average", "Minimum", "Maximum", "SampleCount"} {
			// Query ids must start with a lower-case letter.
			queries = append(queries, types.MetricDataQuery{Id: aws.String(strings.ToLower(stat)), MetricStat: &types.MetricStat{Metric: metric, Period: aws.Int32(60), Stat: aws.String(stat)}})
		}
		out, err := client.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
			MetricDataQueries: queries, StartTime: aws.Time(at.Add(-time.Hour)), EndTime: aws.Time(time.Now().Add(time.Minute)),
		})
		ok(t, err, "GetMetricData")
		want := map[string]float64{"sum": 6, "average": 2, "minimum": 1, "maximum": 3, "samplecount": 3}
		if len(out.MetricDataResults) != len(want) {
			t.Fatalf("GetMetricData returned %d results, want %d", len(out.MetricDataResults), len(want))
		}
		for _, r := range out.MetricDataResults {
			id := aws.ToString(r.Id)
			if r.StatusCode != types.StatusCodeComplete || len(r.Values) != 1 || len(r.Timestamps) != 1 || r.Values[0] != want[id] {
				t.Errorf("result %s = status %q, values %v, timestamps %v; want one value %v", id, r.StatusCode, r.Values, r.Timestamps, want[id])
				continue
			}
			if !r.Timestamps[0].Equal(at.Truncate(time.Minute)) {
				t.Errorf("result %s timestamp = %v, want the period start %v", id, r.Timestamps[0], at.Truncate(time.Minute))
			}
		}
	})

	t.Run("PutMetricData validation", func(t *testing.T) {
		_, err := client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(namespace), MetricData: []types.MetricDatum{
			{MetricName: aws.String("Bad"), Value: aws.Float64(1), Values: []float64{1}},
		}})
		errAs[*types.InvalidParameterCombinationException](t, err, "PutMetricData with Value and Values")
		apiErr(t, err, "PutMetricData with Value and Values", "InvalidParameterCombination")
		wantMessage(t, err, "PutMetricData with Value and Values", "The parameters MetricData.member.1.Value and MetricData.member.1.Values are mutually exclusive and you have specified both.")
		_, err = client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(namespace), MetricData: []types.MetricDatum{
			{MetricName: aws.String("Bad"), Values: []float64{1, 2}, Counts: []float64{1}},
		}})
		errAs[*types.InvalidParameterValueException](t, err, "PutMetricData with Values and Counts of different sizes")
		wantMessage(t, err, "PutMetricData with mismatched Counts", "The parameters MetricData.member.1.Values and MetricData.member.1.Counts must be of the same size.")
	})

	t.Run("ListMetrics", func(t *testing.T) {
		out, err := client.ListMetrics(ctx, &cloudwatch.ListMetricsInput{Namespace: aws.String(namespace)})
		ok(t, err, "ListMetrics")
		if len(out.Metrics) != 1 || aws.ToString(out.Metrics[0].MetricName) != "Latency" || len(out.Metrics[0].Dimensions) != 1 {
			t.Errorf("ListMetrics = %+v, want the one Latency metric", out.Metrics)
		}
	})
}
