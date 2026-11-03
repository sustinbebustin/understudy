package alarm_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

const (
	stateOK           = cloudwatchapi.StateValueOk
	stateAlarm        = cloudwatchapi.StateValueAlarm
	stateInsufficient = cloudwatchapi.StateValueInsufficientData
)

var states = []cloudwatchapi.StateValue{stateOK, stateAlarm, stateInsufficient}

// t0 is the evaluation minute of AWS's alarm-actions example, 2025-07-03
// 08:34 (cloudwatch-alarms.md 3.2).
var t0 = time.Date(2025, 7, 3, 8, 34, 0, 0, time.UTC)

// vectorBins reads a data point string as cloudwatch-alarms.md 3.6 prints
// it, oldest first ("0 - X - X" is d5 d4 d3 d2 d1), into bins newest first
// ending at t0: "0" a Value that does not breach threshold 1 under
// GreaterThan, "X" one that does, "-" a Missing.
func vectorBins(t *testing.T, points string) []alarm.Bin {
	t.Helper()
	const period = 5 * time.Minute
	fields := strings.Fields(points)
	bins := make([]alarm.Bin, len(fields))
	for i, f := range fields {
		k := len(fields) - i
		start := t0.Add(-time.Duration(k) * period)
		switch f {
		case "-":
			bins[k-1] = alarm.Missing{Start: start}
		case "0":
			bins[k-1] = alarm.Value{Start: start, Value: 0, SampleCount: 1}
		case "X":
			bins[k-1] = alarm.Value{Start: start, Value: 2, SampleCount: 1}
		default:
			t.Fatalf("data point %q in %q is none of 0, X, -", f, points)
		}
	}
	return bins
}

// vectorConfig is the alarm of cloudwatch-alarms.md 3.6: a 5-minute period,
// N evaluation periods, M datapoints to alarm, breaching above 1.
func vectorConfig(n, m int, treatment alarm.Treatment) alarm.Config {
	return alarm.Config{
		Period: 300, N: n, M: m, MSet: true,
		Comparison: alarm.GreaterThan, Threshold: 1,
		Treatment: treatment, Statistic: cloudwatchapi.StatisticAverage,
	}
}

// retain is a cell that keeps the current state, whatever it is.
const retain = "retain"

var treatments = []alarm.Treatment{alarm.TreatMissing, alarm.TreatIgnore, alarm.TreatBreaching, alarm.TreatNotBreaching}

// missingDataVector is one row of a cloudwatch-alarms.md 3.6 table: its
// cells in the column order Missing, Ignore, Breaching, Not breaching.
type missingDataVector struct {
	name   string
	points string
	n, m   int
	cells  [4]string
}

