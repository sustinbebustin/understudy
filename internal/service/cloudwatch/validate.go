package cloudwatch

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The checks the generated validators cannot make, with CloudWatch's
// hand-written texts: the Query parameter names (MetricData.member.1.Value)
// appear in them even on the JSON and CBOR wires. Texts marked recorded
// are AWS's (LocalStack snapshot, secrets-logs-events-alarms.md 5.2 and
// 5.3); the rest follow the recorded templates and are inferred.

// Limits of PutMetricData and PutMetricAlarm (secrets-logs-events-alarms.md
// 5.1 and 5.2).
const (
	maxMetricsPerPut = 1000
	maxEntities      = 2
	maxValues        = 150
	// valueBound is the magnitude past which a value is rejected: 2^360.
	valueBound = 0x1p360
	// maxEvaluationRange and maxShortEvaluationRange bound Period times
	// EvaluationPeriods: 7 days, or 1 day when Period is under an hour.
	maxEvaluationRange      = 7 * 24 * time.Hour
	maxShortEvaluationRange = 24 * time.Hour
)

// errExclusive is the recorded InvalidParameterCombination template.
func errExclusive(a, b string) *awserr.Error {
	return cloudwatchapi.ErrInvalidParameterCombinationException("The parameters " + a + " and " + b + " are mutually exclusive and you have specified both.")
}

// errMissing is a required parameter the model leaves optional (inferred
// text).
func errMissing(param string) *awserr.Error {
	return cloudwatchapi.ErrMissingRequiredParameterException("The parameter " + param + " is required.")
}

// errInvalidValue is a value AWS refuses (inferred text).
func errInvalidValue(value, param string) *awserr.Error {
	return cloudwatchapi.ErrInvalidParameterValueException("The value " + value + " for parameter " + param + " is invalid.")
}

// checkPutMetricAlarm checks that an alarm names exactly one kind of
// metric (MetricName, Metrics or EvaluationCriteria) with what that kind
// needs and nothing it excludes (secrets-logs-events-alarms.md 5.1).
func checkPutMetricAlarm(in *cloudwatchapi.PutMetricAlarmInput) *awserr.Error {
	kind, err := alarmKindOf(in)
	if err != nil {
		return err
	}
	switch kind {
	case staticAlarm:
		err = checkStaticAlarm(in)
	case metricsAlarm:
		err = checkMetricsAlarm(in)
	case promQLAlarm:
	}
	if err != nil {
		return err
	}
	if v := in.TreatMissingData; v != nil && !slices.Contains([]string{"breaching", "notBreaching", "ignore", "missing"}, *v) {
		return errInvalidValue(*v, "TreatMissingData")
	}
	if v := in.EvaluateLowSampleCountPercentile; v != nil && !slices.Contains([]string{"evaluate", "ignore"}, *v) {
		return errInvalidValue(*v, "EvaluateLowSampleCountPercentile")
	}
	// The quota holds on an update too, whose Tags are then ignored
	// (inferred: AWS documents the quota, not when it checks it).
	if len(tagMap(nil, in.Tags)) > maxTags {
		return errTooManyTags()
	}
	return nil
}

// alarmKind is the one kind of metric an alarm watches.
type alarmKind uint8

const (
	// staticAlarm watches MetricName with a statistic over a period.
	staticAlarm alarmKind = iota + 1
	// metricsAlarm watches the Metrics array: metric math or Insights.
	metricsAlarm
	// promQLAlarm watches EvaluationCriteria's PromQL query.
	promQLAlarm
)

