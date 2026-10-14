package fault

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	mathrand "math/rand/v2"
	"slices"
	"strings"
	"sync"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Store holds the active rules in the order they were added and evaluates
// them. It is safe for concurrent use; its mutex is a leaf lock that never
// calls out.
type Store struct {
	seed *uint64
	ids  *ids.Source
	cat  Catalogue

	mu    sync.Mutex
	rules []*entry
}

// entry is one active rule with its evaluation state.
type entry struct {
	rule     Rule
	resolved resolved
	hits     int
	stream   *stream // nil unless the trigger is Probability
}

// stream is a rule's Probability source: ChaCha8 keyed by the store seed and
// the rule id, with the count of 64-bit words drawn.
type stream struct {
	rng *mathrand.ChaCha8
	pos uint64
}

// Active is a rule in the store with the hits it has taken; a NextN rule has
// N - Hits hits left.
type Active struct {
	Rule Rule
	Hits int
}

// ruleIDBytes is the number of random bytes in a minted rule id.
const ruleIDBytes = 6

// maxRuleIDLen bounds a rule id; ids appear in admin API paths.
const maxRuleIDLen = 64

// New returns an empty store. With a seed, Probability streams are keyed by
// hash(seed, fault-stream kind, rule id), so the same rules and call order
// give the same hits; without one they are keyed from crypto/rand. source
// mints the ids of rules added without one and must not be nil; rules are
// validated against cat.
func New(seed *uint64, source *ids.Source, cat Catalogue) *Store {
	s := &Store{ids: source, cat: cat}
	if seed != nil {
		v := *seed
		s.seed = &v
	}
	return s
}

// Add validates r and makes it active, returning its id: r.ID, or a minted
// one when r.ID is empty. A rule with the id of an active rule replaces it in
// place, with its hits and stream restarted. The store keeps its own copy of
// r, so later changes to r's slices do not reach the active rule.
func (s *Store) Add(r Rule) (ref.RuleID, *RuleError) {
	r.Match = r.Match.clone()
	res, err := resolve(s.cat, r)
	if err != nil {
		return "", err
	}
	if r.ID != "" {
		e := &entry{rule: r, resolved: res, stream: s.newStream(r)}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.put(e)
		return r.ID, nil
	}
	for {
		// The ids source takes its own lock, so the candidate is drawn
		// before s.mu, which stays a leaf; a taken id draws again.
		r.ID = s.mintID()
		if s.putNew(&entry{rule: r, resolved: res, stream: s.newStream(r)}) {
			return r.ID, nil
		}
	}
}

// Remove deactivates the rule with id and reports whether it was active.
func (s *Store) Remove(id ref.RuleID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return false
	}
	s.rules = slices.Delete(s.rules, i, i+1)
	return true
}

// List returns the active rules in evaluation order. A NextN rule leaves the
// list on its last hit. The rules are copies the caller may change.
func (s *Store) List() []Active {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Active, len(s.rules))
	for i, e := range s.rules {
		r := e.rule
		r.Match = r.Match.clone()
		out[i] = Active{Rule: r, Hits: e.hits}
	}
	return out
}

// Clear deactivates every rule and returns how many there were.
func (s *Store) Clear() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.rules)
	s.rules = nil
	return n
}

