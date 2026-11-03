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
func (t Treatment) String() string {
	switch t {
	case TreatMissing:
		return "missing"
	case TreatIgnore:
		return "ignore"
	case TreatBreaching:
		return "breaching"
	case TreatNotBreaching:
		return "notBreaching"
	}
	return ""
}

// ParseTreatment reads TreatMissingData s: absent is TreatMissing, a wire
// value its Treatment; ok is false for any other text.
func ParseTreatment(s *string) (t Treatment, ok bool) {
	if s == nil {
		return TreatMissing, true // "The default behavior is missing" (cloudwatch-alarms.md 3.5)
	}
	for _, t := range []Treatment{TreatMissing, TreatIgnore, TreatBreaching, TreatNotBreaching} {
		if *s == t.String() {
			return t, true
		}
	}
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
func ParseComparison(op cloudwatchapi.ComparisonOperator) (c Comparison, ok bool) {
	switch op {
	case cloudwatchapi.ComparisonOperatorGreaterThanThreshold:
		return GreaterThan, true
	case cloudwatchapi.ComparisonOperatorGreaterThanOrEqualToThreshold:
		return GreaterThanOrEqual, true
	case cloudwatchapi.ComparisonOperatorLessThanThreshold:
		return LessThan, true
	case cloudwatchapi.ComparisonOperatorLessThanOrEqualToThreshold:
		return LessThanOrEqual, true
	case cloudwatchapi.ComparisonOperatorLessThanLowerOrGreaterThanUpperThreshold,
		cloudwatchapi.ComparisonOperatorLessThanLowerThreshold,
		cloudwatchapi.ComparisonOperatorGreaterThanUpperThreshold:
	}
	return 0, false
}

// Breaches reports whether v, the statistic as first operand, crosses
// threshold T: v > T, v >= T, v < T or v <= T (cloudwatch-alarms.md 3.3).
func (c Comparison) Breaches(v, threshold float64) bool {
	switch c {
	case GreaterThan:
		return v > threshold
	case GreaterThanOrEqual:
		return v >= threshold
	case LessThan:
		return v < threshold
	case LessThanOrEqual:
		return v <= threshold
	}
	return false
}

// Phrase is the operator in a StateReason: "greater than", "greater than
// or equal to", "less than" or "less than or equal to"
// (cloudwatch-alarms.md 4.1). The OrEqual phrases are inferred
// (cloudwatch.alarm.reason-format, CWA-07).
func (c Comparison) Phrase() string {
	switch c {
	case GreaterThan:
		return "greater than"
	case GreaterThanOrEqual:
		return "greater than or equal to"
	case LessThan:
		return "less than"
	case LessThanOrEqual:
		return "less than or equal to"
	}
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

// Range is R, the number of bins one evaluation reads: N + 5 for a
// 60-second period and N + 2 for any other (cloudwatch.alarm.evaluation-range,
// cloudwatch-alarms.md 3.4). AWS's ranges for 60 and 300 seconds are
// recorded (CWA-01); N + 2 for the other periods is UNVERIFIED.
func (c Config) Range() int {
	if c.Period == 60 {
		return c.N + 5
	}
	return c.N + 2
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
// The checks run in that order and the first match is the Why.
// PutMetricAlarm validation guarantees the single-statistic fields of any
// other alarm; one stored without them is reported as not evaluable
// rather than evaluated on a zero value.
func ConfigOf(in *cloudwatchapi.PutMetricAlarmInput) (Config, *NotEvaluableError) {
	notEvaluable := func(why string) (Config, *NotEvaluableError) {
		return Config{}, &NotEvaluableError{Alarm: in.AlarmName, Why: why}
	}
	switch {
	case in.ExtendedStatistic != nil:
		return notEvaluable("ExtendedStatistic")
	case len(in.Metrics) > 0:
		return notEvaluable("Metrics")
	case in.ThresholdMetricId != nil:
		return notEvaluable("ThresholdMetricId")
	case in.EvaluationCriteria != nil:
		return notEvaluable("EvaluationCriteria")
	}
	if _, wall := in.EvaluationWindow.(cloudwatchapi.EvaluationWindowMemberWallClockWindow); wall {
		return notEvaluable("WallClockWindow")
	}
	if in.ComparisonOperator == nil {
		return notEvaluable("no ComparisonOperator")
	}
	cmp, ok := ParseComparison(*in.ComparisonOperator)
	if !ok {
		return notEvaluable("ComparisonOperator " + string(*in.ComparisonOperator))
	}
	switch {
	case in.Statistic == nil:
		return notEvaluable("no Statistic")
	case in.Period == nil:
		return notEvaluable("no Period")
	case in.EvaluationPeriods == nil:
		return notEvaluable("no EvaluationPeriods")
	case in.Threshold == nil:
		return notEvaluable("no Threshold")
	}
	treatment, ok := ParseTreatment(in.TreatMissingData)
	if !ok {
		return notEvaluable("TreatMissingData " + *in.TreatMissingData)
	}
	if in.Namespace != nil && *in.Namespace == "AWS/DynamoDB" {
		treatment = TreatIgnore // cloudwatch.alarm.dynamodb-missing-data (cloudwatch-alarms.md 3.7)
	}
	n := int(*in.EvaluationPeriods)
	cfg := Config{
		Period: *in.Period, N: n, M: n,
		Comparison: cmp, Threshold: *in.Threshold, Treatment: treatment,
		Statistic: *in.Statistic, Unit: in.Unit,
	}
	if in.DatapointsToAlarm != nil {
		cfg.M, cfg.MSet = int(*in.DatapointsToAlarm), true
	}
	if w := in.WarmUpConfiguration; w != nil {
		cfg.WarmUp = &WarmUp{
			Duration: time.Duration(w.WarmUpPeriodDurationInMinutes) * time.Minute,
			WaitFull: w.OnlyStartEvaluatingAfterWarmUpPeriodEnds != nil && *w.OnlyStartEvaluatingAfterWarmUpPeriodEnds,
		}
	}
	return cfg, nil
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
func Evaluate(cfg Config, bins []Bin, current cloudwatchapi.StateValue) Outcome {
	v, ev := decide(cfg, bins)
	to := cloudwatchapi.StateValue(v)
	if v == retain || to == current { // cloudwatch.alarm.unchanged-state (cloudwatch-alarms.md 3.8)
		return Retain{}
	}
	return Transition{To: to, Evidence: ev}
}

// verdict is one decision of the 3.5 algorithm: a state, or retain.
type verdict cloudwatchapi.StateValue

const (
	toAlarm                = verdict(cloudwatchapi.StateValueAlarm)
	toOK                   = verdict(cloudwatchapi.StateValueOk)
	toInsufficient         = verdict(cloudwatchapi.StateValueInsufficientData)
	retain         verdict = ""
)

func alarmIf(breached bool) verdict {
	if breached {
		return toAlarm
	}
	return toOK
}

// decide is the 3.5 algorithm with the Evidence it read.
func decide(cfg Config, bins []Bin) (verdict, Evidence) {
	ev := Evidence{Bins: bins}
	var values []Value
	for _, b := range bins {
		if v, ok := b.(Value); ok {
			values = append(values, v)
		}
	}
	switch {
	case len(values) >= cfg.N:
		// A: enough real data; the treatment is unused.
		ev.Used = values[:cfg.N]
		ev.Breaching = cfg.breaching(ev.Used)
		return alarmIf(ev.Breaching >= cfg.M), ev
	case len(values) == 0:
		// B: no real data; every point is filled.
		ev.Filled = cfg.N
		return cfg.noData(), ev
	}
	// C: some real data, the rest filled.
	ev.Used = values
	ev.Filled = cfg.N - len(values)
	ev.Breaching = cfg.breaching(values)
	return cfg.someData(bins, ev), ev
}

func (c Config) breaching(vs []Value) int {
	n := 0
	for _, v := range vs {
		if c.Comparison.Breaches(v.Value, c.Threshold) {
			n++
		}
	}
	return n
}

// noData is case B of the 3.5 algorithm.
func (c Config) noData() verdict {
	switch c.Treatment {
	case TreatMissing:
		return toInsufficient
	case TreatBreaching:
		return toAlarm
	case TreatNotBreaching:
		return toOK
	case TreatIgnore:
	}
	return retain
}

// someData is case C of the 3.5 algorithm, over ev's Used, Filled and
// Breaching.
func (c Config) someData(bins []Bin, ev Evidence) verdict {
	b := ev.Breaching
	switch c.Treatment {
	case TreatBreaching:
		return alarmIf(b+ev.Filled >= c.M)
	case TreatNotBreaching:
		return alarmIf(b >= c.M)
	case TreatMissing, TreatIgnore:
	}
	if b >= c.M { // C1
		return toAlarm
	}
	if b < len(ev.Used) { // C2
		return toOK
	}
	// C3: premature alarm state, the oldest breaching bin of the whole
	// range d1..dR at least M periods old: X - - - - goes to ALARM under
	// missing (cloudwatch.alarm.unaged-breach, CWA-04).
	oldest := 0
	for k, b := range bins {
		if v, ok := b.(Value); ok && c.Comparison.Breaches(v.Value, c.Threshold) {
			oldest = k + 1
		}
	}
	if oldest > 0 && oldest >= c.M && c.Treatment == TreatMissing {
		return toAlarm
	}
	// C3 under ignore, and C4: every real bin breaching and too recent,
	// - - - - X, keeps the state (CWA-04).
	return retain
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
func (w WarmUp) Over(elapsed time.Duration, bins []Bin, n int) bool {
	if elapsed >= w.Duration {
		return true
	}
	if w.WaitFull || n > len(bins) {
		return false
	}
	for _, b := range bins[:n] {
		if _, ok := b.(Value); !ok {
			return false
		}
	}
	return true
}