// The 40 documented cells of cloudwatch-alarms.md 3.6 tables 1 and 2
// (aws-doc, https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-missing-data.html)
// and the two vectors beyond them, x1 and x2, whose missing and ignore
// cells cloudwatch.alarm.unaged-breach governs; CWA-04 recorded all 48
// (x2 under missing goes to ALARM, x1 retains). Each cell runs from
// each of the three states: a state cell is a Transition to it from the
// two others and a Retain from itself (cloudwatch.alarm.unchanged-state,
// cwa 3.8); a retain cell is a Retain from all three. Evidence.Bins is the
// input as passed (contract 1.3).
func TestEvaluateMissingDataVectors(t *testing.T) {
	ok, al, insufficient := string(stateOK), string(stateAlarm), string(stateInsufficient)
	unagedX2 := switchCase(t, "cloudwatch.alarm.unaged-breach", map[string]string{"alarm-if-aged-in-range-else-retain": al})
	sameState := switchCase(t, "cloudwatch.alarm.unchanged-state", map[string]bool{"no-update": true})
	// The evaluation range of 5 for N = 3 is the one size AWS documents
	// (cwa 3.4, aws-doc).
	vectors := []missingDataVector{
		{"table1/row1", "0 - X - X", 3, 3, [4]string{ok, ok, ok, ok}},
		{"table1/row2", "0 - - - -", 3, 3, [4]string{ok, ok, ok, ok}},
		{"table1/row3", "- - - - -", 3, 3, [4]string{insufficient, retain, al, ok}},
		{"table1/row4", strings.Join([]string{"0", "X", "X", "-", "X"}, " "), 3, 3, [4]string{al, al, al, al}},
		{"table1/row5", "- - X - -", 3, 3, [4]string{al, retain, al, ok}},
		{"table2/row1", "0 - X - X", 3, 2, [4]string{al, al, al, al}},
		{"table2/row2", "0 0 X 0 X", 3, 2, [4]string{al, al, al, al}},
		{"table2/row3", "0 - X - -", 3, 2, [4]string{ok, ok, al, ok}},
		{"table2/row4", "- - - - 0", 3, 2, [4]string{ok, ok, al, ok}},
		{"table2/row5", "- - - X -", 3, 2, [4]string{al, retain, al, ok}},
		{"x1", "- - - - X", 3, 3, [4]string{retain, retain, al, ok}},
		{"x2", "X - - - -", 3, 3, [4]string{unagedX2, retain, al, ok}},
	}
	for _, v := range vectors {
		for i, treatment := range treatments {
			cell := v.cells[i]
			for _, current := range states {
				t.Run(fmt.Sprintf("%s/%s/from-%s", v.name, treatment, current), func(t *testing.T) {
					cfg := vectorConfig(v.n, v.m, treatment)
					if got := cfg.Range(); got != 5 {
						t.Fatalf("Range() for N = 3 = %d, want 5 (cwa 3.4, aws-doc)", got)
					}
					bins := vectorBins(t, v.points)
					got := alarm.Evaluate(cfg, slices.Clone(bins), current)
					want := cell
					if cell == string(current) && sameState {
						want = retain
					}
					checkOutcome(t, got, want, bins)
				})
			}
		}
	}
}

// checkOutcome checks that got is Retain{} for want "retain", else a
// Transition to want whose Evidence.Bins are bins.
func checkOutcome(t *testing.T, got alarm.Outcome, want string, bins []alarm.Bin) {
	t.Helper()
	switch o := got.(type) {
	case alarm.Retain:
		if want != retain {
			t.Fatalf("Evaluate = Retain, want a Transition to %s", want)
		}
	case alarm.Transition:
		if want == retain {
			t.Fatalf("Evaluate = Transition to %s, want Retain", o.To)
		}
		if string(o.To) != want {
			t.Fatalf("Evaluate = Transition to %s, want %s", o.To, want)
		}
		if !slices.Equal(o.Evidence.Bins, bins) {
			t.Errorf("Evidence.Bins = %v, want the input %v", o.Evidence.Bins, bins)
		}
	default:
		t.Fatalf("Evaluate = %#v, neither Retain nor Transition", got)
	}
}

// The Evidence of each case of the cwa 3.5 algorithm (contract 1.3, 1.4):
// case A counts the N newest real bins and fills nothing, case B counts
// nothing and fills N, case C counts every real bin of d1..dR and fills
// N minus their number.
func TestEvaluateEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		points    string
		n, m      int
		treatment alarm.Treatment
		to        cloudwatchapi.StateValue
		used      []int // 1-based bin positions of Used, newest first
		filled    int
		breaching int
	}{
		// table 2 row 1: three real bins, the newest three counted.
		{"case A", "0 - X - X", 3, 2, alarm.TreatMissing, stateAlarm, []int{1, 3, 5}, 0, 2},
		// table 2 row 2: five real bins, d1..d3 counted, d4 and d5 not.
		{"case A newest only", "0 0 X 0 X", 3, 2, alarm.TreatMissing, stateAlarm, []int{1, 2, 3}, 0, 2},
		// table 1 row 3 under breaching: nothing real, N filled.
		{"case B", "- - - - -", 3, 3, alarm.TreatBreaching, stateAlarm, nil, 3, 0},
		// table 2 row 3 under breaching: two real bins, one filled.
		{"case C", "0 - X - -", 3, 2, alarm.TreatBreaching, stateAlarm, []int{3, 5}, 1, 1},
		// table 1 row 2 under notBreaching: one real bin, two filled.
		{"case C beyond dN", "0 - - - -", 3, 3, alarm.TreatNotBreaching, stateOK, []int{5}, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bins := vectorBins(t, tc.points)
			got := alarm.Evaluate(vectorConfig(tc.n, tc.m, tc.treatment), slices.Clone(bins), stateInsufficient)
			tr, ok := got.(alarm.Transition)
			if !ok || tr.To != tc.to {
				t.Fatalf("Evaluate = %#v, want a Transition to %s (cwa 3.6)", got, tc.to)
			}
			var used []alarm.Value
			for _, k := range tc.used {
				v, isValue := bins[k-1].(alarm.Value)
				if !isValue {
					t.Fatalf("bin d%d of %q is not a Value", k, tc.points)
				}
				used = append(used, v)
			}
			ev := tr.Evidence
			if !slices.Equal(ev.Used, used) || ev.Filled != tc.filled || ev.Breaching != tc.breaching {
				t.Errorf("Evidence = Used %v Filled %d Breaching %d\nwant       Used %v Filled %d Breaching %d",
					ev.Used, ev.Filled, ev.Breaching, used, tc.filled, tc.breaching)
			}
		})
	}
}

