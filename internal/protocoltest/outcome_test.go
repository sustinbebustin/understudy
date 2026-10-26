package protocoltest_test

import (
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/codegen/serdetest"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
)

// modeledError builds the error a handler returns for an error case: the
// case's value under its own identity, with the text of its message
// member as the message, so the codec writes the message once under the
// identity's key and the other members as an output's
// (smithy-protocol-tests.md 14.7).
func modeledError(o gen.Error) *awserr.Error {
	c := &messageCapture{}
	o.Value.Serialize(c)
	return awserr.WithMembers(o.Value, c.message, awserr.Modeled{})
}

// messageCapture reads the text of an error value's message member: the
// string member of the error structure named message or Message, the
// members the generator derives the message key from
// (internal/codegen/ir_errors.go). The embedded Recorder takes every
// other write.
type messageCapture struct {
	serdetest.Recorder
	// depth counts the open containers; 1 is the error structure.
	depth   int
	message string
}

func (c *messageCapture) WriteStruct(*smithy.Member, *smithy.Schema) { c.depth++ }
func (c *messageCapture) CloseStruct()                               { c.depth-- }
func (c *messageCapture) WriteUnion(*smithy.Member, *smithy.Schema)  { c.depth++ }
func (c *messageCapture) CloseUnion()                                { c.depth-- }
func (c *messageCapture) WriteList(*smithy.Member, int)              { c.depth++ }
func (c *messageCapture) CloseList()                                 { c.depth-- }
func (c *messageCapture) WriteMap(*smithy.Member, int)               { c.depth++ }
func (c *messageCapture) CloseMap()                                  { c.depth-- }

func (c *messageCapture) WriteString(m *smithy.Member, v string) {
	if c.depth == 1 && m != nil && (m.Name == "message" || m.Name == "Message") {
		c.message = v
	}
}
