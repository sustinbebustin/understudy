package alarm

import (
	"math"
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
// DatapointsToAlarm, the count form otherwise (CWA-06). The no-data texts,
// their singular forms and case C's listing of real bins alone are the
// research's defaults (CWA-08). Past MaxReason the oldest listed
// datapoints are dropped and the count stays true (CWA-10).
func Reason(cfg Config, t Transition) string {
	if t.To == cloudwatchapi.StateValueInsufficientData {
		return "Insufficient Data: " + plural(cfg.N, "datapoint was", "datapoints were") + " unknown."
	}
	alarming := t.To == cloudwatchapi.StateValueAlarm
	var listed []Value
	for _, v := range t.Evidence.Used {
		if cfg.Comparison.Breaches(v.Value, cfg.Threshold) == alarming {
			listed = append(listed, v)
		}
	}
	if len(listed) == 0 {
		label := "[NonBreaching]"
		if alarming {
			label = "[Breaching]"
		}
		f := t.Evidence.Filled
		return "Threshold Crossed: no datapoints were received for " + plural(f, "period", "periods") +
			" and " + plural(f, "missing datapoint was", "missing datapoints were") + " treated as " + label + "."
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
	tail := verb + neg + cfg.Comparison.Phrase() + " the threshold (" + FormatNumber(cfg.Threshold) + ")"
	if cfg.MSet {
		m, dir := cfg.M, "OK -> ALARM"
		if !alarming {
			m, dir = cfg.N-cfg.M+1, "ALARM -> OK"
		}
		head = "Threshold Crossed: " + strconv.Itoa(k) + " out of the last " + strconv.Itoa(cfg.N) + " datapoints"
		tail += " (minimum " + plural(m, "datapoint", "datapoints") + " for " + dir + " transition)"
	}
	tail += "."
	entries := make([]string, len(listed))
	for i, v := range listed {
		entries[i] = FormatNumber(v.Value) + " (" + FormatReasonDate(v.Start) + ")"
	}
	for {
		s := head + " [" + strings.Join(entries, ", ") + "]" + tail
		if len(s) <= MaxReason || len(entries) == 0 {
			return s
		}
		entries = entries[:len(entries)-1]
	}
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
// characters. evaluatedDatapoints lists d1 back to the N-th bin or to the
// oldest bin used, whichever is older (CWA-10); past MaxReasonData the
// oldest evaluated datapoints are dropped, then the oldest recent ones
// (CWA-10).
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

	recent := make([]string, len(used))
	for i, v := range used {
		recent[len(used)-1-i] = FormatNumber(v.Value)
	}
	evaluated := evaluatedDatapoints(cfg, t.Evidence)
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

// evaluatedDatapoints renders bins d1..dK newest first, K the larger of N
// and the position of the oldest bin used, within the bins passed.
func evaluatedDatapoints(cfg Config, ev Evidence) []string {
	k := cfg.N
	if len(ev.Used) > 0 {
		oldest := ev.Used[len(ev.Used)-1].Start
		for i, b := range ev.Bins {
			if v, ok := b.(Value); ok && v.Start.Equal(oldest) {
				k = max(k, i+1)
			}
		}
	}
	k = min(k, len(ev.Bins))
	out := make([]string, k)
	for i, b := range ev.Bins[:k] {
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
