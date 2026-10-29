package alarm

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
)

// Treatment is how an alarm treats missing data points
// (cloudwatch-alarms.md 3.5).
type Treatment uint8

// Treatment values; TreatMissing is the default.
const (
	TreatMissing Treatment = iota + 1
	TreatIgnore
	TreatBreaching
	TreatNotBreaching
)

// String returns the TreatMissingData wire value: "missing", "ignore",
// "breaching" or "notBreaching".
func (Treatment) String() string {
	// not implemented yet
	return ""
}

// ParseTreatment reads TreatMissingData s: absent is TreatMissing, a wire
// value its Treatment; ok is false for any other text.
func ParseTreatment(_ *string) (t Treatment, ok bool) {
	// not implemented yet
	return 0, false
}

// Comparison is one of the four static-threshold comparison operators
// (cloudwatch-alarms.md 3.3); the anomaly detection operators have none.
type Comparison uint8

// Comparison values.
const (
	GreaterThan Comparison = iota + 1
	GreaterThanOrEqual
	LessThan
	LessThanOrEqual
)

// ParseComparison reads a ComparisonOperator; ok is false for the three
// anomaly detection operators.
func ParseComparison(_ cloudwatchapi.ComparisonOperator) (c Comparison, ok bool) {
	// not implemented yet
	return 0, false
}

// Breaches reports whether v, the statistic as first operand, crosses
// threshold T: v > T, v >= T, v < T or v <= T (cloudwatch-alarms.md 3.3).
func (Comparison) Breaches(_, _ float64) bool {
	// not implemented yet
	return false
}

// Phrase is the operator in a StateReason: "greater than", "greater than
// or equal to", "less than" or "less than or equal to"
// (cloudwatch-alarms.md 4.1).
func (Comparison) Phrase() string {
	// not implemented yet
	return ""
}

// Config is what evaluation reads of a single-statistic alarm.
type Config struct {
	// Period is the alarm's Period in seconds: 10, 20, 30 or a multiple
	// of 60.
	Period int32
	// N is EvaluationPeriods, M DatapointsToAlarm (N when unset).
	N, M int
	// MSet reports whether DatapointsToAlarm was sent, which picks the
	// StateReason form (cloudwatch.alarm.reason-format).
	MSet       bool
	Comparison Comparison
	Threshold  float64
	// Treatment is the treatment evaluation applies: TreatIgnore for an
	// AWS/DynamoDB alarm whatever it sent
	// (cloudwatch.alarm.dynamodb-missing-data).
	Treatment Treatment
	// WarmUp is the alarm's WarmUpConfiguration; nil without one.
	WarmUp *WarmUp
	// Statistic and Unit are the alarm's, which StateReasonData names;
	// Unit is nil without one.
	Statistic cloudwatchapi.Statistic
	Unit      *cloudwatchapi.StandardUnit
}

// Range is R, the number of bins one evaluation reads: N + 2
// (cloudwatch.alarm.evaluation-range, cloudwatch-alarms.md 3.4).
func (Config) Range() int {
	// not implemented yet
	return 0
}

// NotEvaluableError is an alarm understudy stores and never evaluates
// (cloudwatch-alarms.md 12.1, 12.7). Why names the reason, such as
// "ExtendedStatistic" or "WallClockWindow"; the PutMetricAlarm Advisory
// carries it.
type NotEvaluableError struct {
	Alarm string
	Why   string
}

func (e *NotEvaluableError) Error() string {
	return "alarm " + e.Alarm + " is stored and never evaluated: " + e.Why
}

// ConfigOf reads an alarm's evaluation config, or a NotEvaluableError for
// an alarm on an ExtendedStatistic, Metrics, a ThresholdMetricId,
// EvaluationCriteria, a WallClockWindow or an anomaly detection operator.
func ConfigOf(in *cloudwatchapi.PutMetricAlarmInput) (Config, *NotEvaluableError) {
	// not implemented yet
	return Config{}, &NotEvaluableError{Alarm: in.AlarmName, Why: "evaluation is not implemented yet"}
}

// Bin is one period of an evaluation range. Bins are passed newest first:
// bins[0] is d1, covering [T0 - Period, T0) (cloudwatch-alarms.md 3.2).
//
//sumtype:decl
type Bin interface{ isBin() }

// Missing is a bin without data, or whose statistic has no value (the
// Average of zero samples).
type Missing struct {
	Start time.Time
}

// Value is a bin with data: the alarm's statistic over the bin and the
// bin's combined SampleCount.
type Value struct {
	Start       time.Time
	Value       float64
	SampleCount float64
}

func (Missing) isBin() {}
func (Value) isBin()   {}

// Outcome is what one evaluation decides.
//
//sumtype:decl
type Outcome interface{ isOutcome() }

// Transition changes the alarm's state to To. Evaluate returns it only
// when To differs from the current state (cloudwatch.alarm.unchanged-state).
type Transition struct {
	To       cloudwatchapi.StateValue
	Evidence Evidence
}

// Retain keeps the state: the algorithm says retain, or it computes the
// current state.
type Retain struct{}

func (Transition) isOutcome() {}
func (Retain) isOutcome()     {}

// Evidence is what a decision read, which the StateReason and
// StateReasonData builders render.
type Evidence struct {
	// Bins are the bins of the evaluation range, newest first.
	Bins []Bin
	// Used are the real bins the decision counted, newest first: the N
	// newest in case A of cloudwatch-alarms.md 3.5, every real bin of
	// d1..dR in case C, none in case B.
	Used []Value
	// Filled is how many missing data points the treatment filled: 0 in
	// case A, N - len(Used) otherwise.
	Filled int
	// Breaching is how many of Used breach.
	Breaching int
}

// Evaluate is the 3.5 algorithm over bins (newest first, len(bins) ==
// cfg.Range()) from the current state (cloudwatch-alarms.md 3.5, 3.6,
// 12.3).
func Evaluate(_ Config, _ []Bin, _ cloudwatchapi.StateValue) Outcome {
	// not implemented yet
	return nil
}

// WarmUp is WarmUpConfiguration (cloudwatch-alarms.md 2.4).
type WarmUp struct {
	Duration time.Duration
	// WaitFull is OnlyStartEvaluatingAfterWarmUpPeriodEnds.
	WaitFull bool
}

// Over reports whether warm-up has ended elapsed after the alarm's
// creation: with WaitFull once Duration has passed, otherwise at Duration
// or as soon as every one of the N newest bins holds a Value
// (cloudwatch.alarm.warm-up-end).
func (WarmUp) Over(_ time.Duration, _ []Bin, _ int) bool {
	// not implemented yet
	return false
}