// Range is N + 5 for a 60-second period and N + 2 for the others under
// cloudwatch.alarm.evaluation-range: CWA-01 recorded both at N = 1 and 3,
// and 5 for N = 3 at 300 seconds is documented (cwa 3.4); other periods
// and N are the research's extrapolation.
func TestRange(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.evaluation-range", "n-plus-5-for-60s,n-plus-2-otherwise")
	for _, period := range []int32{10, 60, 120, 300, 3600} {
		extra := 2
		if period == 60 {
			extra = 5
		}
		for _, n := range []int{1, 2, 3, 5, 10} {
			if got := (alarm.Config{Period: period, N: n, M: n}).Range(); got != n+extra {
				t.Errorf("Range() for Period %d, N = %d = %d, want %d", period, n, got, n+extra)
			}
		}
	}
}

// ParseTreatment reads TreatMissingData: absent is missing, "The default
// behavior is missing" (cwa 3.5, aws-doc); the four wire values are the
// model enum's text, and String writes them back.
func TestParseTreatment(t *testing.T) {
	if got, ok := alarm.ParseTreatment(nil); !ok || got != alarm.TreatMissing {
		t.Errorf("ParseTreatment(nil) = %v %v, want missing (cwa 3.5)", got, ok)
	}
	for _, want := range treatments {
		s := want.String()
		if got, ok := alarm.ParseTreatment(&s); !ok || got != want {
			t.Errorf("ParseTreatment(%q) = %v %v, want %v", s, got, ok, want)
		}
	}
	names := []string{"missing", "ignore", "breaching", "notBreaching"} // cwa 3.5
	for i, want := range treatments {
		if got := want.String(); got != names[i] {
			t.Errorf("Treatment(%d).String() = %q, want %q", want, got, names[i])
		}
	}
	for _, s := range []string{"", "Missing", "not-breaching", "bogus"} {
		if got, ok := alarm.ParseTreatment(&s); ok {
			t.Errorf("ParseTreatment(%q) = %v, true; want false", s, got)
		}
	}
}

