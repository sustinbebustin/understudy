package filterpolicy

import "sync"

// maxCached bounds a Cache. It is above the 200 policies a topic may hold
// (sns.md 6.5), so the policies of a few busy topics stay compiled.
const maxCached = 4096

// key is what a compiled policy depends on.
type key struct {
	text  string
	scope Scope
}

// Cache holds compiled policies by text and scope, so a publish does not
// compile its subscriptions' policies again (ARCHITECTURE.md 16: compiled
// once at configuration time). State keeps policy text, which snapshots
// carry; the cache fills when a policy is set and, after a load, on its
// first use. It is safe for concurrent use.
type Cache struct {
	mu sync.Mutex
	// compiled holds at most maxCached policies; when full, an arbitrary
	// one makes room, to be compiled again on its next use.
	compiled map[key]*Policy
}

// NewCache returns an empty cache.
func NewCache() *Cache { return &Cache{compiled: map[key]*Policy{}} }

// Compile is Compile through the cache. A policy that does not compile is
// not kept.
func (c *Cache) Compile(text string, scope Scope) (*Policy, *Error) {
	k := key{text: text, scope: scope}
	c.mu.Lock()
	p, ok := c.compiled[k]
	c.mu.Unlock()
	if ok {
		return p, nil
	}
	p, err := Compile(text, scope)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.compiled) >= maxCached {
		for old := range c.compiled {
			delete(c.compiled, old)
			break
		}
	}
	c.compiled[k] = p
	return p, nil
}
