package alarm

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// ActionOutcome is how an alarm's SNS action ended, which its Action
// history item records (cloudwatch-alarms.md 8.3).
//
//sumtype:decl
type ActionOutcome interface{ isActionOutcome() }

// ActionSucceeded is a notification the topic accepted; Published is the
// message CloudWatch published (PublishedMessage).
type ActionSucceeded struct{ Published string }

// ActionFailed is a notification that was not published; Error says why,
// "Topic does not exist" for a missing topic or ActionDenied's text for a
// topic whose policy refuses CloudWatch (cloudwatch-alarms.md 5.3, 8.3).
type ActionFailed struct{ Error string }

func (ActionSucceeded) isActionOutcome() {}
func (ActionFailed) isActionOutcome()    {}

// ActionDenied is the error of an Action item whose topic's policy does not
// grant cloudwatch.amazonaws.com SNS:Publish, with no space after
// "resource:" as AWS writes it (cloudwatch-alarms.md 5.3; CWA-23).
func ActionDenied(topic string) string {
	return "CloudWatch Alarms is not authorized to perform: SNS:Publish on resource:" + topic
}

// ActionHistory is the HistorySummary and HistoryData of the Action item of
// an SNS action to topic run on the state change at stateUpdated:
// "Successfully executed action <topic>" or "Failed to execute action
// <topic>", with {"actionState","stateUpdateTimestamp" (epoch
// milliseconds),"notificationResource","publishedMessage","error",
// "muteWindowEndTimestamp"}, the published message on success and the
// error on failure, the other null (cloudwatch-alarms.md 8.3; CWA-11,
// CWA-13, CWA-16, CWA-23). muteWindowEndTimestamp is always null: mute
// rules are unbound. Like a ConfigurationUpdate item it is not capped at
// MaxHistoryData; how AWS fits a longer one is unrecorded.
func ActionHistory(topic string, stateUpdated time.Time, o ActionOutcome) (summary, data string) {
	b := []byte(`{"actionState":`)
	var published, failure []byte
	switch o := o.(type) {
	case ActionSucceeded:
		summary = "Successfully executed action " + topic
		b = append(b, `"Succeeded"`...)
		published, failure = jsonx.AppendString(nil, o.Published), []byte("null")
	case ActionFailed:
		summary = "Failed to execute action " + topic
		b = append(b, `"Failed"`...)
		published, failure = []byte("null"), jsonx.AppendString(nil, o.Error)
	}
	b = append(b, `,"stateUpdateTimestamp":`...)
	b = strconv.AppendInt(b, stateUpdated.UnixMilli(), 10)
	b = append(b, `,"notificationResource":`...)
	b = jsonx.AppendString(b, topic)
	b = append(b, `,"publishedMessage":`...)
	b = append(b, published...)
	b = append(b, `,"error":`...)
	b = append(b, failure...)
	b = append(b, `,"muteWindowEndTimestamp":null}`...)
	return summary, string(b)
}

// PublishedMessage is the message an alarm's SNS action publishes for the
// change ch, as its Action item records it: the JSON message structure
// {"default","sms","email"}, the notification body (SNSMessage), the
// Subject (SNSSubject) and the email text (cloudwatch-alarms.md 8.3; CWA-11,
// CWA-13, CWA-16). The sms text equals the Subject in every recording; for
// a name long enough to cut the Subject that is inferred. understudy
// publishes the body alone, which is what every subscription it delivers
// to receives: the sms and email texts reach no subscriber here.
func PublishedMessage(s Subject, ch Change) string {
	b := []byte(`{"default":`)
	b = jsonx.AppendString(b, SNSMessage(s, ch))
	b = append(b, `,"sms":`...)
	b = jsonx.AppendString(b, SNSSubject(s, ch.New.Value))
	b = append(b, `,"email":`...)
	b = jsonx.AppendString(b, emailText(s, ch))
	return string(append(b, '}'))
}

