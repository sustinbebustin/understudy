package logsfilter

// Options selects the behaviors that AWS has not been recorded deciding. The
// zero value is the documented default for each. The logs service lifts each
// field into a fidelity switch with the metadata Switches returns; this
// package stays a leaf and does not import the fidelity ledger.
type Options struct {
	// TermBoundary decides how an unstructured term or phrase is found in a
	// message. Switch: logs.filter.term-boundary.
	TermBoundary TermBoundary
	// FieldCount decides how a space-delimited pattern treats an event with
	// more fields than the pattern names. Switch:
	// logs.filter.delimited-field-count.
	FieldCount FieldCount
}

// Switch is the fidelity-ledger metadata of one Options field. The logs
// service turns it into a fidelity.Switch.
type Switch struct {
	// Name is the switch name.
	Name string
	// Value is the current setting, as the String methods print it.
	Value string
	// Ref is the research section that documents the open question.
	Ref string
	// Record is the backlog item of the human recording that settles it.
	Record string
}

const (
	termBoundarySwitch = "logs.filter.term-boundary"
	fieldCountSwitch   = "logs.filter.delimited-field-count"
)

// Switches returns the metadata of the two Options fields as o sets them.
func (o Options) Switches() []Switch {
	return []Switch{
		{Name: termBoundarySwitch, Value: o.TermBoundary.String(), Ref: "docs/research/phase1/logs-filter-patterns.md#5", Record: "R10/C1"},
		{Name: fieldCountSwitch, Value: o.FieldCount.String(), Ref: "docs/research/phase1/logs-filter-patterns.md#3", Record: "R10/C6"},
	}
}

// TermBoundary is the rule that decides whether an unstructured term occurs
// in a message (logs-filter-patterns.md section 5).
type TermBoundary uint8

const (
	// TermSubstring is rule S, the default: a term matches wherever its text
	// occurs as a case-sensitive substring, with no word boundary. The one AWS
	// example that separates the candidates (Exception inside
	// InvalidQueryException) favors it.
	TermSubstring TermBoundary = iota
	// TermToken is rule T: message and term are cut into tokens at every
	// character outside [A-Za-z0-9_], and the term's tokens must occur as a
	// contiguous run of whole message tokens.
	TermToken
)

// String returns the switch value as the coverage report prints it.
func (b TermBoundary) String() string {
	switch b {
	case TermSubstring:
		return "substring"
	case TermToken:
		return "token"
	}
	return "unknown"
}

// FieldCount is the rule for space-delimited events with more fields than
// the pattern names when the pattern has no "..." (logs-filter-patterns.md
// rule N6).
type FieldCount uint8

const (
	// FieldCountLastAbsorbs is the default: an event needs at least as many
	// fields as the pattern, and the last pattern field takes the rest of the
	// event. AWS's own [w1=ERROR, w2] example only works under this rule.
	FieldCountLastAbsorbs FieldCount = iota
	// FieldCountExact requires exactly as many event fields as pattern fields.
	FieldCountExact
)

// String returns the switch value as the coverage report prints it.
func (c FieldCount) String() string {
	switch c {
	case FieldCountLastAbsorbs:
		return "last-absorbs"
	case FieldCountExact:
		return "exact"
	}
	return "unknown"
}
