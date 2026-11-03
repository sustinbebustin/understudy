package alarm_test

import (
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

// evalCase is one random evaluation: a config, its R bins newest first and
// a current state.
type evalCase struct {
	cfg     alarm.Config
	bins    []alarm.Bin
	current cloudwatchapi.StateValue
}

var comparisons = []alarm.Comparison{alarm.GreaterThan, alarm.GreaterThanOrEqual, alarm.LessThan, alarm.LessThanOrEqual}

// drawCase draws N from 1 to 10, M from 1 to N, an operator, a threshold
// and R = cfg.Range() bins, each missing or a value near the threshold so
// both outcomes of a comparison and ties occur.
func drawCase(t *rapid.T) evalCase {
	n := rapid.IntRange(1, 10).Draw(t, "N")
	m := rapid.IntRange(1, n).Draw(t, "M")
	cfg := alarm.Config{
		Period: 60, N: n, M: m, MSet: rapid.Bool().Draw(t, "MSet"),
		Comparison: rapid.SampledFrom(comparisons).Draw(t, "comparison"),
		Threshold:  float64(rapid.IntRange(-2, 2).Draw(t, "threshold")),
		Treatment:  rapid.SampledFrom(treatments).Draw(t, "treatment"),
		Statistic:  cloudwatchapi.StatisticAverage,
	}
	r := cfg.Range()
	bins := make([]alarm.Bin, r)
	for k := range bins {
		start := t0.Add(-time.Duration(k+1) * time.Minute)
		if rapid.Bool().Draw(t, "missing") {
			bins[k] = alarm.Missing{Start: start}
			continue
		}
		v := float64(rapid.IntRange(-4, 4).Draw(t, "value")) / 2
		bins[k] = alarm.Value{Start: start, Value: v, SampleCount: float64(rapid.IntRange(1, 5).Draw(t, "samples"))}
	}
	return evalCase{cfg: cfg, bins: bins, current: rapid.SampledFrom(states).Draw(t, "current")}
}

func (c evalCase) with(tr alarm.Treatment) alarm.Outcome {
	cfg := c.cfg
	cfg.Treatment = tr
	return alarm.Evaluate(cfg, slices.Clone(c.bins), c.current)
}

func realBins(bins []alarm.Bin) []alarm.Value {
	var out []alarm.Value
	for _, b := range bins {
		if v, ok := b.(alarm.Value); ok {
			out = append(out, v)
		}
	}
	return out
}

func transitionTo(o alarm.Outcome, s cloudwatchapi.StateValue) bool {
	tr, ok := o.(alarm.Transition)
	return ok && tr.To == s
}

// The properties of cloudwatch-alarms.md 12.3 item 2 (contract 1.4).
func TestEvaluateProperties(t *testing.T) {
	// The range must hold every bin the generator draws.
	followsSwitch(t, "cloudwatch.alarm.evaluation-range", "n-plus-5-for-60s,n-plus-2-otherwise")

	// (1) With at least N real bins the treatment does not change the
	// outcome: "the value you set for how to treat missing data is not
	// needed and is ignored" (cwa 3.5 case 2, aws-doc).
	t.Run("treatment unused with N real bins", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			c := drawCase(t)
			if len(realBins(c.bins)) < c.cfg.N {
				t.Skip("fewer than N real bins")
			}
			first := c.with(alarm.TreatMissing)
			for _, tr := range treatments[1:] {
				if got := c.with(tr); !reflect.DeepEqual(got, first) {
					t.Fatalf("%s: %#v\nmissing: %#v", tr, got, first)
				}
			}
		})
	})

	// (2) If missing yields ALARM, breaching yields ALARM: treating a
	// missing point as bad never makes the alarm less likely to fire.
	t.Run("missing ALARM implies breaching ALARM", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			c := drawCase(t)
			if transitionTo(c.with(alarm.TreatMissing), stateAlarm) && !transitionTo(c.with(alarm.TreatBreaching), stateAlarm) {
				t.Fatalf("missing goes to ALARM, breaching = %#v", c.with(alarm.TreatBreaching))
			}
		})
	})

	// (3) notBreaching yields ALARM only with at least M real breaching
	// bins: filled points are "good" (cwa 3.5).
	t.Run("notBreaching ALARM needs M breaching bins", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			c := drawCase(t)
			got := c.with(alarm.TreatNotBreaching)
			if !transitionTo(got, stateAlarm) {
				return
			}
			breaching := 0
			for _, v := range realBins(c.bins) {
				if c.cfg.Comparison.Breaches(v.Value, c.cfg.Threshold) {
					breaching++
				}
			}
			if breaching < c.cfg.M {
				t.Fatalf("notBreaching goes to ALARM with %d breaching real bins, M = %d", breaching, c.cfg.M)
			}
		})
	})

	// (4) ignore yields what missing yields or Retain, and never
	// INSUFFICIENT_DATA: "The current alarm state is maintained"
	// (cwa 3.5).
	t.Run("ignore is missing or retain", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			c := drawCase(t)
			ignore := c.with(alarm.TreatIgnore)
			if transitionTo(ignore, stateInsufficient) {
				t.Fatalf("ignore goes to INSUFFICIENT_DATA: %#v", ignore)
			}
			if _, kept := ignore.(alarm.Retain); !kept && !reflect.DeepEqual(ignore, c.with(alarm.TreatMissing)) {
				t.Fatalf("ignore = %#v\nmissing = %#v", ignore, c.with(alarm.TreatMissing))
			}
		})
	})

	// (5) The outcome depends only on d1..dR: equal bins give an equal
	// outcome, and a Transition's Evidence.Bins is the input.
	t.Run("depends only on the bins", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			c := drawCase(t)
			a := alarm.Evaluate(c.cfg, slices.Clone(c.bins), c.current)
			b := alarm.Evaluate(c.cfg, slices.Clone(c.bins), c.current)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("two evaluations of equal bins differ:\n%#v\n%#v", a, b)
			}
			if tr, ok := a.(alarm.Transition); ok && !slices.Equal(tr.Evidence.Bins, c.bins) {
				t.Fatalf("Evidence.Bins = %v, want the input %v", tr.Evidence.Bins, c.bins)
			}
		})
	})

	// The outcome is never a Transition to the current state
	// (cloudwatch.alarm.unchanged-state, cwa 3.8).
	t.Run("never a transition to the current state", func(t *testing.T) {
		followsSwitch(t, "cloudwatch.alarm.unchanged-state", "no-update")
		rapid.Check(t, func(t *rapid.T) {
			c := drawCase(t)
			if got := c.with(c.cfg.Treatment); transitionTo(got, c.current) {
				t.Fatalf("Transition to the current state %s", c.current)
			}
		})
	})
}

