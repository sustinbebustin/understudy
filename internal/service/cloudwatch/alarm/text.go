package alarm

import "time"

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
func FormatNumber(_ float64) string {
	// not implemented yet
	return ""
}

// FormatConfigNumber prints a number inside an EventBridge configuration
// object: whole numbers without ".0", others as FormatNumber
// (cloudwatch.alarm.number-format; cloudwatch-alarms.md 4.3).
func FormatConfigNumber(_ float64) string {
	// not implemented yet
	return ""
}

// FormatReasonDate prints a datapoint's timestamp inside a StateReason:
// dd/MM/yy HH:mm:ss in UTC (cloudwatch-alarms.md 4.3).
func FormatReasonDate(_ time.Time) string {
	// not implemented yet
	return ""
}

// FormatTimestamp prints a timestamp inside JSON: yyyy-MM-ddTHH:mm:ss.SSS+0000
// in UTC (cloudwatch-alarms.md 4.3).
func FormatTimestamp(_ time.Time) string {
	// not implemented yet
	return ""
}

// Reason is a transition's StateReason (cloudwatch-alarms.md 4.1, 4.3),
// at most MaxReason characters.
func Reason(_ Config, _ Transition) string {
	// not implemented yet
	return ""
}

// ReasonData is a transition's StateReasonData JSON for an evaluation at
// queryDate (cloudwatch-alarms.md 4.2, 4.3), at most MaxReasonData
// characters.
func ReasonData(_ Config, _ Transition, _ time.Time) string {
	// not implemented yet
	return ""
}
