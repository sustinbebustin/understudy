package protocoltest_test

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
)

// UnrepresentableError is an error case whose value awserr.Error cannot
// carry: a handler names an error by its identity, message, flat string
// fields and headers, so a member holding a structure, collection, blob,
// timestamp or document has no slot.
type UnrepresentableError struct {
	Member string
	Kind   string
}

func (e *UnrepresentableError) Error() string {
	return fmt.Sprintf("the error member %s holds a %s, which awserr.Error has no slot for (it carries a message, string fields and headers)", e.Member, e.Kind)
}

// modeledError builds the error a handler returns for an error case: the
// identity, and the value's top-level members as the message, header-bound
// members as headers and the rest as fields.
func modeledError(o gen.Error) (*awserr.Error, error) {
	c := &errorCapture{err: &awserr.Error{ID: o.ID, Origin: awserr.Modeled{}}}
	o.Value.Serialize(c)
	if c.problem != nil {
		return nil, c.problem
	}
	return c.err, nil
}

// errorCapture is a smithy.Serializer that reads an error value's
// top-level members into an awserr.Error.
type errorCapture struct {
	err     *awserr.Error
	depth   int
	problem error
}

func (c *errorCapture) WriteStruct(m *smithy.Member, _ *smithy.Schema) {
	if m == nil {
		c.depth++
		return
	}
	c.open(m, "structure")
}
func (c *errorCapture) CloseStruct()                                  { c.depth-- }
func (c *errorCapture) WriteUnion(m *smithy.Member, _ *smithy.Schema) { c.open(m, "union") }
func (c *errorCapture) CloseUnion()                                   { c.depth-- }
func (c *errorCapture) WriteList(m *smithy.Member, _ int)             { c.open(m, "list") }
func (c *errorCapture) CloseList()                                    { c.depth-- }
func (c *errorCapture) WriteMap(m *smithy.Member, _ int)              { c.open(m, "map") }
func (*errorCapture) WriteMapKey(string)                              {}
func (c *errorCapture) CloseMap()                                     { c.depth-- }

func (c *errorCapture) WriteString(m *smithy.Member, v string) { c.scalar(m, v) }
func (c *errorCapture) WriteBool(m *smithy.Member, v bool)     { c.scalar(m, strconv.FormatBool(v)) }
func (c *errorCapture) WriteInt8(m *smithy.Member, v int8)     { c.scalar(m, strconv.Itoa(int(v))) }
func (c *errorCapture) WriteInt16(m *smithy.Member, v int16)   { c.scalar(m, strconv.Itoa(int(v))) }
func (c *errorCapture) WriteInt32(m *smithy.Member, v int32)   { c.scalar(m, strconv.Itoa(int(v))) }
func (c *errorCapture) WriteInt64(m *smithy.Member, v int64)   { c.scalar(m, strconv.FormatInt(v, 10)) }

func (c *errorCapture) WriteFloat32(m *smithy.Member, v float32) {
	c.scalar(m, strconv.FormatFloat(float64(v), 'g', -1, 32))
}

func (c *errorCapture) WriteFloat64(m *smithy.Member, v float64) {
	c.scalar(m, strconv.FormatFloat(v, 'g', -1, 64))
}
func (c *errorCapture) WriteBigInt(m *smithy.Member, v *big.Int) { c.scalar(m, v.String()) }
func (c *errorCapture) WriteBigDecimal(m *smithy.Member, v smithy.BigDecimal) {
	c.scalar(m, string(v))
}

func (c *errorCapture) WriteTimestamp(m *smithy.Member, _ time.Time) { c.leaf(m, "timestamp") }
func (c *errorCapture) WriteBlob(m *smithy.Member, _ []byte)         { c.leaf(m, "blob") }
func (c *errorCapture) WriteDocument(m *smithy.Member, _ document.Value) {
	c.leaf(m, "document")
}
func (c *errorCapture) WriteNull(m *smithy.Member)                      { c.leaf(m, "null") }
func (c *errorCapture) WriteBody(m *smithy.Member, _ smithy.BodySource) { c.leaf(m, "stream") }

// open enters a container; one directly in the error value has no slot.
func (c *errorCapture) open(m *smithy.Member, kind string) {
	c.leaf(m, kind)
	c.depth++
}

// leaf records a value with no slot when it is a top-level member.
func (c *errorCapture) leaf(m *smithy.Member, kind string) {
	if c.depth == 1 && c.problem == nil {
		c.problem = &UnrepresentableError{Member: m.Name, Kind: kind}
	}
}

func (c *errorCapture) scalar(m *smithy.Member, v string) {
	if c.depth != 1 {
		return
	}
	if h, ok := m.HTTP.(smithy.HTTPHeader); ok {
		c.err.Headers = append(c.err.Headers, awserr.Header{Name: h.Name, Value: v})
		return
	}
	if strings.EqualFold(m.Name, "message") {
		c.err.Message = v
		return
	}
	c.err.Fields = append(c.err.Fields, awserr.Field{Name: m.Name, Value: v})
}
