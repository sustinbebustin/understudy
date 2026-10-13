package cloudwatch

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
// needs and nothing it excludes (secrets-logs-events-alarms.md 5.1). The
// name is UTF-8 without ASCII control characters (the PutMetricAlarm
// documentation; the model has no pattern for it).
func checkPutMetricAlarm(in *cloudwatchapi.PutMetricAlarmInput) *awserr.Error {
	if !utf8.ValidString(in.AlarmName) || strings.ContainsFunc(in.AlarmName, asciiControl) {
		return errInvalidValue(in.AlarmName, "AlarmName")
	}
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
		err = checkPromQLAlarm(in)
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

// checkStaticAlarm needs a valid statistic, a valid period, the
// evaluation members with a static threshold and an evaluation range
// within AWS's bound. EvaluationInterval belongs to PromQL alarms and
// ThresholdMetricId to the Metrics array, so neither is accepted.
func checkStaticAlarm(in *cloudwatchapi.PutMetricAlarmInput) *awserr.Error {
	switch {
	case in.Statistic == nil && in.ExtendedStatistic == nil:
		return cloudwatchapi.ErrMissingRequiredParameterException("One of the parameters Statistic or ExtendedStatistic is required.")
	case in.ExtendedStatistic != nil && !validExtendedStat(*in.ExtendedStatistic):
		return errInvalidValue(*in.ExtendedStatistic, "ExtendedStatistic")
	case in.Period == nil:
		return errMissing("Period")
	case !validPeriod(*in.Period):
		return cloudwatchapi.ErrInvalidParameterValueException("The parameter Period must be 10, 20, 30 or a multiple of 60.")
	}
	if err := checkDimensions("Dimensions", in.Dimensions); err != nil {
		return err
	}
	if err := refuseSet("MetricName", []member{
		{"EvaluationInterval", in.EvaluationInterval != nil},
		{"ThresholdMetricId", in.ThresholdMetricId != nil},
	}); err != nil {
		return err
	}
	if op := in.ComparisonOperator; op != nil && anomalyOperator(*op) {
		return errAnomalyOperator(*op)
	}
	if err := checkEvaluation(in); err != nil {
		return err
	}
	return checkEvaluationRange(*in.Period, *in.EvaluationPeriods)
}

// checkMetricsAlarm refuses the single-metric members, which the Metrics
// array replaces, and EvaluationInterval, which belongs to PromQL alarms;
// it needs the evaluation members, a Metrics array within AWS's rules and
// a ThresholdMetricId that names one of its entries.
func checkMetricsAlarm(in *cloudwatchapi.PutMetricAlarmInput) *awserr.Error {
	if err := refuseSet("Metrics", []member{
		{"Namespace", in.Namespace != nil},
		{"Dimensions", len(in.Dimensions) > 0},
		{"Period", in.Period != nil},
		{"Unit", in.Unit != nil},
		{"Statistic", in.Statistic != nil},
		{"ExtendedStatistic", in.ExtendedStatistic != nil},
		{"EvaluationInterval", in.EvaluationInterval != nil},
	}); err != nil {
		return err
	}
	if err := checkEvaluation(in); err != nil {
		return err
	}
	if err := checkAlarmQueries(in.Metrics, anomalyOperator(*in.ComparisonOperator)); err != nil {
		return err
	}
	if id := in.ThresholdMetricId; id != nil && !slices.ContainsFunc(in.Metrics, func(q cloudwatchapi.MetricDataQuery) bool { return q.Id == *id }) {
		return errInvalidValue(*id, "ThresholdMetricId") // inferred
	}
	if period, ok := evaluatedPeriod(in.Metrics); ok {
		return checkEvaluationRange(period, *in.EvaluationPeriods)
	}
	return nil
}

// evaluatedPeriod is the period of a Metrics array's first entry that
// returns data and has a period of its own: a MetricStat's, or an
// Expression's Period. ok is false when none has one, as for an expression
// whose period comes from the metrics it reads. AWS bounds every alarm's
// evaluation range, so the bound holds here as for a static alarm
// (inferred: no recording covers a Metrics alarm).
func evaluatedPeriod(qs []cloudwatchapi.MetricDataQuery) (int32, bool) {
	for _, q := range qs {
		switch {
		case q.ReturnData != nil && !*q.ReturnData:
		case q.MetricStat != nil:
			return q.MetricStat.Period, true
		case q.Period != nil:
			return *q.Period, true
		}
	}
	return 0, false
}

// checkPromQLAlarm refuses every member EvaluationCriteria replaces and
// needs EvaluationInterval, which takes the values Period does
// (AWS-documented; the texts are inferred).
func checkPromQLAlarm(in *cloudwatchapi.PutMetricAlarmInput) *awserr.Error {
	if err := refuseSet("EvaluationCriteria", []member{
		{"Namespace", in.Namespace != nil},
		{"Dimensions", len(in.Dimensions) > 0},
		{"Period", in.Period != nil},
		{"Unit", in.Unit != nil},
		{"Statistic", in.Statistic != nil},
		{"ExtendedStatistic", in.ExtendedStatistic != nil},
		{"Threshold", in.Threshold != nil},
		{"ComparisonOperator", in.ComparisonOperator != nil},
		{"ThresholdMetricId", in.ThresholdMetricId != nil},
		{"EvaluationPeriods", in.EvaluationPeriods != nil},
		{"DatapointsToAlarm", in.DatapointsToAlarm != nil},
		{"EvaluationWindow", in.EvaluationWindow != nil},
	}); err != nil {
		return err
	}
	switch v := in.EvaluationInterval; {
	case v == nil:
		return errMissing("EvaluationInterval")
	case !validPeriod(*v):
		return errInvalidValue(strconv.Itoa(int(*v)), "EvaluationInterval")
	}
	return nil
}

// member is one request member and whether a request sets it.
type member struct {
	name string
	set  bool
}

// refuseSet refuses the first of members a request sets, each of which
// the member named owner excludes.
func refuseSet(owner string, members []member) *awserr.Error {
	for _, m := range members {
		if m.set {
			return errExclusive(owner, m.name)
		}
	}
	return nil
}

// checkEvaluation needs what a metric alarm compares, the number of
// periods and the operator; refuses more DatapointsToAlarm than
// EvaluationPeriods, as an M out of N alarm counts M breaching data points
// within the last N; and ties the threshold to the operator: an anomaly
// detection operator compares with the band ThresholdMetricId names and
// takes no Threshold, and every other operator needs a Threshold
// (AWS-documented; the texts are inferred).
func checkEvaluation(in *cloudwatchapi.PutMetricAlarmInput) *awserr.Error {
	switch {
	case in.EvaluationPeriods == nil:
		return errMissing("EvaluationPeriods")
	case in.ComparisonOperator == nil:
		return errMissing("ComparisonOperator")
	case in.DatapointsToAlarm != nil && *in.DatapointsToAlarm > *in.EvaluationPeriods:
		return cloudwatchapi.ErrInvalidParameterCombinationException("DatapointsToAlarm must be less than or equal to EvaluationPeriods.")
	}
	anomaly := anomalyOperator(*in.ComparisonOperator)
	switch {
	case !anomaly && in.Threshold == nil:
		return errMissing("Threshold")
	case anomaly && in.ThresholdMetricId == nil:
		return errMissing("ThresholdMetricId")
	case anomaly && in.Threshold != nil:
		return errExclusive("ThresholdMetricId", "Threshold")
	}
	return nil
}

// anomalyOperator reports the three operators AWS uses only for alarms on
// an anomaly detection band.
func anomalyOperator(op cloudwatchapi.ComparisonOperator) bool {
	switch op {
	case cloudwatchapi.ComparisonOperatorLessThanLowerOrGreaterThanUpperThreshold,
		cloudwatchapi.ComparisonOperatorLessThanLowerThreshold,
		cloudwatchapi.ComparisonOperatorGreaterThanUpperThreshold:
		return true
	case cloudwatchapi.ComparisonOperatorGreaterThanOrEqualToThreshold,
		cloudwatchapi.ComparisonOperatorGreaterThanThreshold,
		cloudwatchapi.ComparisonOperatorLessThanThreshold,
		cloudwatchapi.ComparisonOperatorLessThanOrEqualToThreshold:
		return false
	}
	return false
}

// errAnomalyOperator refuses an anomaly detection operator on an alarm
// with a static threshold (inferred text).
func errAnomalyOperator(op cloudwatchapi.ComparisonOperator) *awserr.Error {
	return cloudwatchapi.ErrInvalidParameterCombinationException("The ComparisonOperator " + string(op) +
		" is valid only for alarms based on anomaly detection models, which use the Metrics array and ThresholdMetricId.")
}

// Limits of a PutMetricAlarm Metrics array and of GetMetricData's queries
// (the MetricDataQuery and GetMetricData documentation).
const (
	maxAlarmQueries     = 20
	maxAlarmMetricStats = 10
	maxAlarmExpressions = 10
	maxQueries          = 500
)

// checkAlarmQueries checks an alarm's Metrics array: at most 20 entries,
// of which at most 10 MetricStat and 10 Expression entries, each with a
// unique valid Id, one of the two forms, and a valid period and statistic
// for a MetricStat. Exactly one entry returns data, an absent ReturnData
// counting as true as it does in GetMetricData, unless the alarm watches
// an anomaly detection band, whose metric and band both return data (the
// documentation's anomaly example). The texts are inferred.
func checkAlarmQueries(qs []cloudwatchapi.MetricDataQuery, band bool) *awserr.Error {
	if len(qs) > maxAlarmQueries {
		return errCollectionSize("Metrics", maxAlarmQueries)
	}
	seen := make(map[string]bool, len(qs))
	metricStats, expressions, returned := 0, 0, 0
	for i := range qs {
		q := &qs[i]
		path := "Metrics.member." + strconv.Itoa(i+1)
		if err := checkQueryID(q.Id, path, seen); err != nil {
			return err
		}
		if err := checkQueryForm(q, path); err != nil {
			return err
		}
		if q.ReturnData == nil || *q.ReturnData {
			returned++
		}
		if q.Expression != nil {
			expressions++
			continue
		}
		metricStats++
		switch ms := q.MetricStat; {
		case !validPeriod(ms.Period):
			return errInvalidValue(strconv.Itoa(int(ms.Period)), path+".MetricStat.Period")
		case !validStat(ms.Stat):
			return errInvalidValue(ms.Stat, path+".MetricStat.Stat")
		}
		if err := checkDimensions(path+".MetricStat.Metric.Dimensions", q.MetricStat.Metric.Dimensions); err != nil {
			return err
		}
	}
	switch {
	case metricStats > maxAlarmMetricStats:
		return cloudwatchapi.ErrInvalidParameterValueException("The Metrics array can contain at most " + strconv.Itoa(maxAlarmMetricStats) + " entries with a MetricStat.")
	case expressions > maxAlarmExpressions:
		return cloudwatchapi.ErrInvalidParameterValueException("The Metrics array can contain at most " + strconv.Itoa(maxAlarmExpressions) + " entries with an Expression.")
	case !band && returned != 1:
		return cloudwatchapi.ErrInvalidParameterValueException("Exactly one entry of the Metrics array must have ReturnData set to true; " + strconv.Itoa(returned) + " do.")
	}
	return nil
}

// queryID is the form of a MetricDataQuery Id: letters, digits and
// underscores, starting with a lowercase letter (AWS-documented).
var queryID = regexp.MustCompile(`^[a-z][A-Za-z0-9_]*$`)

// checkQueryID refuses an Id of the wrong form (inferred text) or one an
// earlier query of the request used, and records it in seen.
func checkQueryID(id, path string, seen map[string]bool) *awserr.Error {
	switch {
	case !queryID.MatchString(id):
		return errInvalidValue(id, path+".Id")
	case seen[id]:
		return cloudwatchapi.ErrInvalidParameterValueException("The value " + id + " for parameter " + path + ".Id is not unique.")
	}
	seen[id] = true
	return nil
}

// checkQueryForm needs exactly one of a query's MetricStat and Expression.
func checkQueryForm(q *cloudwatchapi.MetricDataQuery, path string) *awserr.Error {
	switch {
	case q.MetricStat != nil && q.Expression != nil:
		return errExclusive(path+".MetricStat", path+".Expression")
	case q.MetricStat == nil && q.Expression == nil:
		return cloudwatchapi.ErrMissingRequiredParameterException("One of the parameters " + path + ".MetricStat or " + path + ".Expression is required.")
	}
	return nil
}

// validStat reports a statistic name AWS accepts: one of the basic five
// or an extended statistic.
func validStat(name string) bool {
	_, basic := statOf(name)
	return basic || validExtendedStat(name)
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

// checkDescribeAlarms refuses AlarmNames with AlarmNamePrefix, and
// ChildrenOfAlarmName or ParentsOfAlarmName with any member but MaxRecords
// and NextToken (the DescribeAlarms documentation; the texts are inferred).
func checkDescribeAlarms(in *cloudwatchapi.DescribeAlarmsInput) *awserr.Error {
	if len(in.AlarmNames) > 0 && in.AlarmNamePrefix != nil {
		return errExclusive("AlarmNames", "AlarmNamePrefix")
	}
	filters := []member{
		{"AlarmNames", len(in.AlarmNames) > 0},
		{"AlarmNamePrefix", in.AlarmNamePrefix != nil},
		{"AlarmTypes", len(in.AlarmTypes) > 0},
		{"StateValue", in.StateValue != nil},
		{"ActionPrefix", in.ActionPrefix != nil},
	}
	switch {
	case in.ChildrenOfAlarmName != nil:
		return refuseSet("ChildrenOfAlarmName", append(filters, member{"ParentsOfAlarmName", in.ParentsOfAlarmName != nil}))
	case in.ParentsOfAlarmName != nil:
		return refuseSet("ParentsOfAlarmName", filters)
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

// asciiControl reports an ASCII control character: below space, or DEL.
func asciiControl(r rune) bool { return r < ' ' || r == 0x7f }

// unprintable reports a rune outside printable ASCII: past ASCII, or an
// ASCII control character. An invalid UTF-8 byte reads as U+FFFD, so it
// counts too.
func unprintable(r rune) bool { return r > unicode.MaxASCII || asciiControl(r) }

// checkDimensions refuses a dimension name or value with a character
// outside printable ASCII or without a non-space character, and a name
// starting with a colon (the Dimension documentation; the text is
// inferred). list is the Query parameter path of the Dimensions list.
func checkDimensions(list string, dims []cloudwatchapi.Dimension) *awserr.Error {
	for i, d := range dims {
		path := list + ".member." + strconv.Itoa(i+1)
		switch {
		case !dimensionText(d.Name) || strings.HasPrefix(d.Name, ":"):
			return errInvalidValue(d.Name, path+".Name")
		case !dimensionText(d.Value):
			return errInvalidValue(d.Value, path+".Value")
		}
	}
	return nil
}

// dimensionText reports text a dimension name or value may hold: printable
// ASCII with at least one character that is not a space.
func dimensionText(s string) bool {
	return !strings.ContainsFunc(s, unprintable) && strings.ContainsFunc(s, func(r rune) bool { return r != ' ' })
}

// errCollectionSize is a list past its limit (recorded template).
func errCollectionSize(param string, limit int) *awserr.Error {
	return cloudwatchapi.ErrInvalidParameterValueException("The collection " + param + " must not have a size greater than " + strconv.Itoa(limit) + ".")
}

// checkPutMetricData runs the request rules that need no state: the
// reserved namespace and the namespace's printable ASCII characters (the
// PutMetricData documentation; the model's pattern allows any), the entity
// and metric counts, and per datum the value forms and their bounds. The
// timestamp windows read the clock, so the handler checks them.
//
// StrictEntityValidation is required with EntityMetricData, and an entity
// error refuses the request only when it is true; otherwise AWS ingests
// the metric data and reports the error in a response header, which
// understudy does not send (AWS-documented; the texts are inferred).
func checkPutMetricData(in *cloudwatchapi.PutMetricDataInput) *awserr.Error {
	if strings.HasPrefix(in.Namespace, "AWS/") || strings.ContainsFunc(in.Namespace, unprintable) {
		return errInvalidValue(in.Namespace, "Namespace")
	}
	switch strict := in.StrictEntityValidation; {
	case len(in.EntityMetricData) > 0 && strict == nil:
		return errMissing("StrictEntityValidation")
	case len(in.EntityMetricData) > maxEntities && *strict:
		return errCollectionSize("EntityMetricData", maxEntities)
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

// checkDatum checks one datum's value forms and numbers. Values with
// StatisticValues is accepted, as AWS accepts it (AWS-recorded:
// LocalStack's test_put_metric_data_validation).
func checkDatum(d datum) *awserr.Error {
	value, values, stats := d.d.Value != nil, len(d.d.Values) > 0, d.d.StatisticValues != nil
	switch {
	case value && values:
		return errExclusive(d.path+".Value", d.path+".Values") // recorded
	case value && stats:
		return errExclusive(d.path+".Value", d.path+".StatisticValues") // recorded
	case len(d.d.Counts) > 0 && len(d.d.Counts) != len(d.d.Values):
		return cloudwatchapi.ErrInvalidParameterValueException("The parameters " + d.path + ".Values and " + d.path + ".Counts must be of the same size.") // recorded
	case !value && !values && !stats:
		return cloudwatchapi.ErrMissingRequiredParameterException("One of the parameters " + d.path + ".Value, " + d.path + ".Values or " + d.path + ".StatisticValues is required.")
	case len(d.d.Values) > maxValues:
		return errCollectionSize(d.path+".Values", maxValues)
	case d.d.StorageResolution != nil && *d.d.StorageResolution != 1 && *d.d.StorageResolution != 60:
		return errInvalidValue(strconv.Itoa(int(*d.d.StorageResolution)), d.path+".StorageResolution")
	}
	if err := checkDimensions(d.path+".Dimensions", d.d.Dimensions); err != nil {
		return err
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
// value, at most 500 queries, and that each query names a metric
// understudy can read: a MetricStat with a valid period and one of the
// five basic statistics, under a valid Id no other query uses. A valid
// statistic beyond the five answers 501; any other name is invalid.
func checkGetMetricData(in *cloudwatchapi.GetMetricDataInput) *awserr.Error {
	if !in.StartTime.Before(in.EndTime) {
		return cloudwatchapi.ErrInvalidParameterValueException("The parameter StartTime must be less than the parameter EndTime.") // recorded
	}
	// The model puts no range on MaxDatapoints; a page of no values could
	// never advance.
	if n := in.MaxDatapoints; n != nil && *n < 1 {
		return errInvalidValue(strconv.Itoa(int(*n)), "MaxDatapoints")
	}
	if len(in.MetricDataQueries) > maxQueries {
		return errCollectionSize("MetricDataQueries", maxQueries)
	}
	seen := make(map[string]bool, len(in.MetricDataQueries))
	for i := range in.MetricDataQueries {
		q := &in.MetricDataQueries[i]
		path := "MetricDataQueries.member." + strconv.Itoa(i+1)
		if err := checkQueryID(q.Id, path, seen); err != nil {
			return err
		}
		if err := checkQueryForm(q, path); err != nil {
			return err
		}
		if q.Expression != nil {
			return errExpressionNotImplemented(path)
		}
		ms := q.MetricStat
		if !validQueryPeriod(ms.Period) {
			return errInvalidValue(strconv.Itoa(int(ms.Period)), path+".MetricStat.Period")
		}
		if err := checkDimensions(path+".MetricStat.Metric.Dimensions", ms.Metric.Dimensions); err != nil {
			return err
		}
		if _, ok := statOf(ms.Stat); !ok {
			if validExtendedStat(ms.Stat) {
				return errStatNotImplemented(path, ms.Stat)
			}
			return errInvalidValue(ms.Stat, path+".MetricStat.Stat")
		}
	}
	return nil
}