// The four static-threshold operators compare with the statistic as first
// operand; the three anomaly detection operators have no Comparison
// (cwa 3.3, model doc of ComparisonOperator).
func TestComparison(t *testing.T) {
	for _, tc := range []struct {
		op   cloudwatchapi.ComparisonOperator
		want alarm.Comparison
		// below, equal and above: whether a value one under, at and one
		// over the threshold breaches.
		below   bool
		equal   bool
		above   bool
		phrase  string
		orEqual bool
	}{
		{cloudwatchapi.ComparisonOperatorGreaterThanThreshold, alarm.GreaterThan, false, false, true, "greater than", false},
		{cloudwatchapi.ComparisonOperatorGreaterThanOrEqualToThreshold, alarm.GreaterThanOrEqual, false, true, true, "greater than or equal to", true},
		{cloudwatchapi.ComparisonOperatorLessThanThreshold, alarm.LessThan, true, false, false, "less than", false},
		{cloudwatchapi.ComparisonOperatorLessThanOrEqualToThreshold, alarm.LessThanOrEqual, true, true, false, "less than or equal to", true},
	} {
		got, ok := alarm.ParseComparison(tc.op)
		if !ok || got != tc.want {
			t.Errorf("ParseComparison(%s) = %v %v, want %v", tc.op, got, ok, tc.want)
			continue
		}
		const threshold = 31.0
		if got.Breaches(threshold-1, threshold) != tc.below || got.Breaches(threshold, threshold) != tc.equal || got.Breaches(threshold+1, threshold) != tc.above {
			t.Errorf("%s breaches below/equal/above = %v %v %v, want %v %v %v", tc.op,
				got.Breaches(threshold-1, threshold), got.Breaches(threshold, threshold), got.Breaches(threshold+1, threshold),
				tc.below, tc.equal, tc.above)
		}
		// "greater than" and "less than" are captured (cwa 4.1); "greater
		// than or equal to" is captured in a breaching reason (alerta
		// sample, cwa 4.1); the OrEqual phrases otherwise follow
		// cloudwatch.alarm.reason-format (UNVERIFIED, CWA-07).
		if tc.orEqual {
			followsSwitch(t, "cloudwatch.alarm.reason-format", "count-form-unless-datapoints-to-alarm")
		}
		if p := got.Phrase(); p != tc.phrase {
			t.Errorf("%s Phrase() = %q, want %q (cwa 4.1)", tc.op, p, tc.phrase)
		}
	}
	for _, op := range []cloudwatchapi.ComparisonOperator{
		cloudwatchapi.ComparisonOperatorLessThanLowerOrGreaterThanUpperThreshold,
		cloudwatchapi.ComparisonOperatorLessThanLowerThreshold,
		cloudwatchapi.ComparisonOperatorGreaterThanUpperThreshold,
	} {
		if got, ok := alarm.ParseComparison(op); ok {
			t.Errorf("ParseComparison(%s) = %v, true; want false: anomaly detection only (cwa 3.3)", op, got)
		}
	}
}

// storedAlarm is an evaluable alarm as PutMetricAlarm stores it.
func storedAlarm() *cloudwatchapi.PutMetricAlarmInput {
	return &cloudwatchapi.PutMetricAlarmInput{
		AlarmName:          "cpu",
		MetricName:         new("CPUUtilization"),
		Namespace:          new("app"),
		Statistic:          new(cloudwatchapi.StatisticAverage),
		Period:             new(int32(300)),
		EvaluationPeriods:  new(int32(3)),
		Threshold:          new(31.0),
		ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold),
	}
}

