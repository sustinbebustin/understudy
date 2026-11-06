package kinesis

import (
	"crypto/md5" //nolint:gosec // Kinesis maps partition keys with MD5 (kinesis.md 3.3, aws-doc); not a security use
	"encoding/binary"
	"math/big"
	"sort"
)

// HashKey is a 128-bit hash key, 0 to 2^128-1 (kinesis.md 3.3).
type HashKey struct{ Hi, Lo uint64 }

// maxHashDigits is the length of 2^128-1 in decimal, the model's HashKey
// pattern bound ^(0|([1-9]\d{0,38}))$ (kinesis.md 3.3).
const maxHashDigits = 39

// ParseHashKey reads a decimal hash key without a leading zero, or "0";
// anything else, or a value above 2^128-1, is a *HashKeyError.
func ParseHashKey(s string) (HashKey, error) {
	if s == "" || len(s) > maxHashDigits || (s[0] == '0' && s != "0") {
		return HashKey{}, &HashKeyError{Text: s}
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return HashKey{}, &HashKeyError{Text: s}
		}
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.BitLen() > 128 {
		return HashKey{}, &HashKeyError{Text: s}
	}
	return keyOf(n), nil
}

// String returns h in decimal.
func (h HashKey) String() string { return h.big().String() }

// Compare orders h and o numerically and returns -1, 0 or +1.
func (h HashKey) Compare(o HashKey) int {
	switch {
	case h.Hi < o.Hi:
		return -1
	case h.Hi > o.Hi:
		return 1
	case h.Lo < o.Lo:
		return -1
	case h.Lo > o.Lo:
		return 1
	}
	return 0
}

func (h HashKey) big() *big.Int {
	n := new(big.Int).SetUint64(h.Hi)
	n.Lsh(n, 64)
	return n.Or(n, new(big.Int).SetUint64(h.Lo))
}

// keyOf is the key of n, which must lie in [0, 2^128-1].
func keyOf(n *big.Int) HashKey {
	var b [16]byte
	n.FillBytes(b[:])
	return HashKey{Hi: binary.BigEndian.Uint64(b[:8]), Lo: binary.BigEndian.Uint64(b[8:])}
}

// HashKeyError is text that is not a hash key.
type HashKeyError struct{ Text string }

func (e *HashKeyError) Error() string {
	return `"` + e.Text + `" is not a hash key: it must be a decimal integer from 0 to 2^128-1`
}

// HashRange is a shard's inclusive HashKeyRange.
type HashRange struct {
	Start HashKey `json:"start"`
	End   HashKey `json:"end"`
}

// Split returns n ranges that partition [0, 2^128-1] in order, range i
// starting where the formula s puts it and ending one before range i+1
// starts, the last at 2^128-1 (kinesis.md 3.3); n below 1 returns nil.
func Split(n int, s EvenSplit) []HashRange {
	if n < 1 {
		return nil
	}
	space := new(big.Int).Lsh(big.NewInt(1), 128)
	bn := big.NewInt(int64(n))
	step := new(big.Int).Quo(space, bn)
	start := func(i int) *big.Int {
		bi := big.NewInt(int64(i))
		if s == SplitFloorI {
			p := new(big.Int).Mul(space, bi)
			return p.Quo(p, bn)
		}
		return bi.Mul(step, bi)
	}
	one := big.NewInt(1)
	out := make([]HashRange, n)
	for i := range n {
		end := new(big.Int).Sub(space, one)
		if i+1 < n {
			end = start(i + 1)
			end.Sub(end, one)
		}
		out[i] = HashRange{Start: keyOf(start(i)), End: keyOf(end)}
	}
	return out
}

// PartitionHash is the hash key of a partition key under byte order o: the
// MD5 digest of the key's UTF-8 bytes read as a 128-bit integer
// (kinesis.md 3.3, 5.3).
func PartitionHash(key string, o HashByteOrder) HashKey {
	d := md5.Sum([]byte(key)) //nolint:gosec // see the import
	if o == HashLittleEndianUTF8 {
		return HashKey{Hi: binary.LittleEndian.Uint64(d[8:]), Lo: binary.LittleEndian.Uint64(d[:8])}
	}
	return HashKey{Hi: binary.BigEndian.Uint64(d[:8]), Lo: binary.BigEndian.Uint64(d[8:])}
}

// ShardFor returns the index of the range of ranges, which are in order,
// that holds h; -1 when none does.
func ShardFor(ranges []HashRange, h HashKey) int {
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].End.Compare(h) >= 0 })
	if i == len(ranges) || ranges[i].Start.Compare(h) > 0 {
		return -1
	}
	return i
}