// referenceOutcome is the cwa 3.5 algorithm as the research writes it,
// followed literally: the decision and the Evidence contract 1.3 and 1.4
// define. retain reports "retain"; the caller applies unchanged-state.
func referenceOutcome(cfg alarm.Config, bins []alarm.Bin) (to cloudwatchapi.StateValue, retain bool, ev alarm.Evidence) {
	breaches := func(v alarm.Value) bool { return cfg.Comparison.Breaches(v.Value, cfg.Threshold) }
	count := func(vs []alarm.Value) int {
		n := 0
		for _, v := range vs {
			if breaches(v) {
				n++
			}
		}
		return n
	}
	values := realBins(bins)
	ev.Bins = bins
	switch {
	case len(values) >= cfg.N: // A
		ev.Used = values[:cfg.N]
		ev.Breaching = count(ev.Used)
		if ev.Breaching >= cfg.M {
			return stateAlarm, false, ev
		}
		return stateOK, false, ev
	case len(values) == 0: // B
		ev.Filled = cfg.N
		// ignore is absent: it retains.
		to, decided := map[alarm.Treatment]cloudwatchapi.StateValue{
			alarm.TreatMissing: stateInsufficient, alarm.TreatBreaching: stateAlarm, alarm.TreatNotBreaching: stateOK,
		}[cfg.Treatment]
		return to, !decided, ev
	}
	// C
	ev.Used = values
	ev.Filled = cfg.N - len(values)
	b := count(values)
	ev.Breaching = b
	switch cfg.Treatment {
	case alarm.TreatBreaching:
		if b+ev.Filled >= cfg.M {
			return stateAlarm, false, ev
		}
		return stateOK, false, ev
	case alarm.TreatNotBreaching:
		if b >= cfg.M {
			return stateAlarm, false, ev
		}
		return stateOK, false, ev
	case alarm.TreatMissing, alarm.TreatIgnore:
	}
	if b >= cfg.M { // C1
		return stateAlarm, false, ev
	}
	if b < len(values) { // C2
		return stateOK, false, ev
	}
	oldest := 0 // C3: 1-based position of the oldest breaching bin of d1..dR (CWA-04)
	for k := range bins {
		if v, ok := bins[k].(alarm.Value); ok && breaches(v) {
			oldest = k + 1
		}
	}
	if oldest > 0 && oldest >= cfg.M {
		if cfg.Treatment == alarm.TreatMissing {
			return stateAlarm, false, ev
		}
		return "", true, ev
	}
	return "", true, ev // C4 (cloudwatch.alarm.unaged-breach, CWA-04)
}

