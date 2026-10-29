package kinesis

// HashKey is a 128-bit hash key, 0 to 2^128-1 (kinesis.md 3.3).
type HashKey struct{ Hi, Lo uint64 }

// ParseHashKey reads a decimal hash key without a leading zero, or "0";
// anything else, or a value above 2^128-1, is a *HashKeyError. Not
// implemented yet: it returns the zero key.
func ParseHashKey(string) (HashKey, error) { return HashKey{}, nil }

// String returns h in decimal. Not implemented yet: it returns "".
func (h HashKey) String() string { return "" }

// Compare orders h and o numerically and returns -1, 0 or +1. Not
// implemented yet: it returns 0.
func (h HashKey) Compare(HashKey) int { return 0 }

// HashKeyError is text that is not a hash key.
type HashKeyError struct{ Text string }

func (e *HashKeyError) Error() string {
	return `"` + e.Text + `" is not a hash key: it must be a decimal integer from 0 to 2^128-1`
}

// HashRange is a shard's inclusive HashKeyRange.
type HashRange struct{ Start, End HashKey }

// Split returns n ranges that partition [0, 2^128-1] in order, range i
// starting where the formula s puts it (kinesis.md 3.3); n below 1 returns
// nil. Not implemented yet: it returns nil.
func Split(int, EvenSplit) []HashRange { return nil }

// PartitionHash is the hash key of a partition key under byte order o
// (kinesis.md 5.3). Not implemented yet: it returns the zero key.
func PartitionHash(string, HashByteOrder) HashKey { return HashKey{} }

// ShardFor returns the index of the range of ranges that holds h, -1 when
// none does. Not implemented yet: it returns -1.
func ShardFor([]HashRange, HashKey) int { return -1 }