// Evaluate applies the active rules that belong to hook h to the call in.
// Rules are tried in order; the first one that matches and fires decides,
// except that a Latency rule's Delay goes on to the rules after it. Every
// firing is a hit: it advances the rule's count, spends one unit of a NextN
// budget and is reported in the Decision. A rule of another hook is skipped
// untouched, so an AfterCommit budget is spent only at AfterHandler, after
// the handler succeeded. Calls with Origin Internal{Via: Snapshot} never
// match.
func (s *Store) Evaluate(h Hook, in MatchInput) Decision {
	if o, ok := in.Origin.(ref.Internal); ok && o.Via == ref.Snapshot {
		return None{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evaluateFrom(0, h, in)
}

// put inserts e, replacing the active rule with its id in place.
func (s *Store) put(e *entry) {
	if i := s.index(e.rule.ID); i >= 0 {
		s.rules[i] = e
		return
	}
	s.rules = append(s.rules, e)
}

// putNew appends e unless its id is active, and reports whether it did.
func (s *Store) putNew(e *entry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index(e.rule.ID) >= 0 {
		return false
	}
	s.rules = append(s.rules, e)
	return true
}

// mintID draws a candidate rule id; the caller checks it is free.
func (s *Store) mintID() ref.RuleID {
	return ref.RuleID("fault-" + hex.EncodeToString(s.ids.Draw(ids.KindFaultStream, ruleIDBytes)))
}

func (s *Store) index(id ref.RuleID) int {
	return slices.IndexFunc(s.rules, func(e *entry) bool { return e.rule.ID == id })
}

// newStream returns the Probability stream for r, or nil for other triggers.
func (s *Store) newStream(r Rule) *stream {
	if _, ok := r.Trigger.(Probability); !ok {
		return nil
	}
	return &stream{rng: mathrand.NewChaCha8(s.streamKey(r.ID))}
}

// streamKey is hash(seed, fault-stream kind, rule id) for a seeded store and
// 32 random bytes otherwise.
func (s *Store) streamKey(id ref.RuleID) [32]byte {
	var key [32]byte
	if s.seed == nil {
		_, _ = rand.Read(key[:]) // crypto/rand.Read never returns an error; it crashes the program instead
		return key
	}
	h := sha256.New()
	h.Write([]byte("understudy/fault/v1\x00"))
	h.Write([]byte(ids.KindFaultStream.String()))
	h.Write([]byte{0})
	h.Write(binary.LittleEndian.AppendUint64(nil, *s.seed))
	h.Write([]byte(id))
	return [32]byte(h.Sum(nil))
}

func (s *Store) evaluateFrom(start int, h Hook, in MatchInput) Decision {
	for i := start; i < len(s.rules); i++ {
		e := s.rules[i]
		if !h.runs(e.rule) || !e.rule.Match.matches(in) {
			continue
		}
		id, ok := e.resolved.identity(e.rule.Action, in)
		if !ok || !e.fires() {
			continue
		}
		e.hits++
		inj := Injection{Rule: e.rule.ID, Hit: e.hits}
		next := i + 1
		if n, ok := e.rule.Trigger.(NextN); ok {
			inj.Of = n.N
			if e.hits >= n.N {
				s.rules = slices.Delete(s.rules, i, i+1)
				next = i
			}
		}
		return s.decide(e, id, inj, next, h, in)
	}
	return None{}
}

// decide builds the decision for a hit of e; next is where the rules after e
// start, for a Delay.
func (s *Store) decide(e *entry, id awserr.Identity, inj Injection, next int, h Hook, in MatchInput) Decision {
	switch a := e.rule.Action.(type) {
	case Error:
		return Fail{Err: injectedError(id, inj.Rule, a.Message), Injection: inj}
	case Throttle:
		return Fail{Err: injectedError(id, inj.Rule, ""), Injection: inj}
	case AfterCommit:
		return FailAfterCommit{Err: injectedError(id, inj.Rule, ""), Injection: inj}
	case StatusOKError:
		return FailStatusOK{Err: injectedError(id, inj.Rule, ""), Injection: inj}
	case Latency:
		return Delay{Wall: a.Wall, Then: s.evaluateFrom(next, h, in), Injection: inj}
	case DropDelivery:
		return Drop{Injection: inj}
	default:
		return None{}
	}
}

// fires draws the trigger for a matched call.
func (e *entry) fires() bool {
	switch t := e.rule.Trigger.(type) {
	case Always, NextN:
		return true
	case Probability:
		return e.stream.draw() < t.P
	default:
		return false
	}
}

// draw returns a uniform float64 in [0, 1) from 53 random bits.
func (st *stream) draw() float64 {
	st.pos++
	return float64(st.rng.Uint64()>>11) / (1 << 53)
}

// seek replays the stream to pos words drawn.
func (st *stream) seek(pos uint64) {
	for range pos {
		st.draw()
	}
}

// validRuleID reports whether id fits the rule id charset and length and is
// not made only of dots, which net/http would clean out of PUT and DELETE
// /faults/{id}.
func validRuleID(id ref.RuleID) bool {
	if id == "" || len(id) > maxRuleIDLen || strings.Trim(string(id), ".") == "" {
		return false
	}
	for _, c := range []byte(id) {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// injectedError renders an injected identity with message, or with a
// message naming the rule when message is empty and the shape has one.
func injectedError(id awserr.Identity, rule ref.RuleID, message string) *awserr.Error {
	if message == "" && id.MessageKey != awserr.NoMessage {
		message = "Injected by understudy fault rule " + string(rule) + "."
	}
	return &awserr.Error{ID: id, Message: message, Origin: awserr.Injected{Rule: rule}}
}
