package kernel

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// ZeroTimestampError is an output whose required timestamp members were
// left at the zero time, which clients would read as year 1 (the Terraform
// provider crashes on some; gap-G6.md finding 1).
type ZeroTimestampError struct {
	Op      string
	Members []string
}

func (e *ZeroTimestampError) Error() string {
	return fmt.Sprintf("the %s output leaves required timestamps at the zero time: %s; set them from the commit instant",
		e.Op, strings.Join(e.Members, ", "))
}

// checkStrict is the Strict hook of the encode step: it serializes out
// through a serializer that writes nothing and notes every required
// timestamp member holding the zero time.
func checkStrict(op *registry.Operation, out any) *awserr.Error {
	s, ok := out.(smithy.Serializable)
	if !ok {
		return nil
	}
	var z zeroTimestamps
	s.Serialize(&z)
	if len(z.members) == 0 {
		return nil
	}
	return awserr.Internalf(&ZeroTimestampError{Op: op.ID.String(), Members: z.members})
}

// zeroTimestamps is a smithy.Serializer that records the required timestamp
// members written with the zero time and ignores everything else.
type zeroTimestamps struct{ members []string }

func (z *zeroTimestamps) WriteTimestamp(m *smithy.Member, v time.Time) {
	if m != nil && m.Required && v.IsZero() {
		z.members = append(z.members, m.Name)
	}
}

func (*zeroTimestamps) WriteStruct(*smithy.Member, *smithy.Schema)        {}
func (*zeroTimestamps) CloseStruct()                                      {}
func (*zeroTimestamps) WriteUnion(*smithy.Member, *smithy.Schema)         {}
func (*zeroTimestamps) CloseUnion()                                       {}
func (*zeroTimestamps) WriteList(*smithy.Member, int)                     {}
func (*zeroTimestamps) CloseList()                                        {}
func (*zeroTimestamps) WriteMap(*smithy.Member, int)                      {}
func (*zeroTimestamps) WriteMapKey(string)                                {}
func (*zeroTimestamps) CloseMap()                                         {}
func (*zeroTimestamps) WriteString(*smithy.Member, string)                {}
func (*zeroTimestamps) WriteBool(*smithy.Member, bool)                    {}
func (*zeroTimestamps) WriteInt8(*smithy.Member, int8)                    {}
func (*zeroTimestamps) WriteInt16(*smithy.Member, int16)                  {}
func (*zeroTimestamps) WriteInt32(*smithy.Member, int32)                  {}
func (*zeroTimestamps) WriteInt64(*smithy.Member, int64)                  {}
func (*zeroTimestamps) WriteFloat32(*smithy.Member, float32)              {}
func (*zeroTimestamps) WriteFloat64(*smithy.Member, float64)              {}
func (*zeroTimestamps) WriteBigInt(*smithy.Member, *big.Int)              {}
func (*zeroTimestamps) WriteBigDecimal(*smithy.Member, smithy.BigDecimal) {}
func (*zeroTimestamps) WriteBlob(*smithy.Member, []byte)                  {}
func (*zeroTimestamps) WriteDocument(*smithy.Member, document.Value)      {}
func (*zeroTimestamps) WriteNull(*smithy.Member)                          {}
func (*zeroTimestamps) WriteBody(*smithy.Member, smithy.BodySource)       {}
