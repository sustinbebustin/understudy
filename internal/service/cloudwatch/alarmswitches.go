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

// alarmSwitches are the rows of cloudwatch-alarms.md 12.2 that the pure
// evaluator in package alarm implements: cadence, window, range, the
// missing-data algorithm, warm-up and the operator phrases.
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
	}
	entries := make([]fidelity.Entry, len(switches))
	for i, s := range switches {
		entries[i] = s.Entry()
	}
	return entries
}
