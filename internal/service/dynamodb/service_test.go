package dynamodb_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// errorBody is the part of a JSON error body the tests read.
type errorBody struct {
	Type    string `json:"__type"`
	Message string `json:"message"`
}

func parseError(h *harness, body string) errorBody {
	h.Helper()
	var e errorBody
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		h.Fatalf("error body %q: %v", body, err)
	}
	return e
}

// TestLicenceGateAnswers501WithTheNotice: until the licence is accepted,
// a DynamoDB call answers 501 with the notice, which names the exact flag
// and environment variable, and nothing reaches Local (ADR 0014, product
// M4). A service built without a Local behaves the same.
func TestLicenceGateAnswers501WithTheNotice(t *testing.T) {
	for name, local := range map[string]*fakeLocal{
		"awaiting":    {startErr: &ddblocal.LicenceError{Licence: ddblocal.AwaitingLicence{}}},
		"no Local":    nil,
		"other image": {startErr: &ddblocal.LicenceError{Licence: ddblocal.Accepted{Digest: "sha256:0000"}}},
	} {
		t.Run(name, func(t *testing.T) {
			runWith(t, local, func(h *harness) {
				got := h.call("ListTables", `{}`)
				e := parseError(h, got.Body)
				if got.Status != http.StatusNotImplemented || e.Type != "NotImplemented" || e.Message != ddblocal.Notice() {
					h.Fatalf("ListTables = %d %s, want 501 with the notice", got.Status, got.Body)
				}
				for _, want := range []string{"--accept-dynamodb-local-license", "UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1"} {
					if !strings.Contains(e.Message, want) {
						h.Errorf("the 501 does not name %s", want)
					}
				}
				if seen := h.local.requests(); len(seen) != 0 {
					h.Errorf("Local received %d requests", len(seen))
				}
			})
		})
	}
}

// TestLocalThatCannotStartAnswersWhy: a pull or start failure is
// DynamoDB's InternalServerError carrying the cause and the next step.
func TestLocalThatCannotStartAnswersWhy(t *testing.T) {
	pull := &ddblocal.PullError{Attempts: []error{errors.New("amazon/dynamodb-local: toomanyrequests")}}
	runWith(t, &fakeLocal{startErr: pull}, func(h *harness) {
		got := h.call("ListTables", `{}`)
		e := parseError(h, got.Body)
		if got.Status != http.StatusInternalServerError || e.Type != "com.amazonaws.dynamodb.v20120810#InternalServerError" {
			h.Fatalf("ListTables = %d %s, want 500 InternalServerError", got.Status, got.Body)
		}
		for _, want := range []string{"toomanyrequests", "understudy doctor"} {
			if !strings.Contains(e.Message, want) {
				h.Errorf("message %q lacks %q", e.Message, want)
			}
		}
	})
}

// TestOperationsLocalLacksNeverReachIt: an operation Local does not serve
// is not implemented in understudy's error format before Local sees it, so
// Local's generic UnknownOperationException is never involved
// (dynamodb-local.md 4.2).
func TestOperationsLocalLacksNeverReachIt(t *testing.T) {
	run(t, func(h *harness) {
		for _, op := range []string{
			"CreateBackup", "DescribeEndpoints", "DescribeKinesisStreamingDestination", "GetResourcePolicy", "RestoreTableToPointInTime",
		} {
			got := h.call(op, `{}`)
			if e := parseError(h, got.Body); got.Status != http.StatusNotImplemented || e.Type != "NotImplemented" || !strings.Contains(e.Message, "understudy does not implement dynamodb."+op) {
				h.Errorf("%s = %d %s, want 501 NotImplemented naming it", op, got.Status, got.Body)
			}
		}
		if seen := h.local.requests(); len(seen) != 0 {
			h.Errorf("Local received %d requests", len(seen))
		}
	})
}