// alarmKindOf reads which kind an alarm is; naming none or more than one
// is refused, as is a statistic named both ways.
func alarmKindOf(in *cloudwatchapi.PutMetricAlarmInput) (alarmKind, *awserr.Error) {
	static, metrics, promql := in.MetricName != nil, len(in.Metrics) > 0, in.EvaluationCriteria != nil
	switch {
	case static && metrics:
		return 0, errExclusive("MetricName", "Metrics")
	case promql && static:
		return 0, errExclusive("EvaluationCriteria", "MetricName")
	case promql && metrics:
		return 0, errExclusive("EvaluationCriteria", "Metrics")
	case in.Statistic != nil && in.ExtendedStatistic != nil:
		return 0, errExclusive("Statistic", "ExtendedStatistic")
	case static:
		return staticAlarm, nil
	case metrics:
		return metricsAlarm, nil
	case promql:
		return promQLAlarm, nil
	}
	return 0, cloudwatchapi.ErrMissingRequiredParameterException("One of the parameters MetricName, Metrics or EvaluationCriteria is required.")
}

// checkStaticAlarm needs a statistic, a valid period, the evaluation
// members and an evaluation range within AWS's bound.
func checkStaticAlarm(in *cloudwatchapi.PutMetricAlarmInput) *awserr.Error {
	switch {
	case in.Statistic == nil && in.ExtendedStatistic == nil:
		return cloudwatchapi.ErrMissingRequiredParameterException("One of the parameters Statistic or ExtendedStatistic is required.")
	case in.Period == nil:
		return errMissing("Period")
	case !validPeriod(*in.Period):
		return cloudwatchapi.ErrInvalidParameterValueException("The parameter Period must be 10, 20, 30 or a multiple of 60.")
	}
	if err := checkEvaluation(in); err != nil {
		return err
	}
	return checkEvaluationRange(*in.Period, *in.EvaluationPeriods)
}

// checkMetricsAlarm refuses the single-metric members, which the Metrics
// array replaces, and needs the evaluation members.
func checkMetricsAlarm(in *cloudwatchapi.PutMetricAlarmInput) *awserr.Error {
	for _, p := range []struct {
		name string
		set  bool
	}{
		{"Namespace", in.Namespace != nil},
		{"Dimensions", len(in.Dimensions) > 0},
		{"Period", in.Period != nil},
		{"Unit", in.Unit != nil},
		{"Statistic", in.Statistic != nil},
		{"ExtendedStatistic", in.ExtendedStatistic != nil},
	} {
		if p.set {
			return errExclusive("Metrics", p.name)
		}
	}
	return checkEvaluation(in)
}

// checkEvaluation needs what a metric alarm compares: the number of
// periods, the operator and a threshold or a threshold metric.
func checkEvaluation(in *cloudwatchapi.PutMetricAlarmInput) *awserr.Error {
	switch {
	case in.EvaluationPeriods == nil:
		return errMissing("EvaluationPeriods")
	case in.ComparisonOperator == nil:
		return errMissing("ComparisonOperator")
	case in.Threshold == nil && in.ThresholdMetricId == nil:
		return errMissing("Threshold")
	}
	return nil
}

// validPeriod reports a period AWS accepts for a metric alarm: 10, 20, 30
// or a multiple of 60 seconds.
func validPeriod(p int32) bool { return p == 10 || p == 20 || p == 30 || p > 0 && p%60 == 0 }

// validQueryPeriod reports a period AWS accepts for a GetMetricData query:
// 1, 5, 10, 20, 30 or a multiple of 60 seconds.
func validQueryPeriod(p int32) bool { return p == 1 || p == 5 || validPeriod(p) }

// checkEvaluationRange bounds the time an alarm evaluates over. It compares
// in seconds: the product of two int32 values always fits an int64, where
// a Duration in nanoseconds would wrap.
func checkEvaluationRange(period, evaluationPeriods int32) *awserr.Error {
	limit := maxEvaluationRange
	if time.Duration(period)*time.Second < time.Hour {
		limit = maxShortEvaluationRange
	}
	if int64(period)*int64(evaluationPeriods) <= int64(limit/time.Second) {
		return nil
	}
	return cloudwatchapi.ErrInvalidParameterCombinationException("The product of Period and EvaluationPeriods must not exceed " +
		strconv.FormatInt(int64(limit/time.Second), 10) + " seconds.")
}