// ConfigOf reads a stored alarm: M defaults to N (model doc of
// DatapointsToAlarm, cwa 3.4), the treatment to missing (cwa 3.5), and the
// warm-up from WarmUpConfiguration in minutes (cwa 2.4).
func TestConfigOf(t *testing.T) {
	cfg, nerr := alarm.ConfigOf(storedAlarm())
	if nerr != nil {
		t.Fatalf("ConfigOf = %v", nerr)
	}
	want := alarm.Config{
		Period: 300, N: 3, M: 3, Comparison: alarm.GreaterThan, Threshold: 31,
		Treatment: alarm.TreatMissing, Statistic: cloudwatchapi.StatisticAverage,
	}
	if cfg.Period != want.Period || cfg.N != want.N || cfg.M != want.M || cfg.MSet || cfg.Comparison != want.Comparison ||
		cfg.Threshold != want.Threshold || cfg.Treatment != want.Treatment || cfg.WarmUp != nil || cfg.Statistic != want.Statistic || cfg.Unit != nil {
		t.Errorf("ConfigOf = %+v\nwant       %+v", cfg, want)
	}

	in := storedAlarm()
	in.DatapointsToAlarm = new(int32(2))
	in.TreatMissingData = new("breaching")
	in.Unit = new(cloudwatchapi.StandardUnitSeconds)
	in.ComparisonOperator = new(cloudwatchapi.ComparisonOperatorLessThanOrEqualToThreshold)
	in.WarmUpConfiguration = &cloudwatchapi.WarmUpConfiguration{WarmUpPeriodDurationInMinutes: 15}
	in.EvaluationWindow = cloudwatchapi.EvaluationWindowMemberSlidingWindow{}
	cfg, nerr = alarm.ConfigOf(in)
	if nerr != nil {
		t.Fatalf("ConfigOf with a SlidingWindow = %v, want evaluable (cwa 12.1)", nerr)
	}
	if cfg.M != 2 || !cfg.MSet || cfg.Treatment != alarm.TreatBreaching || cfg.Comparison != alarm.LessThanOrEqual ||
		cfg.Unit == nil || *cfg.Unit != cloudwatchapi.StandardUnitSeconds {
		t.Errorf("ConfigOf = %+v, want M 2 set, breaching, LessThanOrEqual, Seconds", cfg)
	}
	if cfg.WarmUp == nil || *cfg.WarmUp != (alarm.WarmUp{Duration: 15 * time.Minute}) {
		t.Errorf("WarmUp = %+v, want 15 minutes, WaitFull false (the flag defaults to false, cwa 2.4)", cfg.WarmUp)
	}
	in.WarmUpConfiguration.OnlyStartEvaluatingAfterWarmUpPeriodEnds = new(true)
	if cfg, nerr = alarm.ConfigOf(in); nerr != nil || cfg.WarmUp == nil || !cfg.WarmUp.WaitFull {
		t.Errorf("WarmUp = %+v, want WaitFull", cfg.WarmUp)
	}
}

// An AWS/DynamoDB alarm always ignores missing data whatever it sent, under
// cloudwatch.alarm.dynamodb-missing-data (cwa 3.7, CWA-21).
func TestConfigOfDynamoDBIgnoresMissingData(t *testing.T) {
	forced := switchCase(t, "cloudwatch.alarm.dynamodb-missing-data", map[string]bool{"always-ignore": true})
	in := storedAlarm()
	in.Namespace = new("AWS/DynamoDB")
	in.TreatMissingData = new("breaching")
	cfg, nerr := alarm.ConfigOf(in)
	if nerr != nil {
		t.Fatalf("ConfigOf = %v", nerr)
	}
	if forced && cfg.Treatment != alarm.TreatIgnore {
		t.Errorf("Treatment of an AWS/DynamoDB alarm sent breaching = %v, want ignore", cfg.Treatment)
	}
}

// Alarm kinds understudy cannot evaluate are stored and never evaluated,
// each named by why; the checks run in a fixed order and report the first
// (cwa 12.1, 12.7; contract 1.2).
func TestConfigOfNotEvaluable(t *testing.T) {
	wall := cloudwatchapi.EvaluationWindowMemberWallClockWindow{Value: cloudwatchapi.WallClockWindow{Timezone: new("UTC")}}
	criteria := cloudwatchapi.EvaluationCriteriaMemberPromQLCriteria{}
	for _, tc := range []struct {
		name  string
		alter func(in *cloudwatchapi.PutMetricAlarmInput)
		why   string
	}{
		{"extended statistic", func(in *cloudwatchapi.PutMetricAlarmInput) {
			in.Statistic, in.ExtendedStatistic = nil, new("p90")
		}, "ExtendedStatistic"},
		{"metrics", func(in *cloudwatchapi.PutMetricAlarmInput) {
			in.MetricName, in.Namespace, in.Statistic, in.Period = nil, nil, nil, nil
			in.Metrics = []cloudwatchapi.MetricDataQuery{{Id: "m1", Expression: new("SUM(METRICS())")}}
		}, "Metrics"},
		{"threshold metric id", func(in *cloudwatchapi.PutMetricAlarmInput) {
			in.ThresholdMetricId = new("ad1")
		}, "ThresholdMetricId"},
		{"evaluation criteria", func(in *cloudwatchapi.PutMetricAlarmInput) {
			in.EvaluationCriteria = criteria
		}, "EvaluationCriteria"},
		{"wall clock window", func(in *cloudwatchapi.PutMetricAlarmInput) {
			in.EvaluationWindow = wall
		}, "WallClockWindow"},
		{"anomaly operator", func(in *cloudwatchapi.PutMetricAlarmInput) {
			in.ComparisonOperator = new(cloudwatchapi.ComparisonOperatorLessThanLowerOrGreaterThanUpperThreshold)
		}, "ComparisonOperator LessThanLowerOrGreaterThanUpperThreshold"},
		{"first of several", func(in *cloudwatchapi.PutMetricAlarmInput) {
			in.ExtendedStatistic = new("p90")
			in.EvaluationWindow = wall
			in.ComparisonOperator = new(cloudwatchapi.ComparisonOperatorGreaterThanUpperThreshold)
		}, "ExtendedStatistic"},
		{"wall clock before operator", func(in *cloudwatchapi.PutMetricAlarmInput) {
			in.EvaluationWindow = wall
			in.ComparisonOperator = new(cloudwatchapi.ComparisonOperatorLessThanLowerThreshold)
		}, "WallClockWindow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := storedAlarm()
			tc.alter(in)
			_, nerr := alarm.ConfigOf(in)
			if nerr == nil {
				t.Fatal("ConfigOf = a Config, want a NotEvaluableError")
			}
			if nerr.Alarm != "cpu" || nerr.Why != tc.why {
				t.Errorf("NotEvaluableError = %+v, want Alarm cpu, Why %q", *nerr, tc.why)
			}
			if got, want := nerr.Error(), "alarm cpu is stored and never evaluated: "+tc.why; got != want {
				t.Errorf("Error() = %q, want %q", got, want)
			}
		})
	}
}