// TestEveryOperationLocalServesIsBound: the forwarded operations are
// bound, with Full or a Partial naming Local's gap, and so are the
// operations the shims answer; those whose answers carry LatestStreamLabel
// list Local's labels as a deviation.
func TestEveryOperationLocalServesIsBound(t *testing.T) {
	run(t, func(h *harness) {
		forwarded := []string{
			"BatchExecuteStatement", "BatchGetItem", "BatchWriteItem", "CreateTable", "DeleteItem", "DeleteTable", "DescribeLimits",
			"DescribeTable", "ExecuteStatement", "ExecuteTransaction", "GetItem", "ListTables", "PutItem", "Query", "Scan",
			"TransactGetItems", "TransactWriteItems", "UpdateItem", "UpdateTable",
		}
		shimmed := []string{
			"UpdateTimeToLive", "DescribeTimeToLive", "TagResource", "UntagResource", "ListTagsOfResource",
			"DescribeContinuousBackups", "UpdateContinuousBackups",
		}
		labelled := []string{"CreateTable", "DescribeTable", "UpdateTable"}
		for _, op := range dynamodbapi.Service().Operations {
			status := h.Kernel.Bindings().Status(op)
			bound, ok := status.(kernel.Bound)
			if ok != (slices.Contains(forwarded, op.ID.Name) || slices.Contains(shimmed, op.ID.Name)) {
				h.Errorf("%s: status %#v", op.ID.Name, status)
				continue
			}
			p, partial := bound.Coverage.(registry.Partial)
			if ok && partial && slices.Contains(forwarded, op.ID.Name) && !strings.HasPrefix(p.Note, "aws-differs: dynamodb-local: ") {
				h.Errorf("%s: partial note %q does not say how Local differs", op.ID.Name, p.Note)
			}
			// ADR 0009 amendment: Local's stream labels change across a
			// snapshot load or reset, listed where answers carry them.
			if slices.Contains(labelled, op.ID.Name) && (!partial || !strings.Contains(p.Note, "LatestStreamLabel is minted by DynamoDB Local")) {
				h.Errorf("%s: coverage %#v does not list the stream-label deviation", op.ID.Name, bound.Coverage)
			}
		}
	})
}

// TestTablesDifferingOnlyInCaseAreRefused: AWS keeps Authors and authors
// apart; Local cannot, so the second is refused with ResourceInUseException
// before Local sees it, in that scope only, until the first is deleted
// (ADR 0014 amendment, F10).
func TestTablesDifferingOnlyInCaseAreRefused(t *testing.T) {
	run(t, func(h *harness) {
		create := func(name string, from *caller) response {
			return h.post(target("CreateTable"), `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"`+name+
				`","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`, from)
		}
		if got := create("Authors", defaultCaller()); got.Status != http.StatusOK {
			h.Fatalf("CreateTable Authors = %d %s", got.Status, got.Body)
		}
		got := create("authors", defaultCaller())
		e := parseError(h, got.Body)
		if got.Status != http.StatusBadRequest || e.Type != "com.amazonaws.dynamodb.v20120810#ResourceInUseException" ||
			!strings.HasPrefix(e.Message, "Table already exists: Authors.") {
			h.Fatalf("CreateTable authors = %d %s, want ResourceInUseException naming Authors", got.Status, got.Body)
		}
		if got := create("authors", &caller{Account: "111111111111", Region: "eu-west-1"}); got.Status != http.StatusOK {
			h.Errorf("CreateTable authors in another region = %d %s", got.Status, got.Body)
		}
		if got := h.call("DeleteTable", `{"TableName":"arn:aws:dynamodb:us-east-1:111111111111:table/Authors"}`); got.Status != http.StatusOK {
			h.Fatalf("DeleteTable = %d %s", got.Status, got.Body)
		}
		if got := create("authors", defaultCaller()); got.Status != http.StatusOK {
			h.Errorf("CreateTable authors after deleting Authors = %d %s", got.Status, got.Body)
		}
		// Local would delete authors when asked for AUTHORS; AWS has no
		// AUTHORS to delete.
		got = h.call("DeleteTable", `{"TableName":"AUTHORS"}`)
		if e := parseError(h, got.Body); got.Status != http.StatusBadRequest || e.Type != "com.amazonaws.dynamodb.v20120810#ResourceNotFoundException" ||
			e.Message != "Requested resource not found: Table: AUTHORS not found" {
			h.Fatalf("DeleteTable AUTHORS = %d %s, want ResourceNotFoundException", got.Status, got.Body)
		}
		if got := create("Authors", defaultCaller()); got.Status != http.StatusBadRequest {
			h.Errorf("CreateTable Authors beside authors = %d %s, want it refused", got.Status, got.Body)
		}
		count := map[string]int{}
		for _, f := range h.local.requests() {
			count[f.Op]++
		}
		if count["CreateTable"] != 3 || count["DeleteTable"] != 1 {
			h.Errorf("Local received %d CreateTable and %d DeleteTable requests, want 3 and 1", count["CreateTable"], count["DeleteTable"])
		}
	})
}