// validateSetAlarmState checks StateValue, whose check the overlay hands
// to the service: AWS lists the values in its own order, not the model's
// (AWS-recorded). The violation renders as the Coral enum text under
// ValidationError.
func validateSetAlarmState(in *cloudwatchapi.SetAlarmStateInput) []awserr.Violation {
	if in.StateValue.Known() {
		return nil
	}
	return []awserr.Violation{{
		Path:       awserr.Path{"stateValue"},
		Value:      awserr.RenderedText{Text: string(in.StateValue)},
		Constraint: awserr.Enum{Values: []string{"INSUFFICIENT_DATA", "ALARM", "OK"}},
	}}
}

// checkSetAlarmState refuses a StateReasonData that is not JSON, which the
// model documents the member as and answers with InvalidFormatFault.
func checkSetAlarmState(in *cloudwatchapi.SetAlarmStateInput) *awserr.Error {
	if in.StateReasonData != nil && !json.Valid([]byte(*in.StateReasonData)) {
		return errInvalidFormat()
	}
	return nil
}

// checkDescribeAlarms refuses AlarmNames with AlarmNamePrefix.
func checkDescribeAlarms(in *cloudwatchapi.DescribeAlarmsInput) *awserr.Error {
	if len(in.AlarmNames) > 0 && in.AlarmNamePrefix != nil {
		return errExclusive("AlarmNames", "AlarmNamePrefix")
	}
	return nil
}

// datum is one MetricDatum of a PutMetricData request with the Query
// parameter path AWS's texts name it by.
type datum struct {
	path string
	d    *cloudwatchapi.MetricDatum
}

// datums lists a request's MetricData and the MetricData of each
// EntityMetricData, whose entity understudy does not keep.
func datums(in *cloudwatchapi.PutMetricDataInput) []datum {
	var out []datum
	for i := range in.MetricData {
		out = append(out, datum{path: "MetricData.member." + strconv.Itoa(i+1), d: &in.MetricData[i]})
	}
	for i := range in.EntityMetricData {
		e := &in.EntityMetricData[i]
		for j := range e.MetricData {
			out = append(out, datum{path: "EntityMetricData.member." + strconv.Itoa(i+1) + ".MetricData.member." + strconv.Itoa(j+1), d: &e.MetricData[j]})
		}
	}
	return out
}

// errCollectionSize is a list past its limit (recorded template).
func errCollectionSize(param string, limit int) *awserr.Error {
	return cloudwatchapi.ErrInvalidParameterValueException("The collection " + param + " must not have a size greater than " + strconv.Itoa(limit) + ".")
}

// checkPutMetricData runs the request rules that need no state: the
// reserved namespace, the entity and metric counts, and per datum the value
// forms and their bounds. The timestamp windows read the clock, so the
// handler checks them.
func checkPutMetricData(in *cloudwatchapi.PutMetricDataInput) *awserr.Error {
	if strings.HasPrefix(in.Namespace, "AWS/") {
		return errInvalidValue(in.Namespace, "Namespace")
	}
	if len(in.EntityMetricData) > maxEntities {
		return errCollectionSize("EntityMetricData", maxEntities) // inferred
	}
	ds := datums(in)
	if len(ds) > maxMetricsPerPut {
		return errCollectionSize("MetricData", maxMetricsPerPut)
	}
	for _, d := range ds {
		if err := checkDatum(d); err != nil {
			return err
		}
	}
	return nil
}

