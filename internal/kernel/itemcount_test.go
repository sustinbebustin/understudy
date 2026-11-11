package kernel_test

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The catalogue hands the fault store a fresh value of an error shape of
// the operation's service, to decode a rule's members into: DynamoDB
// decodes its error shapes, SQS generates no error decoder, and an
// operation the registry lacks has none (smithy-protocol-tests.md 14.9).
func TestCatalogueNewMembers(t *testing.T) {
	reg, err := registry.New(dynamodbapi.Service(), sqsapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	cat := kernel.NewCatalogue(reg)
	tx := dynamodbapi.OpTransactWriteItemsRaw.Descriptor().ID
	tce := dynamodbapi.IdentityTransactionCanceledException()
	v, ok := cat.NewMembers(tx, tce.Shape)
	if _, isTCE := v.(*dynamodbapi.TransactionCanceledException); !ok || !isTCE {
		t.Errorf("NewMembers(TransactWriteItems, %s) = %T, %t; want a new *TransactionCanceledException", tce.Shape, v, ok)
	}
	if again, ok := cat.NewMembers(tx, tce.Shape); ok && again == v {
		t.Error("NewMembers returned the same value twice; want a fresh one per call")
	}
	if v, ok := cat.NewMembers(sqsapi.OpSendMessage.Descriptor().ID, sqsapi.IdentityQueueDoesNotExist().Shape); ok {
		t.Errorf("NewMembers(sqs.SendMessage, QueueDoesNotExist) = %T; want none", v)
	}
	if v, ok := cat.NewMembers(ref.OperationID{Service: "dynamodb", Name: "Bogus"}, tce.Shape); ok {
		t.Errorf("NewMembers of an unknown operation = %T; want none", v)
	}
}

// txMembers parses rule members from JSON without fault.Members' decoder.
func txMembers(t *testing.T, js string) fault.Members {
	t.Helper()
	v, err := jsonx.NewDecoder([]byte(js)).ReadValue()
	m, ok := v.(document.Map)
	if err != nil || !ok {
		t.Fatalf("members %s: %v", js, err)
	}
	return fault.Members(m)
}

// txInput is a TransactWriteItems input of n Puts.
func txInput(n int) *dynamodbapi.TransactWriteItemsInput {
	in := &dynamodbapi.TransactWriteItemsInput{}
	for i := range n {
		in.TransactItems = append(in.TransactItems, dynamodbapi.TransactWriteItem{Put: &dynamodbapi.Put{
			TableName: "Orders",
			Item:      map[string]dynamodbapi.AttributeValue{"pk": dynamodbapi.AttributeValueMemberS{Value: strconv.Itoa(i)}},
		}})
	}
	return in
}

func countItems(in *dynamodbapi.TransactWriteItemsInput) int { return len(in.TransactItems) }

// bindTransactions binds a stub TransactWriteItems with opts and adds a
// TransactionCanceledException rule giving reasons reasons, so only a call
// whose item count reaches reasons matches it (smithy-protocol-tests.md
// 14.9).
func bindTransactions(k *kerneltest.K, reasons int, opts ...kernel.BindOption) {
	k.Helper()
	stub := func(context.Context, *kernel.Call, *dynamodbapi.TransactWriteItemsInput, kernel.Wire) (kernel.RawResponse, *awserr.Error) {
		return kernel.RawResponse{Status: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: []byte("{}")}, nil
	}
	kernel.BindRaw(k.Kernel.Binder(dynamodbapi.Service()), dynamodbapi.OpTransactWriteItemsRaw, stub, registry.Full{}, opts...)
	parts := ""
	var partsSb88 strings.Builder
	for i := range reasons {
		if i > 0 {
			partsSb88.WriteString(",")
		}
		partsSb88.WriteString(`{"Code":"TransactionConflict"}`)
	}
	parts += partsSb88.String()
	if _, err := k.Faults.Add(fault.Rule{
		ID:      "tx",
		Match:   fault.Match{Operations: []ref.OperationID{dynamodbapi.OpTransactWriteItemsRaw.Descriptor().ID}},
		Phase:   fault.Request,
		Action:  fault.Error{Shape: "TransactionCanceledException", Members: txMembers(k.T, `{"CancellationReasons":[`+parts+`]}`)},
		Trigger: fault.Always{},
	}); err != nil {
		k.Fatal(err)
	}
}

// dispatchTx runs a TransactWriteItems call of n items and reports whether
// it was cancelled, failing on any other error.
func dispatchTx(k *kerneltest.K, n int) (cancelled bool, codes []string) {
	k.Helper()
	_, err := kerneltest.Dispatch[dynamodbapi.TransactWriteItemsInput, kernel.RawOutput](k.Context(), k, k.Call(dynamodbapi.OpTransactWriteItemsRaw.Descriptor()), txInput(n))
	if err == nil {
		return false, nil
	}
	v, ok := err.Members().(*dynamodbapi.TransactionCanceledException)
	if err.ID != dynamodbapi.IdentityTransactionCanceledException() || !ok {
		k.Fatalf("a %d-item call failed with %s %q (members %T); want TransactionCanceledException or the handler", n, err.ID.Shape, err.Message, err.Members())
	}
	for _, r := range v.CancellationReasons {
		if r.Code == nil {
			k.Fatalf("a reason without a code: %+v", v.CancellationReasons)
		}
		codes = append(codes, *r.Code)
	}
	return true, codes
}

// WithItemCount gives fault matching the transaction's item count, so a
// rule with two reasons cancels a call of two or more items, padding the
// reasons to the items, and lets a one-item call through to the handler
// (smithy-protocol-tests.md 14.9).
func TestWithItemCountReachesFaultMatching(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		bindTransactions(k, 2, kernel.WithItemCount(countItems))
		if cancelled, _ := dispatchTx(k, 1); cancelled {
			k.Error("a 1-item call was cancelled by a rule with 2 reasons; want the handler")
		}
		cancelled, codes := dispatchTx(k, 3)
		if want := []string{"TransactionConflict", "TransactionConflict", "None"}; !cancelled || !reflect.DeepEqual(codes, want) {
			k.Errorf("a 3-item call = cancelled %t with codes %v; want %v", cancelled, codes, want)
		}
	}, kerneltest.Services(dynamodbapi.Service()))
}