// emailText is the email of a notification (CWA-11, CWA-13, CWA-16): the
// recordings fix its lines for a single-metric alarm with a standard
// statistic, no description, no dimensions, no unit and no
// DatapointsToAlarm, with and without TreatMissingData, whose line is
// left empty when unset. The rest is inferred and UNVERIFIED: a
// description as given; dimensions as [name = value, ...] sorted by name,
// as the Trigger sorts them; an ExtendedStatistic as given; a unit as
// given; "at least M" with M the DatapointsToAlarm; several actions as
// [a, b]; the console link for every Region in the aws partition's form,
// the alarm name in it unescaped. An alarm on Metrics prints its metric
// lines empty, a form no recording shows.
func emailText(s Subject, ch Change) string {
	c := s.Config
	region, code := regionLabel(s.ARN.Region), s.ARN.Region
	at := ch.New.Timestamp.UTC().Format("Monday 02 January, 2006 15:04:05 UTC")
	n := smithy.Deref(c.EvaluationPeriods)
	m := n
	if c.DatapointsToAlarm != nil {
		m = *c.DatapointsToAlarm
	}
	period := strconv.Itoa(int(smithy.Deref(c.Period)))
	threshold := ""
	if c.Threshold != nil {
		threshold = FormatNumber(*c.Threshold)
	}
	statistic := smithy.Deref(c.ExtendedStatistic)
	if c.Statistic != nil {
		statistic = string(*c.Statistic)
	}
	unit := "not specified"
	if c.Unit != nil {
		unit = string(*c.Unit)
	}
	treatMissing := ""
	if c.TreatMissingData != nil {
		treatMissing = metricLine("TreatMissingData", *c.TreatMissingData)
	}
	var b strings.Builder
	b.WriteString(`You are receiving this email because your Amazon CloudWatch Alarm "` + c.AlarmName + `" in the ` + region +
		` region has entered the ` + string(ch.New.Value) + ` state, because "` + ch.New.Reason + `" at "` + at + "\".\n\n")
	b.WriteString("View this alarm in the AWS Management Console:\n")
	b.WriteString("https://" + code + ".console.aws.amazon.com/cloudwatch/deeplink.js?region=" + code + "#alarmsV2:alarm/" + c.AlarmName + "\n\n")
	b.WriteString("Alarm Details:\n")
	b.WriteString(detailLine("Name", c.AlarmName))
	b.WriteString(detailLine("Description", smithy.Deref(c.AlarmDescription)))
	b.WriteString(detailLine("State Change", string(ch.Old.Value)+" -> "+string(ch.New.Value)))
	b.WriteString(detailLine("Reason for State Change", ch.New.Reason))
	b.WriteString(detailLine("Timestamp", at))
	b.WriteString(detailLine("AWS Account", s.ARN.Account))
	b.WriteString(detailLine("Alarm Arn", s.ARN.String()))
	b.WriteString("\nThreshold:\n")
	b.WriteString("- The alarm is in the ALARM state when the metric is " + string(smithy.Deref(c.ComparisonOperator)) + " " + threshold +
		" for at least " + strconv.Itoa(int(m)) + " of the last " + strconv.Itoa(int(n)) + " period(s) of " + period + " seconds. \n\n")
	b.WriteString("Monitored Metric:\n")
	b.WriteString(metricLine("MetricNamespace", smithy.Deref(c.Namespace)) + "\n")
	b.WriteString(metricLine("MetricName", smithy.Deref(c.MetricName)) + "\n")
	b.WriteString(metricLine("Dimensions", emailDimensions(c.Dimensions)) + "\n")
	b.WriteString(metricLine("Period", period+" seconds") + "\n")
	b.WriteString(metricLine("Statistic", statistic) + "\n")
	b.WriteString(metricLine("Unit", unit) + "\n")
	b.WriteString(treatMissing + "\n\n\n")
	b.WriteString("State Change Actions:\n")
	b.WriteString("- OK: " + emailList(c.OKActions) + "\n")
	b.WriteString("- ALARM: " + emailList(c.AlarmActions) + "\n")
	b.WriteString("- INSUFFICIENT_DATA: " + emailList(c.InsufficientDataActions) + "\n")
	return b.String()
}

// detailLine is an "Alarm Details" line of the email, its value at column
// 31, ending in a newline.
func detailLine(label, value string) string { return padded("- "+label+":", 30) + value + "\n" }

// metricLine is a "Monitored Metric" line of the email, its value at
// column 40, without its newline.
func metricLine(label, value string) string { return padded("- "+label+":", 39) + value }

// padded is s followed by spaces up to width bytes.
func padded(s string, width int) string { return s + strings.Repeat(" ", max(width-len(s), 0)) }

// emailList is an action list as the email prints it: empty, or
// [a, b] (inferred for more than one).
func emailList(arns []string) string {
	if len(arns) == 0 {
		return ""
	}
	return "[" + strings.Join(arns, ", ") + "]"
}

// emailDimensions is the email's Dimensions value: empty, or
// [name = value, ...] sorted by name (inferred).
func emailDimensions(dims []cloudwatchapi.Dimension) string {
	if len(dims) == 0 {
		return ""
	}
	sorted := slices.SortedStableFunc(slices.Values(dims), func(x, y cloudwatchapi.Dimension) int { return strings.Compare(x.Name, y.Name) })
	parts := make([]string, len(sorted))
	for i, d := range sorted {
		parts[i] = d.Name + " = " + d.Value
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
