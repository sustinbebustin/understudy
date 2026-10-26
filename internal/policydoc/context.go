package policydoc

// Presence is how a condition key stands in a request context.
type Presence uint8

// Presence values.
const (
	// Absent is a key the request does not carry: AWS would not send it.
	Absent Presence = iota
	// Present is a key the request carries, with its values.
	Present
	// Unsupplied is a key AWS would send with the request that understudy
	// cannot compute. It evaluates as Absent, and Evaluate reports every
	// statement that tests it (iam-enforcement-surfaces.md 12.2).
	Unsupplied
)

// ContextValue is a present key's value.
type ContextValue struct {
	Values []string
	// Multivalued marks a key the request context defines as multivalued,
	// such as aws:TagKeys; a single-valued key holds exactly one value
	// (iam-evaluation.md 6.12).
	Multivalued bool
}

// Context is a request context's condition keys (iam-evaluation.md 2.1,
// 6.2). Names match case-insensitively, and a later call for a key replaces
// an earlier one.
type Context struct {
	// service is the IAM prefix whose keys are Unsupplied when never set.
	service string
}

// NewContext returns an empty context for a request whose action has the IAM
// prefix service: a key of that prefix that is never set is Unsupplied, any
// other key never set is Absent. NewContext("") makes every unset key
// Absent.
//
// Not implemented yet: the context it returns answers Absent for every key.
func NewContext(service string) *Context {
	return &Context{service: service}
}

// Set makes key present and single-valued with value.
//
// Not implemented yet: it records nothing.
func (c *Context) Set(_, _ string) {}

// SetMulti makes key present and multivalued with values, which may be none.
//
// Not implemented yet: it records nothing.
func (c *Context) SetMulti(_ string, _ ...string) {}

// Absent records that the request does not carry key.
//
// Not implemented yet: it records nothing.
func (c *Context) Absent(_ string) {}

// Unsupplied records that AWS would send key with this request and
// understudy cannot compute it.
//
// Not implemented yet: it records nothing.
func (c *Context) Unsupplied(_ string) {}

// UnsuppliedFamily makes every key that begins with prefix, such as
// "aws:ResourceTag/", and is not set Unsupplied.
//
// Not implemented yet: it records nothing.
func (c *Context) UnsuppliedFamily(_ string) {}

// Lookup reports key's presence and, when Present, its value. A nil *Context
// answers Absent.
//
// Not implemented yet: it answers Absent.
func (c *Context) Lookup(_ string) (ContextValue, Presence) {
	return ContextValue{}, Absent
}
