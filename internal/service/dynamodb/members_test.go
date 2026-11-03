package dynamodb_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The documented TransactionConflict reason (smithy-protocol-tests.md
// 12.2, aws-doc).
const conflictReason = `{"Code":"TransactionConflict","Message":"Transaction is ongoing for the item."}`

// ruleMembers parses rule members without fault.Members' own decoder.
func ruleMembers(t interface {
	Helper()
	Fatalf(string, ...any)
}, js string,
) fault.Members {
	t.Helper()
	v, err := jsonx.NewDecoder([]byte(js)).ReadValue()
	m, ok := v.(document.Map)
	if err != nil || !ok {
		t.Fatalf("members %s: %v", js, err)
	}
	return fault.Members(m)
}

// transaction is a transactional operation and a request body of n items.
type transaction struct {
	op   *registry.Operation
	body func(n int) string
}

func transactions() []transaction {
	items := func(n int, item func(i int) string) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = item(i)
		}
		return strings.Join(parts, ",")
	}
	return []transaction{
		{dynamodbapi.OpTransactWriteItemsRaw.Descriptor(), func(n int) string {
			return `{"TransactItems":[` + items(n, func(i int) string {
				return `{"Put":{"TableName":"Orders","Item":{"pk":{"S":"` + strconv.Itoa(i) + `"}}}}`
			}) + `]}`
		}},
		{dynamodbapi.OpTransactGetItemsRaw.Descriptor(), func(n int) string {
			return `{"TransactItems":[` + items(n, func(i int) string {
				return `{"Get":{"TableName":"Orders","Key":{"pk":{"S":"` + strconv.Itoa(i) + `"}}}}`
			}) + `]}`
		}},
		{dynamodbapi.OpExecuteTransactionRaw.Descriptor(), func(n int) string {
			return `{"TransactStatements":[` + items(n, func(i int) string {
				return `{"Statement":"UPDATE \"Orders\" SET v = 2 WHERE pk = '` + strconv.Itoa(i) + `'"}`
			}) + `]}`
		}},
	}
}

// The DynamoDB binding counts the items of each transactional call, so a
// TransactionCanceledException rule giving two reasons lets a one-item
// call through to Local and cancels a three-item call before it: status
// 400, the reasons padded with None to the three items, and the message
// listing the codes (smithy-protocol-tests.md 12.1, 12.2, 12.4, 14.9).
// TransactWriteItems and TransactGetItems count TransactItems,
// ExecuteTransaction TransactStatements (12.2: one reason per requested
// item).
func TestTransactionRulesCountTheRequestItems(t *testing.T) {
	for _, tx := range transactions() {
		t.Run(tx.op.ID.Name, func(t *testing.T) {
			run(t, func(h *harness) {
				if _, err := h.Faults.Add(fault.Rule{
					ID:      "tx",
					Match:   fault.Match{Operations: []ref.OperationID{tx.op.ID}},
					Phase:   fault.Request,
					Action:  fault.Error{Shape: "TransactionCanceledException", Members: ruleMembers(h, `{"CancellationReasons":[{"Code":"None"},`+conflictReason+`]}`)},
					Trigger: fault.Always{},
				}); err != nil {
					h.Fatal(err)
				}
				if got := h.call(tx.op.ID.Name, tx.body(1)); got.Status != http.StatusOK {
					h.Errorf("a 1-item call = %d %s, want it forwarded to Local", got.Status, got.Body)
				}
				got := h.call(tx.op.ID.Name, tx.body(3))
				// AWS's top-level message key is Message
				// (smithy-protocol-tests.md 12.4, PROTO-01).
				want := `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","Message":` +
					`"Transaction cancelled, please refer cancellation reasons for specific reasons [None, TransactionConflict, None]",` +
					`"CancellationReasons":[{"Code":"None"},` + conflictReason + `,{"Code":"None"}]}`
				if got.Status != http.StatusBadRequest || got.Body != want {
					h.Errorf("a 3-item call = %d %s\nwant 400 %s", got.Status, got.Body, want)
				}
				if seen := h.local.requests(); len(seen) != 1 {
					h.Errorf("Local received %d requests, want only the 1-item call", len(seen))
				}
			})
		})
	}
}

// The transactional operations' coverage note tells users how to simulate
// a conflict now that a rule can set CancellationReasons
// (smithy-protocol-tests.md 14.10); the operations stay Partial.
func TestTransactionNotesOfferTheMembersRule(t *testing.T) {
	const clause = "transactions never conflict; AWS cancels a conflicting transaction with TransactionCanceledException and the cancellation reason " +
		"TransactionConflict; inject it with a fault rule whose members set CancellationReasons"
	run(t, func(h *harness) {
		for _, tx := range transactions() {
			bound, ok := h.Kernel.Bindings().Status(tx.op).(kernel.Bound)
			if !ok {
				h.Errorf("%s is not bound", tx.op.ID.Name)
				continue
			}
			p, ok := bound.Coverage.(registry.Partial)
			if !ok || !strings.Contains(p.Note, "aws-differs: dynamodb-local: "+clause) || strings.Contains(p.Note, "cannot set") {
				h.Errorf("%s coverage = %#v; want Partial with the note %q", tx.op.ID.Name, bound.Coverage, clause)
			}
		}
	})
}