// Evaluate agrees with the cwa 3.5 algorithm, written out independently
// above, on every random evaluation: the decision and the Evidence.
func TestEvaluateFollowsTheAlgorithm(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.unaged-breach", "alarm-if-aged-in-range-else-retain")
	followsSwitch(t, "cloudwatch.alarm.unchanged-state", "no-update")
	followsSwitch(t, "cloudwatch.alarm.evaluation-range", "n-plus-5-for-60s,n-plus-2-otherwise")
	rapid.Check(t, func(t *rapid.T) {
		c := drawCase(t)
		to, keep, ev := referenceOutcome(c.cfg, c.bins)
		got := alarm.Evaluate(c.cfg, slices.Clone(c.bins), c.current)
		if keep || to == c.current {
			if _, ok := got.(alarm.Retain); !ok {
				t.Fatalf("Evaluate = %#v, want Retain", got)
			}
			return
		}
		tr, ok := got.(alarm.Transition)
		if !ok || tr.To != to {
			t.Fatalf("Evaluate = %#v, want a Transition to %s", got, to)
		}
		if !slices.Equal(tr.Evidence.Bins, ev.Bins) || !slices.Equal(tr.Evidence.Used, ev.Used) ||
			tr.Evidence.Filled != ev.Filled || tr.Evidence.Breaching != ev.Breaching {
			t.Fatalf("Evidence = %+v\nwant       %+v", tr.Evidence, ev)
		}
	})
}

// FormatNumber prints a number that parses back to itself and always has a
// point or an exponent, as Java's Double.toString does
// (https://docs.oracle.com/en/java/javase/21/docs/api/java.base/java/lang/Double.html#toString(double);
// cwa 4.3, cloudwatch.alarm.number-format).
func TestFormatNumberRoundTrips(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.number-format", "java-double,event-configuration-plain-whole-numbers")
	rapid.Check(t, func(t *rapid.T) {
		v := rapid.Float64().Filter(func(f float64) bool { return !math.IsInf(f, 0) && !math.IsNaN(f) }).Draw(t, "v")
		s := alarm.FormatNumber(v)
		got, err := strconv.ParseFloat(s, 64)
		if err != nil || got != v {
			t.Fatalf("FormatNumber(%v) = %q, which parses to %v %v", v, s, got, err)
		}
		if !strings.ContainsAny(s, ".E") || strings.Contains(s, "+") || strings.Contains(s, "e") {
			t.Fatalf("FormatNumber(%v) = %q, not a Java double", v, s)
		}
	})
}

// Every generated StateReason and StateReasonData respects the model's 1023
// and 4000 character limits (model com.amazonaws.cloudwatch#StateReason,
// #StateReasonData; cwa 12.5), and StateReasonData is one JSON object.
func TestReasonTextsRespectTheLimits(t *testing.T) {
	if alarm.MaxReason != 1023 || alarm.MaxReasonData != 4000 {
		t.Fatalf("MaxReason, MaxReasonData = %d, %d; want 1023, 4000 (model)", alarm.MaxReason, alarm.MaxReasonData)
	}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 100).Draw(t, "N")
		cfg := alarm.Config{
			Period: 60, N: n, M: rapid.IntRange(1, n).Draw(t, "M"), MSet: rapid.Bool().Draw(t, "MSet"),
			Comparison: rapid.SampledFrom(comparisons).Draw(t, "comparison"),
			Threshold:  rapid.Float64Range(-1e9, 1e9).Draw(t, "threshold"),
			Treatment:  rapid.SampledFrom(treatments).Draw(t, "treatment"),
			Statistic:  cloudwatchapi.StatisticAverage,
		}
		bins := make([]alarm.Bin, n+2)
		for k := range bins {
			start := t0.Add(-time.Duration(k+1) * time.Minute)
			if rapid.IntRange(0, 4).Draw(t, "missing") == 0 {
				bins[k] = alarm.Missing{Start: start}
				continue
			}
			bins[k] = alarm.Value{Start: start, Value: rapid.Float64Range(-1e12, 1e12).Draw(t, "value"), SampleCount: 1e6}
		}
		current := rapid.SampledFrom(states).Draw(t, "current")
		tr, ok := alarm.Evaluate(cfg, bins, current).(alarm.Transition)
		if !ok {
			return
		}
		if r := alarm.Reason(cfg, tr); len(r) > alarm.MaxReason || r == "" {
			t.Fatalf("Reason is %d characters: %q", len(r), r)
		}
		data := alarm.ReasonData(cfg, tr, t0.Add(1234*time.Millisecond))
		if len(data) > alarm.MaxReasonData {
			t.Fatalf("ReasonData is %d characters", len(data))
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			t.Fatalf("ReasonData is not a JSON object: %v: %s", err, data)
		}
	})
}
