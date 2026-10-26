package policydoc

import (
	"slices"
	"strings"
)

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
	// keys holds every key set, by its lower-cased name.
	keys map[string]contextEntry
	// families are the lower-cased prefixes UnsuppliedFamily marked.
	families []string
}

// contextEntry is what one Set, SetMulti, Absent or Unsupplied call recorded.
type contextEntry struct {
	presence Presence
	value    ContextValue
}

// NewContext returns an empty context for a request whose action has the IAM
// prefix service: a key of that prefix that is never set is Unsupplied, any
// other key never set is Absent. NewContext("") makes every unset key
// Absent.
func NewContext(service string) *Context {
	return &Context{service: service, keys: map[string]contextEntry{}}
}

// Set makes key present and single-valued with value.
func (c *Context) Set(key, value string) {
	c.put(key, contextEntry{presence: Present, value: ContextValue{Values: []string{value}}})
}

// SetMulti makes key present and multivalued with values, which may be none.
func (c *Context) SetMulti(key string, values ...string) {
	c.put(key, contextEntry{presence: Present, value: ContextValue{Values: append([]string{}, values...), Multivalued: true}})
}

// Absent records that the request does not carry key.
func (c *Context) Absent(key string) {
	c.put(key, contextEntry{presence: Absent})
}

// Unsupplied records that AWS would send key with this request and
// understudy cannot compute it.
func (c *Context) Unsupplied(key string) {
	c.put(key, contextEntry{presence: Unsupplied})
}

// UnsuppliedFamily makes every key that begins with prefix, such as
// "aws:ResourceTag/", and is not set Unsupplied.
func (c *Context) UnsuppliedFamily(prefix string) {
	c.families = append(c.families, strings.ToLower(prefix))
}

// Lookup reports key's presence and, when Present, its value. A nil *Context
// answers Absent. A key set by name wins over its family and its service.
func (c *Context) Lookup(key string) (ContextValue, Presence) {
	if c == nil {
		return ContextValue{}, Absent
	}
	name := strings.ToLower(key)
	if e, ok := c.keys[name]; ok {
		if e.presence != Present {
			return ContextValue{}, e.presence
		}
		return ContextValue{Values: slices.Clone(e.value.Values), Multivalued: e.value.Multivalued}, Present
	}
	if slices.ContainsFunc(c.families, func(prefix string) bool { return strings.HasPrefix(name, prefix) }) {
		return ContextValue{}, Unsupplied
	}
	if prefix, _, ok := strings.Cut(key, ":"); ok && c.service != "" && strings.EqualFold(prefix, c.service) {
		return ContextValue{}, Unsupplied
	}
	return ContextValue{}, Absent
}

func (c *Context) put(key string, e contextEntry) {
	if c.keys == nil {
		c.keys = map[string]contextEntry{}
	}
	c.keys[strings.ToLower(key)] = e
}
