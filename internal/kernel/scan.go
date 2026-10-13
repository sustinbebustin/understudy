package kernel

import (
	"math/big"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// outputScan is a smithy.Serializer that writes nothing and notes what the
// kernel needs from an output without a codec: the required timestamp
// members left at the zero time (checkStrict) and the streaming bodies
// (Release).
type outputScan struct {
	zeroTimes []string
	bodies    []smithy.BodySource
}

// scanOutput serializes out through an outputScan; an output that is not
// Serializable (a raw answer) has nothing to note.
func scanOutput(out any) outputScan {
	var scan outputScan
	if s, ok := out.(smithy.Serializable); ok {
		s.Serialize(&scan)
	}
	return scan
}

func (o *outputScan) WriteTimestamp(m *smithy.Member, v time.Time) {
	if m != nil && m.Required && v.IsZero() {
		o.zeroTimes = append(o.zeroTimes, m.Name)
	}
}

func (o *outputScan) WriteBody(_ *smithy.Member, b smithy.BodySource) {
	if b != nil {
		o.bodies = append(o.bodies, b)
	}
}

func (*outputScan) WriteStruct(*smithy.Member, *smithy.Schema)        {}
func (*outputScan) CloseStruct()                                      {}
func (*outputScan) WriteUnion(*smithy.Member, *smithy.Schema)         {}
func (*outputScan) CloseUnion()                                       {}
func (*outputScan) WriteList(*smithy.Member, int)                     {}
func (*outputScan) CloseList()                                        {}
func (*outputScan) WriteMap(*smithy.Member, int)                      {}
func (*outputScan) WriteMapKey(string)                                {}
func (*outputScan) CloseMap()                                         {}
func (*outputScan) WriteString(*smithy.Member, string)                {}
func (*outputScan) WriteBool(*smithy.Member, bool)                    {}
func (*outputScan) WriteInt8(*smithy.Member, int8)                    {}
func (*outputScan) WriteInt16(*smithy.Member, int16)                  {}
func (*outputScan) WriteInt32(*smithy.Member, int32)                  {}
func (*outputScan) WriteInt64(*smithy.Member, int64)                  {}
func (*outputScan) WriteFloat32(*smithy.Member, float32)              {}
func (*outputScan) WriteFloat64(*smithy.Member, float64)              {}
func (*outputScan) WriteBigInt(*smithy.Member, *big.Int)              {}
func (*outputScan) WriteBigDecimal(*smithy.Member, smithy.BigDecimal) {}
func (*outputScan) WriteBlob(*smithy.Member, []byte)                  {}
func (*outputScan) WriteDocument(*smithy.Member, document.Value)      {}
func (*outputScan) WriteNull(*smithy.Member)                          {}
