package kinesis

import "strconv"

// Options are the Kinesis switches; the zero value is every default.
type Options struct {
	// EvenSplit is kinesis.hash.even-split.
	EvenSplit EvenSplit
	// HashByteOrder is kinesis.hash.md5-byte-order.
	HashByteOrder HashByteOrder
	// SequenceLayout is kinesis.sequence.layout.
	SequenceLayout SequenceLayout
	// MillisBehind is kinesis.get-records.millis-behind-latest.
	MillisBehind MillisBehind
	// RetentionEqual is kinesis.retention.equal-value.
	RetentionEqual RetentionEqual
}

// EvenSplit is the formula that starts each shard's hash key range when a
// stream is split evenly (kinesis.md 3.3, KIN-01).
type EvenSplit uint8

// EvenSplit values; the zero value is SplitIFloor.
const (
	// SplitIFloor starts shard i at i * floor(2^128/N).
	SplitIFloor EvenSplit = iota
	// SplitFloorI starts shard i at floor(i * 2^128 / N).
	SplitFloorI
)

func (s EvenSplit) String() string {
	switch s {
	case SplitIFloor:
		return "i*floor(2^128/N)"
	case SplitFloorI:
		return "floor(i*2^128/N)"
	}
	return "EvenSplit(" + strconv.Itoa(int(s)) + ")"
}

// HashByteOrder is how a partition key's MD5 digest becomes a hash key
// (kinesis.md 3.3, 5.3, KIN-13).
type HashByteOrder uint8

// HashByteOrder values; the zero value is HashBigEndianUTF8.
const (
	// HashBigEndianUTF8 reads the digest of the key's UTF-8 bytes big-endian.
	HashBigEndianUTF8 HashByteOrder = iota
	// HashLittleEndianUTF8 reads the same digest little-endian.
	HashLittleEndianUTF8
)

func (o HashByteOrder) String() string {
	switch o {
	case HashBigEndianUTF8:
		return "big-endian-utf8"
	case HashLittleEndianUTF8:
		return "little-endian-utf8"
	}
	return "HashByteOrder(" + strconv.Itoa(int(o)) + ")"
}

// SequenceLayout is how a shard's sequence numbers are minted (kinesis.md
// 3.4, KIN-09, KIN-16).
type SequenceLayout uint8

// SequenceLayout values; the zero value is LayoutV2.
const (
	// LayoutV2 is V2Sequencer: 56 digits encoding the shard and a counter.
	LayoutV2 SequenceLayout = iota
	// LayoutDecimal56 is a plain 56-digit per-shard counter.
	LayoutDecimal56
)

func (l SequenceLayout) String() string {
	switch l {
	case LayoutV2:
		return "v2-56-digit"
	case LayoutDecimal56:
		return "decimal-56"
	}
	return "SequenceLayout(" + strconv.Itoa(int(l)) + ")"
}

// MillisBehind is the reference point of GetRecords' MillisBehindLatest
// (kinesis.md 5.10, KIN-10).
type MillisBehind uint8

// MillisBehind values; the zero value is BehindNextUnread.
const (
	// BehindNextUnread is now minus the first unread record's arrival.
	BehindNextUnread MillisBehind = iota
	// BehindLastReturned is now minus the last returned record's arrival.
	BehindLastReturned
	// BehindTipLastReturned is the newest record's arrival minus the last
	// returned record's.
	BehindTipLastReturned
)

func (m MillisBehind) String() string {
	switch m {
	case BehindNextUnread:
		return "now-minus-next-unread-arrival"
	case BehindLastReturned:
		return "now-minus-last-returned-arrival"
	case BehindTipLastReturned:
		return "tip-minus-last-returned-arrival"
	}
	return "MillisBehind(" + strconv.Itoa(int(m)) + ")"
}

// RetentionEqual is how Increase and DecreaseStreamRetentionPeriod answer
// the stream's current value (kinesis.md 4.7, KIN-06).
type RetentionEqual uint8

// RetentionEqual values; the zero value is RetentionEqualAccept.
const (
	// RetentionEqualAccept accepts an equal value and changes nothing.
	RetentionEqualAccept RetentionEqual = iota
	// RetentionEqualReject refuses an equal value.
	RetentionEqualReject
)

func (r RetentionEqual) String() string {
	switch r {
	case RetentionEqualAccept:
		return "accept"
	case RetentionEqualReject:
		return "reject"
	}
	return "RetentionEqual(" + strconv.Itoa(int(r)) + ")"
}
