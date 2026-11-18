package ids

import (
	"bytes"
	"testing"
)

// TestSSECSalt: an SSE-C salt is 16 bytes from its own stream, so seeded
// runs reproduce salts (ARCHITECTURE.md 12; s3-ssec-object-lock.md 3.11).
func TestSSECSalt(t *testing.T) {
	a, b := NewSeeded(7), NewSeeded(7)
	first := a.SSECSalt()
	if len(first) != 16 || !bytes.Equal(first, b.SSECSalt()) {
		t.Errorf("seeded salts = %x and another; want 16 equal bytes", first)
	}
	if bytes.Equal(first, a.SSECSalt()) {
		t.Error("two salts drawn in a row are equal")
	}
}