// TestCaseTwinOfATableIsNotFound: with Foo in the scope, every operation
// naming foo answers ResourceNotFoundException as AWS does, in AWS's
// words, before Local, which would act on Foo, sees it; Foo itself and
// names the registry does not know are forwarded.
func TestCaseTwinOfATableIsNotFound(t *testing.T) {
	run(t, func(h *harness) {
		if got := h.call("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"Foo","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}]}`); got.Status != http.StatusOK {
			h.Fatalf("CreateTable Foo = %d %s", got.Status, got.Body)
		}
		const (
			key     = `{"id":{"S":"a"}}`
			named   = "Requested resource not found: Table: foo not found"
			unnamed = "Requested resource not found"
		)
		calls := []struct{ op, body, message string }{
			{"DescribeTable", `{"TableName":"foo"}`, named},
			{"UpdateTable", `{"TableName":"foo","BillingMode":"PAY_PER_REQUEST"}`, named},
			{"DeleteTable", `{"TableName":"foo"}`, named},
			{"GetItem", `{"TableName":"foo","Key":` + key + `}`, unnamed},
			{"PutItem", `{"TableName":"foo","Item":` + key + `}`, unnamed},
			{"UpdateItem", `{"TableName":"foo","Key":` + key + `}`, unnamed},
			{"DeleteItem", `{"TableName":"foo","Key":` + key + `}`, unnamed},
			{"Query", `{"TableName":"foo"}`, unnamed},
			{"Scan", `{"TableName":"arn:aws:dynamodb:us-east-1:111111111111:table/foo"}`, unnamed},
			{"BatchGetItem", `{"RequestItems":{"Foo":{"Keys":[` + key + `]},"foo":{"Keys":[` + key + `]}}}`, unnamed},
			{"BatchWriteItem", `{"RequestItems":{"foo":[{"PutRequest":{"Item":` + key + `}}]}}`, unnamed},
			{"TransactGetItems", `{"TransactItems":[{"Get":{"TableName":"foo","Key":` + key + `}}]}`, unnamed},
			{"TransactWriteItems", `{"TransactItems":[{"Put":{"TableName":"Foo","Item":` + key + `}},{"ConditionCheck":{"TableName":"foo","Key":` + key + `,"ConditionExpression":"attribute_exists(id)"}}]}`, unnamed},
		}
		for _, c := range calls {
			got := h.call(c.op, c.body)
			if e := parseError(h, got.Body); got.Status != http.StatusBadRequest ||
				e.Type != "com.amazonaws.dynamodb.v20120810#ResourceNotFoundException" || e.Message != c.message {
				h.Errorf("%s %s = %d %s, want ResourceNotFoundException %q", c.op, c.body, got.Status, got.Body, c.message)
			}
		}
		if seen := h.local.requests(); len(seen) != 1 {
			h.Errorf("Local received %d requests, want only CreateTable Foo", len(seen))
		}
		for _, c := range []struct{ op, body string }{
			{"PutItem", `{"TableName":"Foo","Item":` + key + `}`},
			{"GetItem", `{"TableName":"Bar","Key":` + key + `}`},
		} {
			if got := h.call(c.op, c.body); got.Status != http.StatusOK {
				h.Errorf("%s %s = %d %s, want it forwarded", c.op, c.body, got.Status, got.Body)
			}
		}
	})
}

// TestTableARNOfAnotherScopeIsNotFound: a table ARN naming another
// partition, account or region than the caller's is not found, before
// Local, which ignores the ARN's scope, acts on the caller's own table of
// that name (dynamodb-local.md 4.2). An ARN of the caller's own scope is
// forwarded.
func TestTableARNOfAnotherScopeIsNotFound(t *testing.T) {
	run(t, func(h *harness) {
		const (
			key          = `{"id":{"S":"a"}}`
			otherRegion  = "arn:aws:dynamodb:eu-west-1:111111111111:table/Authors"
			otherAccount = "arn:aws:dynamodb:us-east-1:222222222222:table/Authors"
			otherBoth    = "arn:aws:dynamodb:eu-west-1:222222222222:table/Authors"
			otherPart    = "arn:aws-cn:dynamodb:us-east-1:111111111111:table/Authors"
			unnamed      = "Requested resource not found"
		)
		named := func(arn string) string { return "Requested resource not found: Table: " + arn + " not found" }
		calls := []struct{ op, body, message string }{
			{"DescribeTable", `{"TableName":"` + otherRegion + `"}`, named(otherRegion)},
			{"DescribeTable", `{"TableName":"` + otherAccount + `"}`, named(otherAccount)},
			{"DescribeTable", `{"TableName":"` + otherBoth + `"}`, named(otherBoth)},
			{"DescribeTable", `{"TableName":"` + otherPart + `"}`, named(otherPart)},
			{"CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"` + otherRegion +
				`","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`, named(otherRegion)},
			{"DeleteTable", `{"TableName":"` + otherAccount + `"}`, named(otherAccount)},
			{"GetItem", `{"TableName":"` + otherAccount + `","Key":` + key + `}`, unnamed},
			{"BatchGetItem", `{"RequestItems":{"Authors":{"Keys":[` + key + `]},"` + otherRegion + `":{"Keys":[` + key + `]}}}`, unnamed},
			{"TransactWriteItems", `{"TransactItems":[{"Put":{"TableName":"` + otherRegion + `","Item":` + key + `}}]}`, unnamed},
		}
		for _, c := range calls {
			got := h.call(c.op, c.body)
			if e := parseError(h, got.Body); got.Status != http.StatusBadRequest ||
				e.Type != "com.amazonaws.dynamodb.v20120810#ResourceNotFoundException" || e.Message != c.message {
				h.Errorf("%s %s = %d %s, want ResourceNotFoundException %q", c.op, c.body, got.Status, got.Body, c.message)
			}
		}
		if seen := h.local.requests(); len(seen) != 0 {
			h.Errorf("Local received %d requests, want none: %+v", len(seen), seen)
		}
		own := `{"TableName":"arn:aws:dynamodb:us-east-1:111111111111:table/Authors"}`
		if got := h.call("DescribeTable", own); got.Status != http.StatusOK {
			h.Errorf("DescribeTable %s = %d %s, want it forwarded", own, got.Status, got.Body)
		}
		// A malformed table ARN is Local's to refuse, in any region it
		// names, so its answer does not depend on that region.
		for _, malformed := range []string{"arn:aws:s3:eu-west-1:111111111111:table/Authors", "arn:aws:dynamodb:eu-west-1:111111111111:Authors"} {
			before := len(h.local.requests())
			h.call("DescribeTable", `{"TableName":"`+malformed+`"}`)
			if len(h.local.requests()) != before+1 {
				h.Errorf("DescribeTable %s was answered without Local, want it forwarded", malformed)
			}
		}
	})
}