// Without the option the item count is 0, so a rule giving reasons never
// matches.
func TestWithoutItemCountTheCountIsZero(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		bindTransactions(k, 1)
		if cancelled, _ := dispatchTx(k, 3); cancelled {
			k.Error("a call bound without WithItemCount was cancelled; want the item count 0, below the rule's 1 reason")
		}
	}, kerneltest.Services(dynamodbapi.Service()))
}

// With several WithItemCount options the last applies.
func TestTheLastItemCountApplies(t *testing.T) {
	zero := func(*dynamodbapi.TransactWriteItemsInput) int { return 0 }
	for _, c := range []struct {
		name string
		opts []kernel.BindOption
		want bool
	}{
		{"zero then count", []kernel.BindOption{kernel.WithItemCount(zero), kernel.WithItemCount(countItems)}, true},
		{"count then zero", []kernel.BindOption{kernel.WithItemCount(countItems), kernel.WithItemCount(zero)}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			kerneltest.Run(t, func(k *kerneltest.K) {
				bindTransactions(k, 1, c.opts...)
				if cancelled, _ := dispatchTx(k, 2); cancelled != c.want {
					k.Errorf("a 2-item call cancelled = %t, want %t", cancelled, c.want)
				}
			}, kerneltest.Services(dynamodbapi.Service()))
		})
	}
}

// A WithItemCount of another operation's input type is a bind mistake, as
// for the other typed options.
func TestItemCountOfAnotherInputIsABindMistake(t *testing.T) {
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(dynamodbapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	k, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: ids.NewSeeded(1), Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	stub := func(context.Context, *kernel.Call, *dynamodbapi.TransactWriteItemsInput, kernel.Wire) (kernel.RawResponse, *awserr.Error) {
		return kernel.RawResponse{Status: http.StatusOK}, nil
	}
	kernel.BindRaw(k.Binder(dynamodbapi.Service()), dynamodbapi.OpTransactWriteItemsRaw, stub, registry.Full{},
		kernel.WithItemCount(func(*sqsapi.SendMessageInput) int { return 1 }))
	if !isA[*kernel.BindError](k.BindErr()) {
		t.Errorf("BindErr = %v, want a BindError for the SendMessage item count on TransactWriteItems", k.BindErr())
	}
}
