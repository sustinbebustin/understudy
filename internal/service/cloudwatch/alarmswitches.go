package cloudwatch

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// alarmResearch is the research note the alarm switches cite.
const alarmResearch = "docs/research/cloudwatch-alarms.md"

// alarmEvaluationOps are the operations whose observable alarm state an
// evaluation switch governs.
func alarmEvaluationOps() []string {
	return []string{"cloudwatch.PutMetricAlarm", "cloudwatch.DescribeAlarms"}
}

// alarmActionOps are the operations whose transitions run actions and send
// events: an evaluation (armed by PutMetricAlarm) or SetAlarmState.
func alarmActionOps() []string {
	return []string{"cloudwatch.PutMetricAlarm", "cloudwatch.SetAlarmState"}
}

// alarmSwitches are the rows of cloudwatch-alarms.md 12.2 that package
// alarm and the service implement: cadence, window, range, the
// missing-data algorithm, warm-up, the texts, the notification and event
// payloads, and what a refused action does (12.4).
func alarmSwitches() []fidelity.Entry {
	unrecorded := func(item string) fidelity.NoScenario {
		return fidelity.NoScenario{Reason: "unrecorded: " + item}
	}
	switches := []fidelity.Switch[string]{
		{
			Name: "cloudwatch.alarm.evaluation-cadence", Value: "minute,10s-for-high-resolution,hourly-past-one-day",
			Label: evidence.AWSDoc, Ref: alarmResearch + "#31-cadence-and-phase", Ops: alarmEvaluationOps(),
			Settles: fidelity.NoScenario{Reason: "AWS documents the cadence (alarm-evaluation); no recording observes the instant of an evaluation"},
		},
		{
			Name: "cloudwatch.alarm.evaluation-phase", Value: "minute-boundary",
			Label: evidence.Inferred, Ref: alarmResearch + "#31-cadence-and-phase", Ops: alarmEvaluationOps(),
			Settles: fidelity.NoScenario{Reason: "AWS evaluates at an arbitrary second, which does not change the data a minute's evaluation reads; no recording can pin it"},
		},
		{
			Name: "cloudwatch.alarm.first-evaluation", Value: "next-boundary",
			Label: evidence.Inferred, Ref: alarmResearch + "#31-cadence-and-phase", Record: "CWA-03", Ops: alarmEvaluationOps(),
			Settles: unrecorded("CWA-03"),
		},
		{
			Name: "cloudwatch.alarm.window-anchor", Value: "evaluation-minute",
			Label: evidence.AWSDoc, Ref: alarmResearch + "#32-the-window-bins-anchored-at-the-evaluation-minute", Record: "CWA-02", Ops: alarmEvaluationOps(),
			Settles: unrecorded("CWA-02 (sub-minute periods)"),
		},
		{
			Name: "cloudwatch.alarm.mixed-units", Value: "first-unit-by-name",
			Label: evidence.Inferred, Ref: alarmResearch + "#33-which-series-and-which-statistic", Ops: alarmEvaluationOps(),
			Settles: fidelity.NoScenario{Reason: "AWS documents an alarm on a metric of several units without Unit as undefined, so no recording can settle it"},
		},
		{
			Name: "cloudwatch.alarm.evaluation-range", Value: "n-plus-2",
			Label: evidence.Inferred, Ref: alarmResearch + "#34-evaluation-range-n-and-m", Record: "CWA-01", Ops: alarmEvaluationOps(),
			Settles: unrecorded("CWA-01"),
		},
		{
			Name: "cloudwatch.alarm.unaged-breach", Value: "retain",
			Label: evidence.Inferred, Ref: alarmResearch + "#35-missing-data-the-algorithm", Record: "CWA-04", Ops: alarmEvaluationOps(),
			Settles: unrecorded("CWA-04"),
		},
		{
			Name: "cloudwatch.alarm.unchanged-state", Value: "no-update",
			Label: evidence.Inferred, Ref: alarmResearch + "#38-what-a-transition-does-and-what-no-transition-does", Record: "CWA-05", Ops: alarmEvaluationOps(),
			Settles: unrecorded("CWA-05"),
		},
		{
			Name: "cloudwatch.alarm.dynamodb-missing-data", Value: "always-ignore",
			Label: evidence.AWSDoc, Ref: alarmResearch + "#37-special-cases", Record: "CWA-21", Ops: alarmEvaluationOps(),
			Settles: unrecorded("CWA-21"),
		},
		{
			Name: "cloudwatch.alarm.warm-up-end", Value: "every-period-has-data",
			Label: evidence.AWSDoc, Ref: alarmResearch + "#24-warm-up", Record: "CWA-22", Ops: alarmEvaluationOps(),
			Settles: unrecorded("CWA-22"),
		},
		{
			Name: "cloudwatch.alarm.reason-format", Value: "count-form-unless-datapoints-to-alarm",
			Label: evidence.Inferred, Ref: alarmResearch + "#41-statereason-texts", Record: "CWA-06, CWA-07, CWA-08", Ops: alarmEvaluationOps(),
			Settles: unrecorded("CWA-06, CWA-07, CWA-08"),
		},
		{
			Name: "cloudwatch.alarm.number-format", Value: "java-double,event-configuration-plain-whole-numbers",
			Label: evidence.Inferred, Ref: alarmResearch + "#43-formatting", Record: "CWA-09, CWA-15",
			Ops:     append(alarmEvaluationOps(), "cloudwatch.DescribeAlarmHistory"),
			Settles: unrecorded("CWA-09, CWA-15"),
		},
		{
			Name: "cloudwatch.alarm.sns.unset-fields", Value: "description-null,treat-missing-data-empty,unit-null,low-sample-empty,datapoints-to-alarm-omitted,mute-window-false",
			Label: evidence.Inferred, Ref: alarmResearch + "#51-message-body", Record: "CWA-11", Ops: alarmActionOps(),
			Settles: unrecorded("CWA-11"),
		},
		{
			Name: "cloudwatch.alarm.sns.subject", Value: "state-quoted-name-in-region-long-name,100-chars",
			Label: evidence.Inferred, Ref: alarmResearch + "#52-subject-and-region", Record: "CWA-12", Ops: alarmActionOps(),
			Settles: unrecorded("CWA-12"),
		},
		{
			Name: "cloudwatch.alarm.sns.fifo-group", Value: "sha256-hex-of-alarm-arn",
			Label: evidence.Inferred, Ref: alarmResearch + "#53-delivery", Record: "CWA-13", Ops: alarmActionOps(),
			Settles: unrecorded("CWA-13"),
		},
		{
			Name: "cloudwatch.alarm.events.actions-disabled", Value: "events-sent",
			Label: evidence.Inferred, Ref: alarmResearch + "#62-eventbridge-events", Record: "CWA-14",
			Ops:     append(alarmActionOps(), "cloudwatch.EnableAlarmActions", "cloudwatch.DisableAlarmActions"),
			Settles: unrecorded("CWA-14"),
		},
		{
			Name: "cloudwatch.alarm.events.metric-id", Value: "one-per-configuration",
			Label: evidence.Inferred, Ref: alarmResearch + "#62-eventbridge-events", Record: "CWA-15",
			Ops:     append(alarmActionOps(), "cloudwatch.DeleteAlarms"),
			Settles: unrecorded("CWA-15"),
		},
		{
			Name: "cloudwatch.alarm.actions.denied", Value: "dropped",
			Label: evidence.Inferred, Ref: alarmResearch + "#124-actions-and-delivery", Record: "CWA-23", Ops: alarmActionOps(),
			Settles: unrecorded("CWA-23"),
		},
		{
			Name: "cloudwatch.alarm.history.page-size", Value: "50",
			Label: evidence.Inferred, Ref: alarmResearch + "#81-describealarmhistory", Record: "CWA-18",
			Ops:     []string{"cloudwatch.DescribeAlarmHistory"},
			Settles: unrecorded("CWA-18"),
		},
	}
	entries := make([]fidelity.Entry, len(switches))
	for i, s := range switches {
		entries[i] = s.Entry()
	}
	return entries
}