// TestFaultNotesNameInjectableShapes: every fault rule a coverage note
// offers ("inject X or Y with a fault rule") is one the fault store
// accepts for that operation.
func TestFaultNotesNameInjectableShapes(t *testing.T) {
	offer := regexp.MustCompile(`inject ([A-Za-z.#]+(?: or [A-Za-z.#]+)*) with a fault rule`)
	run(t, func(h *harness) {
		checked := 0
		for _, op := range dynamodbapi.Service().Operations {
			bound, ok := h.Kernel.Bindings().Status(op).(kernel.Bound)
			if !ok {
				continue
			}
			p, ok := bound.Coverage.(registry.Partial)
			if !ok {
				continue
			}
			for _, m := range offer.FindAllStringSubmatch(p.Note, -1) {
				for shape := range strings.SplitSeq(m[1], " or ") {
					checked++
					_, err := h.Faults.Add(fault.Rule{
						Match:   fault.Match{Operations: []ref.OperationID{op.ID}},
						Phase:   fault.Request,
						Action:  fault.Error{Shape: smithy.ShapeID(shape)},
						Trigger: fault.Always{},
					})
					if err != nil {
						h.Errorf("%s offers a %s fault rule, which the fault store refuses: %v", op.ID.Name, shape, err)
					}
				}
			}
		}
		if checked == 0 {
			h.Error("no coverage note offers a fault rule")
		}
	})
}

