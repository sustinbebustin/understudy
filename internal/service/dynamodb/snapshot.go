package dynamodb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Items live in Local, not in the cells, so an explicit snapshot save
// exports them after the cut and a load imports them first (kernel.Late,
// ARCHITECTURE.md 13). Each table Local holds in a scope the shim records
// is one section,
// <account>/<region>/<table>.ndjson: its first line is the CreateTable
// request that rebuilds the table from Local's DescribeTable, every other
// line one item in DynamoDB JSON, members sorted, the lines ordered by the
// item's key in the same encoding. The shim's own state (tags, TTL, PITR,
// table ids) travels in the cell sections. Local's SQLite files are never
// copied, and its stream labels change across a load.

// sectionExt is the extension of an item section.
const sectionExt = ".ndjson"

// batchSize is the most puts one BatchWriteItem carries.
const batchSize = 25

// batchAttempts bounds how often one batch is sent while Local leaves
// some of it unprocessed, which it does only under load AWS would throttle.
const batchAttempts = 10

// The service's items are late data.
var _ kernel.Late = (*Service)(nil)

// ExportLate exports, through Local, the items of every table Local holds
// in the scopes the shim records: by Local's own list, not the shim's, so
// a table the shim has not adopted yet (one kept under --persist) is saved
// too, as ImportLate deletes every table of those scopes. It comes back
// without a record and is adopted again on first use. A table deleted
// since the cut is left out.
func (s *Service) ExportLate(ctx context.Context) ([]store.Section, error) {
	var out []store.Section
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		names, err := s.localTables(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("listing the DynamoDB tables of account %s in %s: %w", scope.Account, scope.Region, err)
		}
		for _, name := range names {
			sec, ok, err := s.exportTable(ctx, tableKey{scope: scope, table: name})
			if err != nil {
				return nil, fmt.Errorf("exporting the items of DynamoDB table %s: %w", tableARN(scope, name), err)
			}
			if ok {
				out = append(out, sec)
			}
		}
	}
	slices.SortFunc(out, func(a, b store.Section) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

func (s *Service) exportTable(ctx context.Context, k tableKey) (store.Section, bool, error) {
	var described dynamodbapi.DescribeTableOutput
	err := s.direct(ctx, k.scope, dynamodbapi.OpDescribeTableRaw.Descriptor(), &dynamodbapi.DescribeTableInput{TableName: k.table}, &described)
	if le, ok := errors.AsType[*LocalError](err); ok && le.is("ResourceNotFoundException") {
		return store.Section{}, false, nil
	}
	if err != nil {
		return store.Section{}, false, err
	}
	if described.Table == nil {
		return store.Section{}, false, &AnswerError{Op: dynamodbapi.OpDescribeTableRaw.Descriptor().ID, Reason: "the answer has no Table"}
	}
	create, aerr := encodeInput(dynamodbapi.OpCreateTableRaw.Descriptor(), recreate(described.Table))
	if aerr != nil {
		return store.Section{}, false, aerr.AsError()
	}
	keys := make([]string, 0, len(described.Table.KeySchema))
	for _, e := range described.Table.KeySchema {
		keys = append(keys, e.AttributeName)
	}
	type line struct{ key, item []byte }
	var lines []line
	err = s.scanItems(ctx, k, func(item document.Map) error {
		key := make(document.Map, 0, len(keys))
		for _, name := range keys {
			key = append(key, document.Entry{Key: name, Value: member(item, name)})
		}
		kb, err := canonical(key)
		if err != nil {
			return err
		}
		ib, err := canonical(item)
		if err != nil {
			return err
		}
		lines = append(lines, line{key: kb, item: ib})
		return nil
	})
	if err != nil {
		return store.Section{}, false, err
	}
	slices.SortFunc(lines, func(a, b line) int { return bytes.Compare(a.key, b.key) })
	var data bytes.Buffer
	data.Write(create)
	data.WriteByte('\n')
	for _, l := range lines {
		data.Write(l.item)
		data.WriteByte('\n')
	}
	return store.Section{Path: k.path() + sectionExt, Version: stateVersion, Data: data.Bytes()}, true, nil
}

// recreate is the CreateTable request that rebuilds the table d describes:
// its keys, indexes, billing, streams and deletion protection. What the
// shim owns is restored from the cell sections instead.
func recreate(d *dynamodbapi.TableDescription) *dynamodbapi.CreateTableInput {
	in := &dynamodbapi.CreateTableInput{
		AttributeDefinitions:      d.AttributeDefinitions,
		KeySchema:                 d.KeySchema,
		DeletionProtectionEnabled: d.DeletionProtectionEnabled,
		OnDemandThroughput:        d.OnDemandThroughput,
	}
	if d.TableName != nil {
		in.TableName = *d.TableName
	}
	billing := dynamodbapi.BillingModeProvisioned
	if d.BillingModeSummary != nil && d.BillingModeSummary.BillingMode != nil {
		billing = *d.BillingModeSummary.BillingMode
	}
	in.BillingMode = &billing
	provisioned := billing == dynamodbapi.BillingModeProvisioned
	if provisioned {
		in.ProvisionedThroughput = throughput(d.ProvisionedThroughput)
	}
	if d.StreamSpecification != nil && d.StreamSpecification.StreamEnabled {
		in.StreamSpecification = d.StreamSpecification
	}
	for _, x := range d.LocalSecondaryIndexes {
		in.LocalSecondaryIndexes = append(in.LocalSecondaryIndexes, dynamodbapi.LocalSecondaryIndex{
			IndexName: deref(x.IndexName), KeySchema: x.KeySchema, Projection: derefProjection(x.Projection),
		})
	}
	for _, x := range d.GlobalSecondaryIndexes {
		g := dynamodbapi.GlobalSecondaryIndex{
			IndexName: deref(x.IndexName), KeySchema: x.KeySchema, Projection: derefProjection(x.Projection),
			OnDemandThroughput: x.OnDemandThroughput,
		}
		if provisioned {
			g.ProvisionedThroughput = throughput(x.ProvisionedThroughput)
		}
		in.GlobalSecondaryIndexes = append(in.GlobalSecondaryIndexes, g)
	}
	return in
}

// throughput is the provisioned throughput a description reports, as a
// request sets it.
func throughput(d *dynamodbapi.ProvisionedThroughputDescription) *dynamodbapi.ProvisionedThroughput {
	p := &dynamodbapi.ProvisionedThroughput{}
	if d != nil && d.ReadCapacityUnits != nil {
		p.ReadCapacityUnits = *d.ReadCapacityUnits
	}
	if d != nil && d.WriteCapacityUnits != nil {
		p.WriteCapacityUnits = *d.WriteCapacityUnits
	}
	return p
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefProjection(p *dynamodbapi.Projection) dynamodbapi.Projection {
	if p == nil {
		return dynamodbapi.Projection{}
	}
	return *p
}

// canonical encodes v with members sorted, the items' line encoding.
func canonical(v document.Value) ([]byte, error) {
	enc := jsonx.NewEncoder(jsonx.SortedKeys)
	enc.Value(v)
	return enc.Bytes()
}

// scanItems reads every item of k, consistently, page by page, as Local
// wrote it: numbers and binaries keep their text.
func (s *Service) scanItems(ctx context.Context, k tableKey, each func(item document.Map) error) error {
	var start document.Value
	for {
		enc := jsonx.NewEncoder(jsonx.WireOrder)
		enc.BeginObject()
		enc.Name("TableName")
		enc.String(k.table)
		enc.Name("ConsistentRead")
		enc.Bool(true)
		if start != nil {
			enc.Name("ExclusiveStartKey")
			enc.Value(start)
		}
		enc.EndObject()
		body, err := enc.Bytes()
		if err != nil {
			return err
		}
		scan := dynamodbapi.OpScanRaw.Descriptor()
		answer, err := s.directBody(ctx, k.scope, scan, body)
		if err != nil {
			return err
		}
		page, err := readPage(scan, answer)
		if err != nil {
			return err
		}
		for _, item := range page.items {
			if err := each(item); err != nil {
				return err
			}
		}
		if page.next == nil {
			return nil
		}
		start = page.next
	}
}

// scanPage is one page of a Scan as Local wrote it.
type scanPage struct {
	items []document.Map
	// next is LastEvaluatedKey; nil on the last page.
	next document.Value
}

func readPage(op *registry.Operation, body []byte) (scanPage, error) {
	dec := jsonx.NewDecoder(body)
	doc, err := dec.ReadValue()
	if err != nil {
		return scanPage{}, &AnswerError{Op: op.ID, Reason: err.Error()}
	}
	m, ok := doc.(document.Map)
	if !ok {
		return scanPage{}, &AnswerError{Op: op.ID, Reason: "the answer is not a JSON object"}
	}
	var page scanPage
	items, ok := member(m, "Items").(document.List)
	if !ok {
		return scanPage{}, &AnswerError{Op: op.ID, Reason: "the answer has no Items list"}
	}
	for _, it := range items {
		item, ok := it.(document.Map)
		if !ok {
			return scanPage{}, &AnswerError{Op: op.ID, Reason: "an item is not a JSON object"}
		}
		page.items = append(page.items, item)
	}
	if next, ok := member(m, "LastEvaluatedKey").(document.Map); ok && len(next) > 0 {
		page.next = next
	}
	return page, nil
}

// ImportLate replaces every table in Local with the tables of ss: the
// tables of every scope the shim or ss names are deleted, then each
// section's table is created and its items written. The cell sections the
// load imports next restore what the shim records about them.
func (s *Service) ImportLate(ctx context.Context, ss []store.Section) error {
	tables := make([]lateTable, 0, len(ss))
	scopes := s.cells.Keys(store.ScopeKeys())
	for _, sec := range ss {
		t, err := parseSection(sec)
		if err != nil {
			return err
		}
		tables = append(tables, t)
		if !slices.Contains(scopes, t.key.scope) {
			scopes = append(scopes, t.key.scope)
		}
	}
	if err := s.dropTables(ctx, scopes); err != nil {
		return err
	}
	for _, t := range tables {
		if err := s.importTable(ctx, t); err != nil {
			return fmt.Errorf("importing DynamoDB table %s: %w", tableARN(t.key.scope, t.key.table), err)
		}
	}
	return nil
}

// lateTable is one parsed item section.
type lateTable struct {
	key    tableKey
	create []byte
	items  [][]byte
}

// parseSection reads an item section, refusing one of another schema
// version or layout before anything in Local changes.
func parseSection(sec store.Section) (lateTable, error) {
	if sec.Version != stateVersion {
		return lateTable{}, &store.VersionMismatchError{Path: sec.Path, Section: sec.Version, Running: stateVersion}
	}
	path, ok := strings.CutSuffix(sec.Path, sectionExt)
	if !ok {
		return lateTable{}, &SectionError{Path: sec.Path, Reason: "the path does not end in " + sectionExt}
	}
	key, err := parseTableKey(path)
	if err != nil {
		return lateTable{}, &SectionError{Path: sec.Path, Reason: err.Error()}
	}
	lines := bytes.Split(bytes.TrimSuffix(sec.Data, []byte("\n")), []byte("\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		return lateTable{}, &SectionError{Path: sec.Path, Reason: "the first line, the CreateTable request, is missing"}
	}
	if reason, ok := createsTable(lines[0], key.table); !ok {
		return lateTable{}, &SectionError{Path: sec.Path, Reason: reason}
	}
	t := lateTable{key: key, create: lines[0]}
	for i, l := range lines[1:] {
		dec := jsonx.NewDecoder(l)
		doc, err := dec.ReadValue()
		if _, isItem := doc.(document.Map); err != nil || !isItem || dec.End() != nil {
			return lateTable{}, &SectionError{Path: sec.Path, Reason: fmt.Sprintf("line %d is not one item as a JSON object", i+2)}
		}
		t.items = append(t.items, l)
	}
	return t, nil
}

// createsTable checks the first line of an item section: a CreateTable
// request that decodes and passes the model's validation, as the edge
// would check it, and names table, the table of the section's path. A
// hand-edited line is refused here, before any table in Local is deleted,
// with the reason, instead of by Local after the reset.
func createsTable(line []byte, table string) (string, bool) {
	op := dynamodbapi.OpCreateTableRaw.Descriptor()
	var in dynamodbapi.CreateTableInput
	r := &http.Request{Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(line))}
	if err := (awsjson.Codec{Version: awsjson.V10}).Decode(r, op, &in); err != nil {
		reason := "the first line is not a CreateTable request in JSON"
		if err.Message != "" {
			reason += ": " + err.Message
		}
		return reason, false
	}
	if vs := in.Validate(); len(vs) > 0 {
		return "the first line's CreateTable request is invalid: " + in.ValidationError(vs).Message, false
	}
	if in.TableName != table {
		return fmt.Sprintf("the first line creates table %q, but the path names table %q", in.TableName, table), false
	}
	// The model leaves both optional; every table has them, and the export
	// writes them (recreate).
	if len(in.KeySchema) == 0 || len(in.AttributeDefinitions) == 0 {
		return "the first line's CreateTable request has no KeySchema or no AttributeDefinitions", false
	}
	return "", true
}

// SectionError is an item section ImportLate cannot read.
type SectionError struct {
	Path   string
	Reason string
}

func (e *SectionError) Error() string {
	return fmt.Sprintf("DynamoDB item section %q cannot be imported: %s; nothing in DynamoDB Local was changed", e.Path, e.Reason)
}

func (s *Service) importTable(ctx context.Context, t lateTable) error {
	if _, err := s.directBody(ctx, t.key.scope, dynamodbapi.OpCreateTableRaw.Descriptor(), t.create); err != nil {
		return err
	}
	for batch := range slices.Chunk(t.items, batchSize) {
		if err := s.writeBatch(ctx, t.key, batch); err != nil {
			return err
		}
	}
	return nil
}

// writeBatch puts items into k with BatchWriteItem, sending again what
// Local leaves unprocessed until it takes all of them, at most
// batchAttempts times.
func (s *Service) writeBatch(ctx context.Context, k tableKey, items [][]byte) error {
	enc := jsonx.NewEncoder(jsonx.WireOrder)
	enc.BeginObject()
	enc.Name("RequestItems")
	enc.BeginObject()
	enc.Name(k.table)
	enc.BeginArray()
	for _, item := range items {
		dec := jsonx.NewDecoder(item)
		doc, err := dec.ReadValue()
		if err != nil {
			return err
		}
		enc.BeginObject()
		enc.Name("PutRequest")
		enc.BeginObject()
		enc.Name("Item")
		enc.Value(doc)
		enc.EndObject()
		enc.EndObject()
	}
	enc.EndArray()
	enc.EndObject()
	enc.EndObject()
	body, err := enc.Bytes()
	if err != nil {
		return err
	}
	write := dynamodbapi.OpBatchWriteItemRaw.Descriptor()
	for range batchAttempts {
		answer, err := s.directBody(ctx, k.scope, write, body)
		if err != nil {
			return err
		}
		dec := jsonx.NewDecoder(answer)
		doc, err := dec.ReadValue()
		if err != nil {
			return &AnswerError{Op: write.ID, Reason: err.Error()}
		}
		m, ok := doc.(document.Map)
		if !ok {
			return &AnswerError{Op: write.ID, Reason: "the answer is not a JSON object"}
		}
		left, ok := member(m, "UnprocessedItems").(document.Map)
		if !ok || len(left) == 0 {
			return nil
		}
		retry := jsonx.NewEncoder(jsonx.WireOrder)
		retry.BeginObject()
		retry.Name("RequestItems")
		retry.Value(left)
		retry.EndObject()
		if body, err = retry.Bytes(); err != nil {
			return err
		}
	}
	return fmt.Errorf("DynamoDB Local left items unprocessed after %d BatchWriteItem attempts; the table is only partly imported, so load the snapshot again", batchAttempts)
}

// direct sends in, a request of op, to Local in scope and reads the answer
// into out (nil to ignore it). Snapshot export and import, and the reset,
// are not calls: they reach Local without the pipeline, so they never
// match fault rules.
func (s *Service) direct(ctx context.Context, scope aws.Scope, op *registry.Operation, in smithy.Serializable, out smithy.Deserializable) error {
	body, aerr := encodeInput(op, in)
	if aerr != nil {
		return aerr.AsError()
	}
	resp, err := s.directRaw(ctx, scope, op, body)
	if err != nil {
		return err
	}
	return decodeAnswer(op, resp, out)
}

// directBody sends body as a request of op to Local in scope and returns
// the body of its successful answer.
func (s *Service) directBody(ctx context.Context, scope aws.Scope, op *registry.Operation, body []byte) ([]byte, error) {
	resp, err := s.directRaw(ctx, scope, op, body)
	if err != nil {
		return nil, err
	}
	if err := decodeAnswer(op, resp, nil); err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (s *Service) directRaw(ctx context.Context, scope aws.Scope, op *registry.Operation, body []byte) (kernel.RawResponse, error) {
	endpoint, aerr := s.ready(ctx)
	if aerr != nil {
		return kernel.RawResponse{}, aerr.AsError()
	}
	resp, aerr := s.send(ctx, endpoint, exchange{scope: scope, op: op.ID, at: s.kernel.Clock().Now()}, body)
	if aerr != nil {
		return kernel.RawResponse{}, aerr.AsError()
	}
	return resp, nil
}
