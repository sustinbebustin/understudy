package fault

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// exportVersion is the schema version of the Export document; Import refuses
// any other.
const exportVersion = 1

// exportJSON is the Export document: the active rules in evaluation order,
// each with its hits and, for a Probability rule, its stream position.
type exportJSON struct {
	Version int          `json:"version"`
	Rules   []activeJSON `json:"rules"`
}

type activeJSON struct {
	Rule Rule `json:"rule"`
	Hits int  `json:"hits,omitzero"`
	// Position is the number of 64-bit words drawn from a Probability rule's
	// stream.
	Position uint64 `json:"position,omitzero"`
}

// ImportError reports a document Import refused. The active rules are
// unchanged.
type ImportError struct {
	// Index is the rule's position in the document, or -1 when the document
	// itself is at fault.
	Index  int
	Rule   ref.RuleID
	Reason string
	// Cause is the decode error or *RuleError behind Reason, if any.
	Cause error
}

// Error names the refused rule or document and why, and says the active
// rules are unchanged.
func (e *ImportError) Error() string {
	where := "the document"
	if e.Index >= 0 {
		where = fmt.Sprintf("rule %d (%q)", e.Index, e.Rule)
	}
	msg := "fault rules not imported, the active rules are unchanged: " + where + ": " + e.Reason
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap returns Cause, so errors.As finds a *RuleError behind the refusal.
func (e *ImportError) Unwrap() error { return e.Cause }

// ExportError reports active rules whose Probability streams have drawn more
// words in total than Import accepts, so Export wrote nothing rather than a
// document that cannot be loaded. The rules are unchanged and keep
// evaluating normally.
type ExportError struct {
	// Rule is the rule whose stream position brings the total above
	// ids.MaxPosition, and Position is that position.
	Rule     ref.RuleID
	Position uint64
}

// Error names the rule whose stream is too far along and how to recover.
func (e *ExportError) Error() string {
	return fmt.Sprintf("fault rules not exported: probability rule %q has drawn %d words from its stream, which brings the active rules' total above the %d a snapshot can restore; the rules, their hits and their streams are unchanged and keep evaluating normally (add each probability rule again with its id to restart its stream, then save again)",
		e.Rule, e.Position, uint64(ids.MaxPosition))
}

// Export encodes the active rules with their hits and stream positions, for
// snapshots. The same store state gives the same bytes. It refuses with
// *ExportError when the stream positions add up to more than Import accepts,
// so a snapshot is never written that Import would refuse.
func (s *Store) Export() ([]byte, error) {
	doc, err := s.exportDoc()
	if err != nil {
		return nil, err
	}
	return jsonx.Marshal(doc)
}

// exportDoc copies the active rules into an Export document under the lock.
func (s *Store) exportDoc() (exportJSON, *ExportError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := exportJSON{Version: exportVersion, Rules: make([]activeJSON, len(s.rules))}
	var total uint64
	for i, e := range s.rules {
		doc.Rules[i] = activeJSON{Rule: e.rule, Hits: e.hits}
		if e.stream == nil {
			continue
		}
		var ok bool
		if total, ok = addPosition(total, e.stream.pos); !ok {
			return exportJSON{}, &ExportError{Rule: e.rule.ID, Position: e.stream.pos}
		}
		doc.Rules[i].Position = e.stream.pos
	}
	return doc, nil
}

// addPosition adds a rule's stream position to total, the positions of the
// rules before it, and reports false when the sum would exceed
// ids.MaxPosition, the most Import replays. total must be at most
// ids.MaxPosition, which every sum addPosition accepts is.
func addPosition(total, pos uint64) (uint64, bool) {
	if pos > ids.MaxPosition-total {
		return total, false
	}
	return total + pos, true
}

// Import replaces the active rules with those of an Export document,
// validating each against the catalogue. NextN budgets resume where they
// were; a seeded store replays each Probability stream to its position, so
// the hits that follow are those that followed the export. An unseeded store
// keys fresh random streams. The stream positions of the whole document add
// up to at most ids.MaxPosition, so a corrupt document cannot stall the load.
// On error nothing changes.
func (s *Store) Import(data []byte) error {
	var doc exportJSON
	if err := jsonx.Unmarshal(data, &doc); err != nil {
		return &ImportError{Index: -1, Reason: "it is not a fault rules document", Cause: err}
	}
	if doc.Version != exportVersion {
		return &ImportError{Index: -1, Reason: fmt.Sprintf("version %d is not the version %d this binary reads", doc.Version, exportVersion)}
	}
	if err := checkReplay(doc.Rules); err != nil {
		return err
	}
	entries := make([]*entry, 0, len(doc.Rules))
	seen := make(map[ref.RuleID]bool, len(doc.Rules))
	for i := range doc.Rules {
		a := &doc.Rules[i]
		e, err := s.restore(a, seen)
		if err != nil {
			err.Index, err.Rule = i, a.Rule.ID
			return err
		}
		seen[a.Rule.ID] = true
		entries = append(entries, e)
	}
	s.mu.Lock()
	s.rules = entries
	s.mu.Unlock()
	return nil
}

// checkReplay refuses rules whose stream positions add up to more than
// ids.MaxPosition, before any stream is replayed.
func checkReplay(rules []activeJSON) *ImportError {
	var total uint64
	for i := range rules {
		a := &rules[i]
		var ok bool
		if total, ok = addPosition(total, a.Position); !ok {
			return &ImportError{Index: i, Rule: a.Rule.ID, Reason: fmt.Sprintf(
				"stream position %d brings the document's total above the maximum %d; the document is corrupt or was not written by Export",
				a.Position, uint64(ids.MaxPosition))}
		}
	}
	return nil
}

// restore rebuilds one exported rule; the caller fills the error's Index and
// Rule.
func (s *Store) restore(a *activeJSON, seen map[ref.RuleID]bool) (*entry, *ImportError) {
	r := a.Rule
	switch {
	case r.ID == "":
		return nil, &ImportError{Reason: "it has no id; exported rules always carry one"}
	case seen[r.ID]:
		return nil, &ImportError{Reason: "the id appears twice"}
	case a.Hits < 0:
		return nil, &ImportError{Reason: fmt.Sprintf("hits is %d; it cannot be negative", a.Hits)}
	}
	res, rerr := resolve(s.cat, r)
	if rerr != nil {
		return nil, &ImportError{Reason: "the rule is not valid here", Cause: rerr}
	}
	if n, ok := r.Trigger.(NextN); ok && a.Hits >= n.N {
		return nil, &ImportError{Reason: fmt.Sprintf("it has taken %d of its %d hits, and a spent rule is not active", a.Hits, n.N)}
	}
	st := s.newStream(r)
	switch {
	case st == nil && a.Position != 0:
		return nil, &ImportError{Reason: "it has a stream position but no probability trigger"}
	case st != nil && s.seed != nil:
		st.seek(a.Position)
	case st != nil:
		st.pos = a.Position
	}
	return &entry{rule: r, resolved: res, hits: a.Hits, stream: st}, nil
}
