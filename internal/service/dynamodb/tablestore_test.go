package dynamodb_test

import (
	"encoding/json"
	"maps"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

// item is one item in DynamoDB JSON: attribute name to its typed value,
// such as {"N":"12"}.
type item = map[string]json.RawMessage

// localRequest is the part of a forwarded request the fake reads.
type localRequest struct {
	TableName              string
	KeySchema              []struct{ AttributeName string }
	AttributeDefinitions   json.RawMessage
	BillingMode            string
	ProvisionedThroughput  *struct{ ReadCapacityUnits, WriteCapacityUnits int64 }
	GlobalSecondaryIndexes []struct {
		IndexName             string
		KeySchema, Projection json.RawMessage
		ProvisionedThroughput *struct{ ReadCapacityUnits, WriteCapacityUnits int64 }
	}
	Item                      item
	Key                       item
	ExclusiveStartKey         item
	ConditionExpression       *string
	FilterExpression          *string
	ProjectionExpression      *string
	UpdateExpression          *string
	ExpressionAttributeNames  map[string]string
	ExpressionAttributeValues item
	RequestItems              map[string][]struct{ PutRequest *struct{ Item item } }
}

// tableStore is a fake DynamoDB Local that keeps tables and items in one
// account and region: enough of Local's control and data plane for the
// shims' calls and a client's plain writes. It evaluates only the
// expressions the shims send (a TTL comparison) and single-assignment SET
// updates.
type tableStore struct {
	mu     sync.Mutex
	tables map[string]*fakeTable
	// pageSize bounds a Scan page, so the shims must follow
	// LastEvaluatedKey.
	pageSize int
	// beforeDelete runs before a conditional DeleteItem is evaluated, with
	// the store unlocked: a test refreshes an item there.
	beforeDelete func(key item)
}

type fakeTable struct {
	desc  map[string]any
	keys  []string
	items map[string]item
}

func newTableStore() *tableStore { return &tableStore{tables: map[string]*fakeTable{}, pageSize: 2} }

// serve answers one forwarded request as Local does.
func (s *tableStore) serve(op, body string) reply {
	var in localRequest
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		return localErr("com.amazon.coral.service#SerializationException", err.Error())
	}
	if op == "DeleteItem" && s.beforeDelete != nil && in.ConditionExpression != nil {
		s.beforeDelete(in.Key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tables[in.TableName]
	switch op {
	case "CreateTable":
		if t != nil {
			return localErr("com.amazonaws.dynamodb.v20120810#ResourceInUseException", "Table already exists: "+in.TableName)
		}
		s.tables[in.TableName] = newFakeTable(in)
		return ok(map[string]any{"TableDescription": s.tables[in.TableName].desc})
	case "ListTables":
		return ok(map[string]any{"TableNames": slices.Sorted(maps.Keys(s.tables))})
	case "BatchWriteItem":
		return s.batchWrite(in)
	}
	if t == nil {
		return localErr("com.amazonaws.dynamodb.v20120810#ResourceNotFoundException", "Cannot do operations on a non-existent table")
	}
	if op == "DeleteTable" {
		delete(s.tables, in.TableName)
		return ok(map[string]any{"TableDescription": t.desc})
	}
	return t.serve(op, body, in, s.pageSize)
}

func (t *fakeTable) serve(op, body string, in localRequest, pageSize int) reply {
	switch op {
	case "DescribeTable":
		return ok(map[string]any{"Table": t.desc})
	case "UpdateTable":
		return t.updateTable(body)
	case "PutItem":
		t.items[t.keyOf(in.Item)] = in.Item
		return ok(map[string]any{})
	case "GetItem":
		got, found := t.items[t.keyOf(in.Key)]
		if !found {
			return ok(map[string]any{})
		}
		return ok(map[string]any{"Item": got})
	case "UpdateItem":
		return t.updateItem(in)
	case "DeleteItem":
		k := t.keyOf(in.Key)
		if in.ConditionExpression != nil && !matches(t.items[k], *in.ConditionExpression, in) {
			return localErr("com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException", "The conditional request failed")
		}
		delete(t.items, k)
		return ok(map[string]any{})
	case "Scan":
		if a, ok := overlappingPath(in); ok {
			return localErr("com.amazon.coral.validate#ValidationException", "Invalid ProjectionExpression: Two document paths overlap with each other; "+
				"must remove or rewrite one of these paths; path one: ["+a+"], path two: ["+a+"]")
		}
		return t.scan(in, pageSize)
	}
	return localErr("com.amazonaws.dynamodb.v20120810#UnknownOperationException", "An unknown operation was requested.")
}

func newFakeTable(in localRequest) *fakeTable {
	t := &fakeTable{items: map[string]item{}}
	schema := make([]map[string]string, 0, len(in.KeySchema))
	for i, e := range in.KeySchema {
		t.keys = append(t.keys, e.AttributeName)
		kind := "HASH"
		if i > 0 {
			kind = "RANGE"
		}
		schema = append(schema, map[string]string{"AttributeName": e.AttributeName, "KeyType": kind})
	}
	t.desc = map[string]any{
		"AttributeDefinitions": in.AttributeDefinitions, "TableName": in.TableName, "KeySchema": schema,
		"TableStatus": "ACTIVE", "CreationDateTime": json.Number("1791434323.298"),
		"TableArn": "arn:aws:dynamodb:ddblocal:000000000000:table/" + in.TableName, "DeletionProtectionEnabled": false,
		"ItemCount": 0, "TableSizeBytes": 0,
	}
	if in.BillingMode == "PAY_PER_REQUEST" {
		t.desc["BillingModeSummary"] = map[string]any{"BillingMode": "PAY_PER_REQUEST", "LastUpdateToPayPerRequestDateTime": json.Number("1791434323.298")}
	}
	if in.ProvisionedThroughput != nil {
		t.desc["ProvisionedThroughput"] = unchangedThroughput(in.ProvisionedThroughput.ReadCapacityUnits, in.ProvisionedThroughput.WriteCapacityUnits)
	}
	if len(in.GlobalSecondaryIndexes) > 0 {
		indexes := make([]map[string]any, 0, len(in.GlobalSecondaryIndexes))
		for _, g := range in.GlobalSecondaryIndexes {
			index := map[string]any{
				"IndexName": g.IndexName, "KeySchema": g.KeySchema, "Projection": g.Projection, "IndexStatus": "ACTIVE",
				"IndexArn":  "arn:aws:dynamodb:ddblocal:000000000000:table/" + in.TableName + "/index/" + g.IndexName,
				"ItemCount": 0, "IndexSizeBytes": 0,
			}
			if g.ProvisionedThroughput != nil {
				index["ProvisionedThroughput"] = unchangedThroughput(g.ProvisionedThroughput.ReadCapacityUnits, g.ProvisionedThroughput.WriteCapacityUnits)
			}
			indexes = append(indexes, index)
		}
		t.desc["GlobalSecondaryIndexes"] = indexes
	}
	return t
}

// unchangedThroughput is how Local describes provisioned throughput that
// never changed.
func unchangedThroughput(reads, writes int64) map[string]any {
	return map[string]any{
		"LastIncreaseDateTime": json.Number("0.000"), "LastDecreaseDateTime": json.Number("0.000"), "NumberOfDecreasesToday": 0,
		"ReadCapacityUnits": reads, "WriteCapacityUnits": writes,
	}
}

// updateTable answers as Local does: "Nothing to update" when only the
// members Local ignores are set.
func (t *fakeTable) updateTable(body string) reply {
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &members); err != nil {
		return localErr("com.amazon.coral.service#SerializationException", err.Error())
	}
	var throughput struct{ ReadCapacityUnits, WriteCapacityUnits int64 }
	if raw, has := members["ProvisionedThroughput"]; has {
		if err := json.Unmarshal(raw, &throughput); err != nil {
			return localErr("com.amazon.coral.service#SerializationException", err.Error())
		}
		pt, provisioned := t.desc["ProvisionedThroughput"].(map[string]any)
		if !provisioned {
			return localErr("com.amazon.coral.validate#ValidationException", "the fake updates the throughput of a provisioned table only")
		}
		pt["ReadCapacityUnits"], pt["WriteCapacityUnits"] = throughput.ReadCapacityUnits, throughput.WriteCapacityUnits
		// Local stamps a change from the wall clock.
		pt["LastIncreaseDateTime"] = json.Number("1791434999.123")
	}
	for k := range members {
		if k != "TableName" && k != "SSESpecification" && k != "TableClass" && k != "WarmThroughput" {
			return ok(map[string]any{"TableDescription": t.desc})
		}
	}
	return localErr("com.amazon.coral.validate#ValidationException", "Nothing to update")
}

// keyOf is the canonical encoding of the key attributes of it.
func (t *fakeTable) keyOf(it item) string {
	key := make(item, len(t.keys))
	for _, k := range t.keys {
		key[k] = it[k]
	}
	b, err := json.Marshal(key)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// updateItem applies UpdateExpression "SET <name> = <value>".
func (t *fakeTable) updateItem(in localRequest) reply {
	expr := ""
	if in.UpdateExpression != nil {
		expr = *in.UpdateExpression
	}
	lhs, rhs, found := strings.Cut(strings.TrimPrefix(expr, "SET "), " = ")
	if !found {
		return localErr("com.amazon.coral.validate#ValidationException", "the fake understands SET <name> = <value> only")
	}
	k := t.keyOf(in.Key)
	it := maps.Clone(t.items[k])
	if it == nil {
		it = maps.Clone(in.Key)
	}
	it[resolveName(lhs, in)] = in.ExpressionAttributeValues[rhs]
	t.items[k] = it
	return ok(map[string]any{})
}

func (t *fakeTable) scan(in localRequest, pageSize int) reply {
	keys := slices.Sorted(maps.Keys(t.items))
	start := ""
	if in.ExclusiveStartKey != nil {
		start = t.keyOf(in.ExclusiveStartKey)
	}
	out := []item{}
	last := ""
	scanned := 0
	for _, k := range keys {
		if k <= start {
			continue
		}
		if scanned == pageSize {
			break
		}
		scanned++
		last = k
		it := t.items[k]
		if in.FilterExpression != nil && !matches(it, *in.FilterExpression, in) {
			continue
		}
		out = append(out, project(it, in))
	}
	answer := map[string]any{"Items": out, "Count": len(out), "ScannedCount": scanned}
	if scanned == pageSize && last != keys[len(keys)-1] {
		var lek item
		if err := json.Unmarshal([]byte(last), &lek); err != nil {
			panic(err)
		}
		answer["LastEvaluatedKey"] = lek
	}
	return ok(answer)
}

func (s *tableStore) batchWrite(in localRequest) reply {
	for name, requests := range in.RequestItems {
		t := s.tables[name]
		if t == nil {
			return localErr("com.amazonaws.dynamodb.v20120810#ResourceNotFoundException", "Cannot do operations on a non-existent table")
		}
		for _, r := range requests {
			if r.PutRequest != nil {
				t.items[t.keyOf(r.PutRequest.Item)] = r.PutRequest.Item
			}
		}
	}
	return ok(map[string]any{"UnprocessedItems": map[string]any{}})
}

// matches evaluates "<name> BETWEEN <low> AND <high>", "<name> <= <value>"
// or "<name> > <value>" over numbers; a missing or non-number attribute
// fails, as in DynamoDB.
func matches(it item, expr string, in localRequest) bool {
	if lhs, bounds, found := strings.Cut(expr, " BETWEEN "); found {
		low, high, _ := strings.Cut(bounds, " AND ")
		have, isNumber := number(it[resolveName(lhs, in)])
		from, fromNumber := number(in.ExpressionAttributeValues[low])
		to, toNumber := number(in.ExpressionAttributeValues[high])
		return isNumber && fromNumber && toNumber && have.Cmp(from) >= 0 && have.Cmp(to) <= 0
	}
	for _, op := range []string{" <= ", " > "} {
		lhs, rhs, found := strings.Cut(expr, op)
		if !found {
			continue
		}
		have, isNumber := number(it[resolveName(lhs, in)])
		want, wantNumber := number(in.ExpressionAttributeValues[rhs])
		if !isNumber || !wantNumber {
			return false
		}
		if op == " <= " {
			return have.Cmp(want) <= 0
		}
		return have.Cmp(want) > 0
	}
	panic("the fake cannot evaluate " + expr)
}

// number reads a Number attribute value.
func number(v json.RawMessage) (*big.Rat, bool) {
	var n struct{ N *string }
	if v == nil || json.Unmarshal(v, &n) != nil || n.N == nil {
		return nil, false
	}
	return new(big.Rat).SetString(*n.N)
}

// project keeps the attributes a ProjectionExpression names.
func project(it item, in localRequest) item {
	if in.ProjectionExpression == nil {
		return it
	}
	out := item{}
	for p := range strings.SplitSeq(*in.ProjectionExpression, ",") {
		name := resolveName(strings.TrimSpace(p), in)
		if v, has := it[name]; has {
			out[name] = v
		}
	}
	return out
}

// overlappingPath is an attribute a ProjectionExpression names twice,
// which Local refuses (probed on the pinned image).
func overlappingPath(in localRequest) (string, bool) {
	if in.ProjectionExpression == nil {
		return "", false
	}
	seen := map[string]bool{}
	for p := range strings.SplitSeq(*in.ProjectionExpression, ",") {
		name := resolveName(strings.TrimSpace(p), in)
		if seen[name] {
			return name, true
		}
		seen[name] = true
	}
	return "", false
}

func resolveName(name string, in localRequest) string {
	if resolved, has := in.ExpressionAttributeNames[name]; has {
		return resolved
	}
	return name
}

// items returns the items of table by key, nil for a table it lacks.
func (s *tableStore) items(table string) map[string]item {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tables[table]
	if t == nil {
		return nil
	}
	out := make(map[string]item, len(t.items))
	for k, v := range t.items {
		out[k] = maps.Clone(v)
	}
	return out
}

// tableNames lists the tables the store holds.
func (s *tableStore) tableNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(maps.Keys(s.tables))
}

func ok(v any) reply {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return reply{Status: http.StatusOK, Body: string(b)}
}

func localErr(typ, message string) reply {
	b, err := json.Marshal(map[string]string{"__type": typ, "Message": message})
	if err != nil {
		panic(err)
	}
	return reply{Status: http.StatusBadRequest, Body: string(b)}
}

// runStore runs fn against the service over a fake Local backed by a
// tableStore, with the other services given.
func runStore(t *testing.T, fn func(h *harness, store *tableStore), others ...neighbour) {
	t.Helper()
	store := newTableStore()
	runWith(t, &fakeLocal{answer: store.serve}, func(h *harness) { fn(h, store) }, others...)
}