// Warm-up ends once its duration has passed; with
// OnlyStartEvaluatingAfterWarmUpPeriodEnds false it may end earlier, when
// the window can be filled, which cloudwatch.alarm.warm-up-end reads as
// every one of the N newest periods holding data (UNVERIFIED, CWA-22); with
// the flag true it waits the full duration (cwa 2.4, aws-doc).
func TestWarmUpOver(t *testing.T) {
	everyPeriod := switchCase(t, "cloudwatch.alarm.warm-up-end", map[string]bool{"every-period-has-data": true})
	// Oldest first, as cwa 3.6 prints data points: full has d1..d3,
	// partial d1 but not d2.
	full := vectorBins(t, "- - 0 X 0")
	partial := vectorBins(t, "X 0 X - 0")
	none := vectorBins(t, "- - - - -")
	early := alarm.WarmUp{Duration: 30 * time.Minute}
	waits := alarm.WarmUp{Duration: 30 * time.Minute, WaitFull: true}
	for _, tc := range []struct {
		name    string
		w       alarm.WarmUp
		elapsed time.Duration
		bins    []alarm.Bin
		want    bool
	}{
		{"duration passed, no data", early, 30 * time.Minute, none, true},
		{"duration passed, waiting", waits, 31 * time.Minute, none, true},
		{"waiting, window full", waits, 29 * time.Minute, full, false},
		{"early, window full", early, 15 * time.Minute, full, everyPeriod},
		{"early, newest period only", early, 15 * time.Minute, partial, false},
		{"early, no data", early, 15 * time.Minute, none, false},
	} {
		if got := tc.w.Over(tc.elapsed, tc.bins, 3); got != tc.want {
			t.Errorf("%s: Over = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// at reads a UTC test instant "2006-01-02 15:04:05.000".
func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02 15:04:05.000", s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Evaluation cadence: every minute for a period of a minute or more, every
// 10 seconds for 10, 20 and 30 second periods, hourly once N x Period
// exceeds a day (cwa 3.1, aws-doc; cloudwatch.alarm.evaluation-cadence).
// The boundary within the minute and the first evaluation after creation
// follow cloudwatch.alarm.evaluation-phase and
// cloudwatch.alarm.first-evaluation (inferred, CWA-02, CWA-03).
func TestNext(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.evaluation-cadence", "minute,10s-for-high-resolution,hourly-past-one-day")
	followsSwitch(t, "cloudwatch.alarm.evaluation-phase", "minute-boundary")
	followsSwitch(t, "cloudwatch.alarm.first-evaluation", "next-boundary")
	minute := alarm.Config{Period: 300, N: 3, M: 3}
	sub := alarm.Config{Period: 10, N: 3, M: 3}
	long := alarm.Config{Period: 86400, N: 2, M: 2}
	day := alarm.Config{Period: 86400, N: 1, M: 1} // exactly one day: not long
	for _, tc := range []struct {
		cfg   alarm.Config
		after string
		want  string
	}{
		{minute, "2025-07-03 12:00:00.000", "2025-07-03 12:01:00.000"},
		{minute, "2025-07-03 12:00:30.000", "2025-07-03 12:01:00.000"},
		{minute, "2025-07-03 12:00:59.999", "2025-07-03 12:01:00.000"},
		{sub, "2025-07-03 12:00:05.000", "2025-07-03 12:00:10.000"},
		{sub, "2025-07-03 12:00:10.000", "2025-07-03 12:00:20.000"},
		{sub, "2025-07-03 12:00:55.500", "2025-07-03 12:01:00.000"},
		{long, "2025-07-03 10:03:00.000", "2025-07-03 11:00:00.000"},
		{long, "2025-07-03 11:00:00.000", "2025-07-03 12:00:00.000"},
		{day, "2025-07-03 10:03:00.000", "2025-07-03 10:04:00.000"},
	} {
		if got := tc.cfg.Next(at(t, tc.after)); !got.Equal(at(t, tc.want)) {
			t.Errorf("Period %d N %d: Next(%s) = %v, want %s", tc.cfg.Period, tc.cfg.N, tc.after, got, tc.want)
		}
	}
}

// The window ends at T0, the evaluation time truncated to the minute, and
// spans Range periods; the vectors are cwa 3.2's AWS examples (aws-doc).
// For N other than 3 the range follows cloudwatch.alarm.evaluation-range;
// sub-minute T0 is the 10-second division (CWA-02) and a multi-day alarm's
// the hour (cwa 3.1), under cloudwatch.alarm.window-anchor.
func TestWindow(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.window-anchor", "evaluation-minute")
	followsSwitch(t, "cloudwatch.alarm.evaluation-range", "n-plus-5-for-60s,n-plus-2-otherwise")
	for _, tc := range []struct {
		name   string
		cfg    alarm.Config
		at     string
		anchor string
		start  string
	}{
		// alarm-actions: eval 08:34:06.300, bins 08:29, 08:24, 08:19.
		{"alarm-actions", alarm.Config{Period: 300, N: 3, M: 3}, "2025-07-03 08:34:06.300", "2025-07-03 08:34:00.000", "2025-07-03 08:09:00.000"},
		// cloudwatch-and-eventbridge: eval 17:04:40.985, bin 16:59.
		{"single metric", alarm.Config{Period: 300, N: 1, M: 1}, "2019-10-02 17:04:40.985", "2019-10-02 17:04:00.000", "2019-10-02 16:49:00.000"},
		// alarms-and-actions-Lambda: eval 12:31:29.591, bins 12:30 to 12:26.
		{"lambda", alarm.Config{Period: 60, N: 5, M: 5}, "2023-08-04 12:31:29.591", "2023-08-04 12:31:00.000", "2023-08-04 12:21:00.000"},
		{"sub-minute", alarm.Config{Period: 10, N: 3, M: 3}, "2025-07-03 12:00:15.500", "2025-07-03 12:00:10.000", "2025-07-03 11:59:20.000"},
		{"multi-day", alarm.Config{Period: 86400, N: 2, M: 2}, "2025-07-03 10:03:20.000", "2025-07-03 10:00:00.000", "2025-06-29 10:00:00.000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Anchor(at(t, tc.at)); !got.Equal(at(t, tc.anchor)) {
				t.Errorf("Anchor = %v, want %s", got, tc.anchor)
			}
			start, end := tc.cfg.Window(at(t, tc.at))
			if !start.Equal(at(t, tc.start)) || !end.Equal(at(t, tc.anchor)) {
				t.Errorf("Window = [%v, %v), want [%s, %s)", start, end, tc.start, tc.anchor)
			}
		})
	}
}
