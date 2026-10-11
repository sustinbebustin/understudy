package events

import (
	"sync"

	"github.com/sustinbebustin/understudy/internal/ruler"
)

// maxCachedPatterns bounds the pattern cache. It is above the 300 rules a
// bus may have, so a working set of a few busy scopes stays compiled.
const maxCachedPatterns = 4096

// patternCache holds event patterns compiled for one Ruler profile, keyed
// by their text, so matching an event does not compile the patterns again
// (ARCHITECTURE.md 16: compiled once at configuration time). State keeps
// pattern text, which snapshots carry, and the cache fills from PutRule and,
// after Import, on first use. A compiled pattern is immutable and safe for
// concurrent use.
type patternCache struct {
	profile ruler.EventBridge
	mu      sync.Mutex
	// compiled holds at most maxCachedPatterns patterns; when full, an
	// arbitrary one makes room, to be compiled again on its next use.
	compiled map[string]*ruler.Pattern
}

func newPatternCache(p ruler.EventBridge) *patternCache {
	return &patternCache{profile: p, compiled: map[string]*ruler.Pattern{}}
}

// compile returns the compiled pattern of text, compiling and keeping it on
// a miss. A pattern that does not compile is not kept.
func (c *patternCache) compile(text string) (*ruler.Pattern, *ruler.CompileError) {
	c.mu.Lock()
	p, ok := c.compiled[text]
	c.mu.Unlock()
	if ok {
		return p, nil
	}
	p, err := ruler.Compile([]byte(text), c.profile)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.compiled) >= maxCachedPatterns {
		for k := range c.compiled {
			delete(c.compiled, k)
			break
		}
	}
	c.compiled[text] = p
	return p, nil
}
