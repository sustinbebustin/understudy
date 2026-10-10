package registry

// BindCoverage is what a bind site states about the handler it binds. The
// kernel reports an operation's status as Bound with this value, or Unbound.
//
//sumtype:decl
type BindCoverage interface{ isBindCoverage() }

// Full is an operation implemented as AWS behaves, as far as the research
// knows.
type Full struct{}

// StoredOnly is an operation that accepts, stores and echoes its input with
// no runtime effect (queue policies, concurrency settings).
type StoredOnly struct{ Note string }

// Partial is an operation implemented with named gaps.
type Partial struct{ Note string }

func (Full) isBindCoverage()       {}
func (StoredOnly) isBindCoverage() {}
func (Partial) isBindCoverage()    {}
