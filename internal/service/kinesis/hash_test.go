package kinesis_test

import (
	"crypto/md5" //nolint:gosec // Kinesis maps partition keys with MD5 (kinesis.md 3.3, aws-doc); not a security use
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/kinesis"
)

// maxHash is 2^128-1, the top of the hash key space (kinesis.md 3.3, model
// HashKey pattern and the documented ranges).
const maxHash = "340282366920938463463374607431768211455"

func bigOf(h kinesis.HashKey) *big.Int {
	n := new(big.Int).SetUint64(h.Hi)
	n.Lsh(n, 64)
	return n.Or(n, new(big.Int).SetUint64(h.Lo))
}

func two128() *big.Int { return new(big.Int).Lsh(big.NewInt(1), 128) }

// hashGen draws any key of [0, 2^128-1].
func hashGen() *rapid.Generator[kinesis.HashKey] {
	return rapid.Custom(func(t *rapid.T) kinesis.HashKey {
		return kinesis.HashKey{Hi: rapid.Uint64().Draw(t, "hi"), Lo: rapid.Uint64().Draw(t, "lo")}
	})
}

// ParseHashKey reads the model's decimal HashKey, 0 to 2^128-1 with no
// leading zero (kinesis.md 3.3: pattern ^(0|([1-9]\d{0,38}))$ and the key
// space), String writes it back, and Compare orders numerically.
func TestHashKeyRoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		h := hashGen().Draw(t, "h")
		want := bigOf(h).String()
		if got := h.String(); got != want {
			t.Fatalf("String(%#v) = %q, want %q", h, got, want)
		}
		back, err := kinesis.ParseHashKey(want)
		if err != nil || back != h {
			t.Fatalf("ParseHashKey(%q) = %#v, %v; want %#v", want, back, err, h)
		}
		o := hashGen().Draw(t, "o")
		if got, want := h.Compare(o), bigOf(h).Cmp(bigOf(o)); got != want {
			t.Fatalf("Compare(%s, %s) = %d, want %d", h, o, got, want)
		}
	})
}

// Text that is not a decimal key of the space is a *HashKeyError: above
// 2^128-1 (the model's 39 digits allow it, kinesis.md 5.3), a leading zero,
// a sign, empty.
func TestParseHashKeyRefuses(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"340282366920938463463374607431768211456", "01", "-1", "+1", "", "1.0", "abc", "1 "} {
		_, err := kinesis.ParseHashKey(s)
		var he *kinesis.HashKeyError
		if !errors.As(err, &he) || he.Text != s {
			t.Errorf("ParseHashKey(%q) error = %v, want *HashKeyError{%q}", s, err, s)
		}
	}
	for _, s := range []string{"0", "1", maxHash} {
		if h, err := kinesis.ParseHashKey(s); err != nil || h.String() != s {
			t.Errorf("ParseHashKey(%q) = %s, %v", s, h, err)
		}
	}
}

// startOf is start(i) of the kinesis.hash.even-split value s, exactly as
// the switch value names its formula.
func startOf(i, n int, s kinesis.EvenSplit) *big.Int {
	switch s {
	case kinesis.SplitIFloor: // "i*floor(2^128/N)"
		q := new(big.Int).Quo(two128(), big.NewInt(int64(n)))
		return q.Mul(q, big.NewInt(int64(i)))
	case kinesis.SplitFloorI: // "floor(i*2^128/N)"
		p := new(big.Int).Mul(two128(), big.NewInt(int64(i)))
		return p.Quo(p, big.NewInt(int64(n)))
	}
	panic("unknown EvenSplit")
}

