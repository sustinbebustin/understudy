package alarm

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
)

// The longest StateReason and StateReasonData AWS stores (model
// com.amazonaws.cloudwatch#StateReason, #StateReasonData); generated texts
// respect both (cloudwatch-alarms.md 12.5).
const (
	MaxReason     = 1023
	MaxReasonData = 4000
)

// FormatNumber prints a double as Java's Double.toString does: a whole
// number keeps ".0", others the shortest round-trip digits, and values at
// or above 10^7 or below 10^-3 in Java's E notation
// (cloudwatch.alarm.number-format; cloudwatch-alarms.md 4.3).
func FormatNumber(v float64) string {
	if a := math.Abs(v); a == 0 || a >= 1e-3 && a < 1e7 {
		s := strconv.FormatFloat(v, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	// Go prints 1.5e+07 and 1e-04; Java 1.5E7 and 1.0E-4.
	mantissa, exp, _ := strings.Cut(strconv.FormatFloat(v, 'e', -1, 64), "e")
	if !strings.Contains(mantissa, ".") {
		mantissa += ".0"
	}
	sign, digits := "", exp
	switch exp[0] {
	case '-':
		sign, digits = "-", exp[1:]
	case '+':
		digits = exp[1:]
	}
	return mantissa + "E" + sign + strings.TrimLeft(digits, "0")
}

// FormatConfigNumber prints a number inside an EventBridge configuration
// object: whole numbers without ".0", others as FormatNumber
// (cloudwatch.alarm.number-format; cloudwatch-alarms.md 4.3).
func FormatConfigNumber(v float64) string {
	if v == math.Trunc(v) && !math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return FormatNumber(v)
}

// FormatReasonDate prints a datapoint's timestamp inside a StateReason:
// dd/MM/yy HH:mm:ss in UTC (cloudwatch-alarms.md 4.3).
func FormatReasonDate(t time.Time) string {
	return t.UTC().Format("02/01/06 15:04:05")
}

// FormatTimestamp prints a timestamp inside JSON: yyyy-MM-ddTHH:mm:ss.SSS+0000
// in UTC (cloudwatch-alarms.md 4.3).
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "+0000"
}

// Reason is a transition's StateReason (cloudwatch-alarms.md 4.1, 4.3),
// at most MaxReason characters. The form follows
// cloudwatch.alarm.reason-format: the M out of N form when the alarm sent
// DatapointsToAlarm, the count form otherwise (CWA-06). It lists the real
// datapoints that cross in the transition's direction; under breaching or
// notBreaching the filled points add "and <F> missing datapoints were
// treated as [Breaching]", and with no real datapoint to list the text
// counts the received ones instead (CWA-01, CWA-04, CWA-08). When the
// listing would pass MaxReason, a second sentence lists the five newest
// (CWA-10).
func Reason(cfg Config, t Transition) string {
	if t.To == cloudwatchapi.StateValueInsufficientData {
		return "Insufficient Data: " + plural(cfg.N, "datapoint was", "datapoints were") + " unknown."
	}
	alarming := t.To == cloudwatchapi.StateValueAlarm
	listed := crossing(cfg, t)
	filled := ""
	if f := t.Evidence.Filled; f > 0 && (cfg.Treatment == TreatBreaching || cfg.Treatment == TreatNotBreaching) {
		label := "[NonBreaching]"
		if cfg.Treatment == TreatBreaching {
			label = "[Breaching]"
		}
		filled = " and " + plural(f, "missing datapoint was", "missing datapoints were") + " treated as " + label
	}
	if len(listed) == 0 {
		received := "no datapoints were"
		if n := len(t.Evidence.Used); n > 0 {
			received = plural(n, "datapoint was", "datapoints were")
		}
		return "Threshold Crossed: " + received + " received for " + plural(cfg.N, "period", "periods") + filled + "."
	}
	k := len(listed)
	neg := "not "
	if alarming {
		neg = ""
	}
	verb := " were "
	if k == 1 {
		verb = " was "
	}
	head := "Threshold Crossed: " + plural(k, "datapoint", "datapoints")
	crossed := verb + neg + cfg.Comparison.Phrase() + " the threshold (" + FormatNumber(cfg.Threshold) + ")" + filled
	minimum := ""
	if cfg.MSet {
		m, dir := cfg.M, "OK -> ALARM"
		if !alarming {
			m, dir = cfg.N-cfg.M+1, "ALARM -> OK"
		}
		head = "Threshold Crossed: " + strconv.Itoa(k) + " out of the last " + strconv.Itoa(cfg.N) + " datapoints"
		minimum = " (minimum " + plural(m, "datapoint", "datapoints") + " for " + dir + " transition)"
	}
	entries := make([]string, len(listed))
	for i, v := range listed {
		entries[i] = FormatNumber(v.Value) + " (" + FormatReasonDate(v.Start) + ")"
	}
	if s := head + " [" + strings.Join(entries, ", ") + "]" + crossed + minimum + "."; len(s) <= MaxReason {
		return s
	}
	recent := entries[:min(len(entries), 5)]
	which := "datapoints which"
	if len(recent) == 1 {
		which = "datapoint which"
	}
	s := head + crossed + ". The most recent " + which + " crossed the threshold: [" + strings.Join(recent, ", ") + "]" + minimum + "."
	if len(s) > MaxReason {
		s = cut(s, len(s)-MaxReason)
	}
	return s
}

// crossing is the real datapoints of a transition that cross in its
// direction, newest first: breaching ones for ALARM, the others for OK.
func crossing(cfg Config, t Transition) []Value {
	alarming := t.To == cloudwatchapi.StateValueAlarm
	var out []Value
	for _, v := range t.Evidence.Used {
		if cfg.Comparison.Breaches(v.Value, cfg.Threshold) == alarming {
			out = append(out, v)
		}
	}
	return out
}

// plural is "<n> <one>" or "<n> <many>".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// ReasonData is a transition's StateReasonData JSON for an evaluation at
// queryDate (cloudwatch-alarms.md 4.2, 4.3), at most MaxReasonData
// characters. recentDatapoints spans the used bins oldest first, a missing
// bin between them as null; evaluatedDatapoints lists the bins the
// decision rests on (decisive) (CWA-01, CWA-04, CWA-06, CWA-08, CWA-10).
// Past MaxReasonData the oldest evaluated datapoints are dropped, then the
// oldest recent ones; how AWS shortens them is UNVERIFIED (CWA-10).
func ReasonData(cfg Config, t Transition, queryDate time.Time) string {
	used := t.Evidence.Used
	head := `{"version":"1.0","queryDate":"` + FormatTimestamp(queryDate) + `"`
	if len(used) > 0 {
		head += `,"startDate":"` + FormatTimestamp(used[len(used)-1].Start) + `"`
	}
	if cfg.Unit != nil {
		head += `,"unit":` + strconv.Quote(string(*cfg.Unit))
	}
	head += `,"statistic":` + strconv.Quote(string(cfg.Statistic)) + `,"period":` + strconv.Itoa(int(cfg.Period))

	recent := recentDatapoints(t.Evidence)
	evaluated := evaluatedDatapoints(decisive(cfg, t))
	for {
		s := head + `,"recentDatapoints":[` + strings.Join(recent, ",") + `],"threshold":` + FormatNumber(cfg.Threshold) +
			`,"evaluatedDatapoints":[` + strings.Join(evaluated, ",") + `]}`
		switch {
		case len(s) <= MaxReasonData:
			return s
		case len(evaluated) > 0:
			evaluated = evaluated[:len(evaluated)-1]
		case len(recent) > 0:
			recent = recent[1:]
		default:
			return s
		}
	}
}

// recentDatapoints renders the bins from the oldest used one to the newest,
// oldest first, a Missing bin as null: [5.0,null,5.0] (CWA-04, CWA-10).
func recentDatapoints(ev Evidence) []string {
	if len(ev.Used) == 0 {
		return nil
	}
	newest, oldest := ev.Used[0].Start, ev.Used[len(ev.Used)-1].Start
	var out []string
	for _, v := range slices.Backward(ev.Bins) {
		switch b := v.(type) {
		case Missing:
			if b.Start.After(oldest) && b.Start.Before(newest) {
				out = append(out, "null")
			}
		case Value:
			if !b.Start.Before(oldest) && !b.Start.After(newest) {
				out = append(out, FormatNumber(b.Value))
			}
		}
	}
	return out
}

// decisive is the bins a transition rests on, newest first: every real
// datapoint that crosses in its direction (crossing), then the newest
// filled bins that count the same way until there are M for ALARM, N - M + 1
// for OK or N for INSUFFICIENT_DATA. The filled bins are the Filled newest
// Missing ones; under missing and ignore they count either way, under
// breaching only for ALARM and under notBreaching only for OK (CWA-01,
// CWA-04, CWA-08). With more crossing real datapoints than that, which AWS
// lists is UNVERIFIED; understudy lists them all.
func decisive(cfg Config, t Transition) []Bin {
	need := cfg.N
	switch t.To {
	case cloudwatchapi.StateValueAlarm:
		need = cfg.M
	case cloudwatchapi.StateValueOk:
		need = cfg.N - cfg.M + 1
	case cloudwatchapi.StateValueInsufficientData:
	}
	fillCounts := true
	switch cfg.Treatment {
	case TreatBreaching:
		fillCounts = t.To == cloudwatchapi.StateValueAlarm
	case TreatNotBreaching:
		fillCounts = t.To == cloudwatchapi.StateValueOk
	case TreatMissing, TreatIgnore:
	}
	picked := make([]bool, len(t.Evidence.Bins))
	n := 0
	for _, v := range crossing(cfg, t) {
		for i, b := range t.Evidence.Bins {
			if bv, ok := b.(Value); ok && bv.Start.Equal(v.Start) {
				picked[i] = true
				n++
			}
		}
	}
	filled := t.Evidence.Filled
	for i, b := range t.Evidence.Bins {
		if filled == 0 {
			break
		}
		if _, ok := b.(Missing); !ok {
			continue
		}
		filled--
		if fillCounts && n < need {
			picked[i] = true
			n++
		}
	}
	var out []Bin
	for i, b := range t.Evidence.Bins {
		if picked[i] {
			out = append(out, b)
		}
	}
	return out
}

// evaluatedDatapoints renders bins as evaluatedDatapoints entries: a
// Missing bin as its timestamp alone, a Value with its sampleCount and
// value.
func evaluatedDatapoints(bins []Bin) []string {
	out := make([]string, len(bins))
	for i, b := range bins {
		switch b := b.(type) {
		case Missing:
			out[i] = `{"timestamp":"` + FormatTimestamp(b.Start) + `"}`
		case Value:
			out[i] = `{"timestamp":"` + FormatTimestamp(b.Start) + `","sampleCount":` + FormatNumber(b.SampleCount) +
				`,"value":` + FormatNumber(b.Value) + `}`
		}
	}
	return out
}
