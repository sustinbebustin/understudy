package ses

import (
	"fmt"
	"slices"
	"testing"
)

// javaStringHash is String.hashCode: "" is 0, "hello" 99162322 and "Aa"
// and "BB" collide at 2112 (Java SE String.hashCode); a code point
// outside the BMP hashes as its two UTF-16 surrogates.
func TestJavaStringHash(t *testing.T) {
	for _, c := range []struct {
		s    string
		want uint32
	}{
		{"", 0},
		{"hello", 99162322},
		{"Aa", 2112},
		{"BB", 2112},
		{"\U0001F600", 31*0xD83D + 0xDE00},
	} {
		if got := javaStringHash(c.s); got != c.want {
			t.Errorf("javaStringHash(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

// hashSetOrder keeps one of each id; colliding ids keep insertion order;
// and past 12 entries the table has 32 buckets, so the order follows the
// low five bits of the spread hash.
func TestHashSetOrder(t *testing.T) {
	if got := hashSetOrder([]string{"BB", "Aa", "BB"}); !slices.Equal(got, []string{"BB", "Aa"}) {
		t.Errorf("hashSetOrder(BB, Aa, BB) = %q, want [BB Aa]", got)
	}
	ids := make([]string, 0, 13)
	for i := range 13 {
		ids = append(ids, fmt.Sprintf("r%d@example.com", i))
	}
	got := hashSetOrder(ids)
	for i := 1; i < len(got); i++ {
		if spread(javaStringHash(got[i-1]))&31 > spread(javaStringHash(got[i]))&31 {
			t.Fatalf("hashSetOrder of 13 ids = %q: not in 32-bucket order at %d", got, i)
		}
	}
}