// TestFaultsInjectWhatLocalNeverRaises: throttling, provisioned throughput
// and transaction conflicts come from fault rules matched by table ARN;
// the call never reaches Local (dynamodb-local.md Implications 10).
func TestFaultsInjectWhatLocalNeverRaises(t *testing.T) {
	for _, shape := range []smithy.ShapeID{
		dynamodbapi.IdentityProvisionedThroughputExceededException().Shape,
		dynamodbapi.IdentityThrottlingException().Shape,
		dynamodbapi.IdentityTransactionConflictException().Shape,
	} {
		t.Run(shape.Name(), func(t *testing.T) {
			run(t, func(h *harness) {
				_, rerr := h.Faults.Add(fault.Rule{
					Match:   fault.Match{Operations: []ref.OperationID{dynamodbapi.OpPutItemRaw.Descriptor().ID}, Resource: fault.NewARNGlob("arn:aws:dynamodb:*:*:table/Locks")},
					Phase:   fault.Request,
					Action:  fault.Error{Shape: shape},
					Trigger: fault.Always{},
				})
				if rerr != nil {
					h.Fatal(rerr)
				}
				got := h.call("PutItem", `{"TableName":"Locks","Item":{"LockID":{"S":"a"}}}`)
				if e := parseError(h, got.Body); got.Status != http.StatusBadRequest || e.Type != "com.amazonaws.dynamodb.v20120810#"+shape.Name() {
					h.Errorf("PutItem = %d %s, want %s", got.Status, got.Body, shape)
				}
				if got := h.call("PutItem", `{"TableName":"Other","Item":{"LockID":{"S":"a"}}}`); got.Status != http.StatusOK {
					h.Errorf("PutItem on another table = %d %s, want it forwarded", got.Status, got.Body)
				}
				if seen := h.local.requests(); len(seen) != 1 {
					h.Errorf("Local received %d requests, want only the other table's", len(seen))
				}
			})
		})
	}
}

// TestTableRegistryIsSnapshotState: the shim's tables list as resources, a
// reset drops them, and an import of the export brings them back.
func TestTableRegistryIsSnapshotState(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range []string{"Locks", "Jobs"} {
			body := `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"` + name + `","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}]}`
			if got := h.call("CreateTable", body); got.Status != http.StatusOK {
				h.Fatalf("CreateTable %s = %d %s", name, got.Status, got.Body)
			}
		}
		account, err := aws.ParseAccountID("111111111111")
		if err != nil {
			h.Fatal(err)
		}
		scope := aws.Scope{Partition: aws.PartitionAWS(), Account: account, Region: aws.DefaultRegion()}
		resources := func() []string {
			rs := h.svc.Resources(scope)
			arns := make([]string, 0, len(rs))
			for _, r := range rs {
				arns = append(arns, r.ARN.String())
			}
			return arns
		}
		want := []string{"arn:aws:dynamodb:us-east-1:111111111111:table/Jobs", "arn:aws:dynamodb:us-east-1:111111111111:table/Locks"}
		if got := resources(); !slices.Equal(got, want) {
			h.Errorf("Resources = %v, want %v", got, want)
		}
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if got := resources(); len(got) != 0 {
			h.Errorf("Resources after reset = %v", got)
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		if got := resources(); !slices.Equal(got, want) {
			h.Errorf("Resources after import = %v, want %v", got, want)
		}
	})
}