// The shards' hash ranges partition [0, 2^128-1] in index order, each
// starting where the switch's formula puts it, and every key lands in the
// one range that holds it (kinesis.md 3.3).
func TestSplitPartitionsTheKeySpace(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 200).Draw(t, "n")
		s := rapid.SampledFrom([]kinesis.EvenSplit{kinesis.SplitIFloor, kinesis.SplitFloorI}).Draw(t, "split")
		rs := kinesis.Split(n, s)
		if len(rs) != n {
			t.Fatalf("Split(%d, %s) has %d ranges", n, s, len(rs))
		}
		if rs[0].Start.String() != "0" || rs[n-1].End.String() != maxHash {
			t.Fatalf("Split(%d, %s) spans %s..%s, want 0..2^128-1", n, s, rs[0].Start, rs[n-1].End)
		}
		for i, r := range rs {
			if got := bigOf(r.Start); got.Cmp(startOf(i, n, s)) != 0 {
				t.Fatalf("range %d starts %s, want %s", i, got, startOf(i, n, s))
			}
			if r.Start.Compare(r.End) > 0 {
				t.Fatalf("range %d is empty: %s..%s", i, r.Start, r.End)
			}
			if i > 0 {
				next := new(big.Int).Add(bigOf(rs[i-1].End), big.NewInt(1))
				if bigOf(r.Start).Cmp(next) != 0 {
					t.Fatalf("range %d starts %s, not one after range %d's end %s", i, r.Start, i-1, rs[i-1].End)
				}
			}
		}
		h := hashGen().Draw(t, "h")
		i := kinesis.ShardFor(rs, h)
		if i < 0 || rs[i].Start.Compare(h) > 0 || rs[i].End.Compare(h) < 0 {
			t.Fatalf("ShardFor(%s) = %d, not a range holding it", h, i)
		}
	})
	if kinesis.Split(0, kinesis.SplitIFloor) != nil {
		t.Error("Split(0) is not nil")
	}
}

// The documented and computed starts (kinesis.md 3.3 table, aws-doc
// DescribeStream example; four shards computed in 3.3; N=10 and N=7 from
// 14.1, where the formulas differ).
func TestSplitDocumentedStarts(t *testing.T) {
	t.Parallel()
	starts := func(n int, s kinesis.EvenSplit) []string {
		out := make([]string, 0, n)
		for _, r := range kinesis.Split(n, s) {
			out = append(out, r.Start.String())
		}
		return out
	}
	for _, s := range []kinesis.EvenSplit{kinesis.SplitIFloor, kinesis.SplitFloorI} {
		three := kinesis.Split(3, s)
		got := make([][2]string, 0, len(three))
		for _, r := range three {
			got = append(got, [2]string{r.Start.String(), r.End.String()})
		}
		want := [][2]string{
			{"0", "113427455640312821154458202477256070484"},
			{"113427455640312821154458202477256070485", "226854911280625642308916404954512140969"},
			{"226854911280625642308916404954512140970", maxHash},
		}
		if !slices.Equal(got, want) {
			t.Errorf("Split(3, %s) = %v, want the documented %v", s, got, want)
		}
		if got, want := starts(4, s), []string{"0", "85070591730234615865843651857942052864", "170141183460469231731687303715884105728", "255211775190703847597530955573826158592"}; !slices.Equal(got, want) {
			t.Errorf("Split(4, %s) starts %v, want %v", s, got, want)
		}
		if got := starts(7, s); len(got) != 7 || got[1] != "48611766702991209066196372490252601636" {
			t.Errorf("Split(7, %s) shard 1 starts %v, want 48611766702991209066196372490252601636 (kinesis.md 14.1, either formula)", s, got)
		}
	}
	for _, tc := range []struct {
		n     int
		s     kinesis.EvenSplit
		shard int
		want  string
	}{
		{10, kinesis.SplitIFloor, 2, "68056473384187692692674921486353642290"},
		{10, kinesis.SplitFloorI, 2, "68056473384187692692674921486353642291"},
		{7, kinesis.SplitIFloor, 2, "97223533405982418132392744980505203272"},
		{7, kinesis.SplitFloorI, 2, "97223533405982418132392744980505203273"},
	} {
		if got := starts(tc.n, tc.s); len(got) != tc.n || got[tc.shard] != tc.want {
			t.Errorf("Split(%d, %s) shard %d starts %v, want %s (kinesis.md 14.1)", tc.n, tc.s, tc.shard, got, tc.want)
		}
	}
}

