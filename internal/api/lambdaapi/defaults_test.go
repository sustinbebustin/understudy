package lambdaapi_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/codegen/serdetest"
)

// deref is f's value, through the pointer when f is one: a member whose
// default is not its Go zero value is a pointer (smithy-protocol-tests.md
// 14.2), and the tests hold for either representation.
func deref(f reflect.Value) (any, bool) {
	if f.Kind() == reflect.Pointer {
		if f.IsNil() {
			return nil, false
		}
		f = f.Elem()
	}
	return f.Interface(), true
}

// The pinned models' one non-zero output default is
// com.amazonaws.lambda#Event$EventId, @default(1) (smithy-protocol-tests.md
// 10.5): an event the handler leaves it unset in is written with 1, not 0
// (10.2, 14.3).
func TestUnsetEventIDSerializesAsItsDefault(t *testing.T) {
	out := &lambdaapi.GetDurableExecutionHistoryOutput{Events: []lambdaapi.Event{{}}}
	var rec serdetest.Recorder
	out.Serialize(&rec)
	i := slices.IndexFunc(rec.Events, func(e serdetest.Event) bool { return e.Member != nil && e.Member.Name == "EventId" })
	if i < 0 || rec.Events[i].Op != serdetest.OpWriteInt32 || rec.Events[i].Value != int32(1) {
		t.Errorf("Serialize of an event with EventId unset recorded\n%q\nwant WriteInt32 EventId 1", rec.Strings())
	}
}

// GetDurableExecution's IncludeExecutionData has @default(true)
// (third_party/api-models-aws/models/lambda/service/2015-03-31/lambda-2015-03-31.json,
// com.amazonaws.lambda#GetDurableExecutionRequest): a request that leaves
// it out decodes as true, never as false (smithy-protocol-tests.md 10.2,
// 14.4).
func TestOmittedIncludeExecutionDataDecodesAsTrue(t *testing.T) {
	schema := lambdaapi.OpGetDurableExecution.Descriptor().Input
	var in lambdaapi.GetDurableExecutionInput
	p := serdetest.NewReplayer([]serdetest.Event{{Op: serdetest.OpWriteStruct, Schema: schema}, {Op: serdetest.OpCloseStruct}})
	if err := in.Deserialize(p); err != nil {
		t.Fatal(err)
	}
	if err := p.Done(); err != nil {
		t.Fatal(err)
	}
	if v, ok := deref(reflect.ValueOf(in).FieldByName("IncludeExecutionData")); !ok || v != true {
		t.Errorf("IncludeExecutionData = %v (set %t), want true", v, ok)
	}
}