func checkDatum(d datum) *awserr.Error {
	value, values, stats := d.d.Value != nil, len(d.d.Values) > 0, d.d.StatisticValues != nil
	switch {
	case value && values:
		return errExclusive(d.path+".Value", d.path+".Values") // recorded
	case value && stats:
		return errExclusive(d.path+".Value", d.path+".StatisticValues") // recorded
	case values && stats:
		return errExclusive(d.path+".Values", d.path+".StatisticValues")
	case len(d.d.Counts) > 0 && len(d.d.Counts) != len(d.d.Values):
		return cloudwatchapi.ErrInvalidParameterValueException("The parameters " + d.path + ".Values and " + d.path + ".Counts must be of the same size.") // recorded
	case !value && !values && !stats:
		return cloudwatchapi.ErrMissingRequiredParameterException("One of the parameters " + d.path + ".Value, " + d.path + ".Values or " + d.path + ".StatisticValues is required.")
	case len(d.d.Values) > maxValues:
		return errCollectionSize(d.path+".Values", maxValues)
	case d.d.StorageResolution != nil && *d.d.StorageResolution != 1 && *d.d.StorageResolution != 60:
		return errInvalidValue(strconv.Itoa(int(*d.d.StorageResolution)), d.path+".StorageResolution")
	}
	for _, v := range numbers(d) {
		if math.IsNaN(v.v) || math.Abs(v.v) > valueBound {
			return errInvalidValue(strconv.FormatFloat(v.v, 'g', -1, 64), v.param)
		}
	}
	return nil
}

// number is one number of a datum with its parameter path.
type number struct {
	param string
	v     float64
}

// numbers lists every number a datum carries, each of which CloudWatch
// refuses when it is NaN, infinite or past 2^360 in magnitude.
func numbers(d datum) []number {
	var out []number
	if d.d.Value != nil {
		out = append(out, number{d.path + ".Value", *d.d.Value})
	}
	for i, v := range d.d.Values {
		out = append(out, number{d.path + ".Values.member." + strconv.Itoa(i+1), v})
	}
	for i, v := range d.d.Counts {
		out = append(out, number{d.path + ".Counts.member." + strconv.Itoa(i+1), v})
	}
	if s := d.d.StatisticValues; s != nil {
		p := d.path + ".StatisticValues."
		out = append(out, number{p + "SampleCount", s.SampleCount}, number{p + "Sum", s.Sum},
			number{p + "Minimum", s.Minimum}, number{p + "Maximum", s.Maximum})
	}
	return out
}

// checkGetMetricData checks the time range, a page size of at least one
// value, and that each query names a metric understudy can read: a
// MetricStat with a valid period and one of the five basic statistics,
// under an Id no other query uses.
func checkGetMetricData(in *cloudwatchapi.GetMetricDataInput) *awserr.Error {
	if !in.StartTime.Before(in.EndTime) {
		return cloudwatchapi.ErrInvalidParameterValueException("The parameter StartTime must be less than the parameter EndTime.") // recorded
	}
	// The model puts no range on MaxDatapoints; a page of no values could
	// never advance.
	if n := in.MaxDatapoints; n != nil && *n < 1 {
		return errInvalidValue(strconv.Itoa(int(*n)), "MaxDatapoints")
	}
	seen := make(map[string]bool, len(in.MetricDataQueries))
	for i := range in.MetricDataQueries {
		q := &in.MetricDataQueries[i]
		path := "MetricDataQueries.member." + strconv.Itoa(i+1)
		switch {
		case seen[q.Id]:
			return cloudwatchapi.ErrInvalidParameterValueException("The value " + q.Id + " for parameter " + path + ".Id is not unique.")
		case q.MetricStat != nil && q.Expression != nil:
			return errExclusive(path+".MetricStat", path+".Expression")
		case q.Expression != nil:
			return errExpressionNotImplemented(path)
		case q.MetricStat == nil:
			return cloudwatchapi.ErrMissingRequiredParameterException("One of the parameters " + path + ".MetricStat or " + path + ".Expression is required.")
		case !validQueryPeriod(q.MetricStat.Period):
			return errInvalidValue(strconv.Itoa(int(q.MetricStat.Period)), path+".MetricStat.Period")
		}
		if _, ok := statOf(q.MetricStat.Stat); !ok {
			return errStatNotImplemented(path, q.MetricStat.Stat)
		}
		seen[q.Id] = true
	}
	return nil
}
