package cloudwatch

import (
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The resource and paging errors. Texts are AWS-recorded where a comment
// says so and inferred otherwise; the validation texts are in validate.go.
// Keeping them here means a recording changes one place.

// errAlarmNotFound is SetAlarmState's answer for a missing alarm:
// ResourceNotFound without a message (AWS-recorded, error-matrix.md 5.13).
func errAlarmNotFound() *awserr.Error { return cloudwatchapi.ErrResourceNotFound("") }

// errTagTargetNotFound answers a tag operation on an ARN that names no
// alarm (inferred text).
func errTagTargetNotFound(arn string) *awserr.Error {
	return cloudwatchapi.ErrResourceNotFoundException("The resource " + arn + " does not exist.")
}

// errTooManyTags is the 50-tag quota of a resource (inferred text).
func errTooManyTags() *awserr.Error {
	return cloudwatchapi.ErrInvalidParameterValueException("A resource can have no more than " + strconv.Itoa(maxTags) + " tags.")
}

// errInvalidFormat answers a SetAlarmState StateReasonData that is not
// JSON (inferred text: the InvalidFormatFault documentation in the model).
func errInvalidFormat() *awserr.Error {
	return cloudwatchapi.ErrInvalidFormatFault("Data was not syntactically valid JSON")
}

// errInvalidNextToken answers a DescribeAlarms or GetMetricData token this
// service did not issue (inferred text).
func errInvalidNextToken() *awserr.Error {
	return cloudwatchapi.ErrInvalidNextToken("The specified NextToken is invalid.")
}

// errInvalidListToken is ListMetrics' answer for such a token: its model
// lists InvalidParameterValue, not InvalidNextToken (inferred text).
func errInvalidListToken() *awserr.Error {
	return cloudwatchapi.ErrInvalidParameterValueException("The specified NextToken is invalid.")
}

// errExpressionNotImplemented refuses a metric math or Metrics Insights
// query, which GetMetricData supports and understudy does not.
func errExpressionNotImplemented(path string) *awserr.Error {
	return cloudwatchapi.ErrNotImplemented("understudy does not implement metric math or Metrics Insights expressions (" + path +
		".Expression); query stored metrics with MetricStat instead. Run 'understudy coverage --service cloudwatch' for what is supported.")
}

// errStatNotImplemented refuses a statistic GetMetricData supports and
// understudy does not: percentiles, trimmed and windowed statistics.
func errStatNotImplemented(path, stat string) *awserr.Error {
	return cloudwatchapi.ErrNotImplemented("understudy does not implement the statistic " + stat + " (" + path + ".MetricStat.Stat); it implements " +
		strings.Join(statNames(), ", ") + ". Run 'understudy coverage --service cloudwatch' for what is supported.")
}
