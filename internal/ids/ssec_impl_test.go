package ids

import (
	"bytes"
	"testing"
)

// TestSSECSalt: an SSE-C salt is 16 bytes from its own stream, named
// s3-ssec-salt in snapshot manifests, so seeded runs reproduce salts
// (ARCHITECTURE.md 12; s3-ssec-object-lock.md 3.11).
func TestSSECSalt(t *testing.T) {
	if got := KindSSECSalt.String(); got != "s3-ssec-salt" {
		t.Errorf("KindSSECSalt = %q, want s3-ssec-salt", got)
	}
	var k Kind
	if err := k.UnmarshalText([]byte("s3-ssec-salt")); err != nil || k != KindSSECSalt {
		t.Errorf("UnmarshalText(s3-ssec-salt) = %v, %v; want KindSSECSalt", k, err)
	}
	a, b := NewSeeded(7), NewSeeded(7)
	first := a.SSECSalt()
	if len(first) != 16 || !bytes.Equal(first, b.SSECSalt()) {
		t.Errorf("seeded salts = %x and another; want 16 equal bytes", first)
	}
	if bytes.Equal(first, a.SSECSalt()) {
		t.Error("two salts drawn in a row are equal")
	}
}