// PartitionHash is the MD5 digest of the key's UTF-8 bytes read as a
// 128-bit integer in the switch's byte order (kinesis.md 3.3, 5.3; the
// switch kinesis.hash.md5-byte-order names both readings).
func TestPartitionHashReadsTheDigest(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringN(1, 256, -1).Draw(t, "key")
		d := md5.Sum([]byte(key)) //nolint:gosec // see the import
		be := new(big.Int).SetBytes(d[:])
		rev := slices.Clone(d[:])
		slices.Reverse(rev)
		le := new(big.Int).SetBytes(rev)
		if got := bigOf(kinesis.PartitionHash(key, kinesis.HashBigEndianUTF8)); got.Cmp(be) != 0 {
			t.Fatalf("PartitionHash(%q, big-endian) = %s, want %s", key, got, be)
		}
		if got := bigOf(kinesis.PartitionHash(key, kinesis.HashLittleEndianUTF8)); got.Cmp(le) != 0 {
			t.Fatalf("PartitionHash(%q, little-endian) = %s, want %s", key, got, le)
		}
	})
}

// The computed placements on a four-shard even split (kinesis.md 5.3 and
// 14.13's expected ShardIds for both readings).
func TestPartitionHashDocumentedPlacement(t *testing.T) {
	t.Parallel()
	four := kinesis.Split(4, kinesis.SplitIFloor)
	for _, tc := range []struct {
		key   string
		order kinesis.HashByteOrder
		shard int
	}{
		{"a", kinesis.HashBigEndianUTF8, 0},
		{"b", kinesis.HashBigEndianUTF8, 2},
		{"c", kinesis.HashBigEndianUTF8, 1},
		{"d", kinesis.HashBigEndianUTF8, 2},
		{"1", kinesis.HashBigEndianUTF8, 3},
		{"ééé", kinesis.HashBigEndianUTF8, 3},
		{"a", kinesis.HashLittleEndianUTF8, 1},
		{"c", kinesis.HashLittleEndianUTF8, 0},
		{"1", kinesis.HashLittleEndianUTF8, 2},
	} {
		if got := kinesis.ShardFor(four, kinesis.PartitionHash(tc.key, tc.order)); got != tc.shard {
			t.Errorf("key %q under %s lands in shard %d, want %d", tc.key, tc.order, got, tc.shard)
		}
	}
	// 85070591730234615865843651857942052863 is the last key of shard 0
	// (14.13 step ehk-end-0).
	if h, err := kinesis.ParseHashKey("85070591730234615865843651857942052863"); err != nil || kinesis.ShardFor(four, h) != 0 {
		t.Errorf("the end of shard 0 lands in %d (%v)", kinesis.ShardFor(four, h), err)
	}
	if got := kinesis.ShardFor(nil, kinesis.HashKey{}); got != -1 {
		t.Errorf("ShardFor(nil) = %d, want -1", got)
	}
}

// Each switch type prints its value's name, else "<Type>(<n>)".
func TestOptionStrings(t *testing.T) {
	t.Parallel()
	for got, want := range map[string]string{
		kinesis.SplitIFloor.String():           "i*floor(2^128/N)",
		kinesis.SplitFloorI.String():           "floor(i*2^128/N)",
		kinesis.EvenSplit(9).String():          "EvenSplit(9)",
		kinesis.HashBigEndianUTF8.String():     "big-endian-utf8",
		kinesis.HashLittleEndianUTF8.String():  "little-endian-utf8",
		kinesis.HashByteOrder(9).String():      "HashByteOrder(9)",
		kinesis.LayoutV2.String():              "v2-56-digit",
		kinesis.LayoutDecimal56.String():       "decimal-56",
		kinesis.SequenceLayout(9).String():     "SequenceLayout(9)",
		kinesis.BehindNextUnread.String():      "now-minus-next-unread-arrival",
		kinesis.BehindLastReturned.String():    "now-minus-last-returned-arrival",
		kinesis.BehindTipLastReturned.String(): "tip-minus-last-returned-arrival",
		kinesis.MillisBehind(9).String():       "MillisBehind(9)",
		kinesis.RetentionEqualAccept.String():  "accept",
		kinesis.RetentionEqualReject.String():  "reject",
		kinesis.RetentionEqual(9).String():     "RetentionEqual(9)",
	} {
		if got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
	if !strings.Contains((&kinesis.HashKeyError{Text: "x"}).Error(), `"x" is not a hash key`) {
		t.Error("HashKeyError text")
	}
}
