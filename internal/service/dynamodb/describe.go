package dynamodb

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Warm throughput AWS reports for a table nobody configured; the Terraform
// provider hides exactly these values from state (dynamodb-local.md 6).
const (
	defaultWarmReads  = 12000
	defaultWarmWrites = 4000
)

// shimmed completes the table description in one of Local's answers from
// the shim (dynamodb-local.md 7.3): WarmThroughput, which Terraform waits
// on, in the answers AWS gives one; TableId, TableClassSummary,
// SSEDescription and the emulator clock's timestamps (creation, the switch
// to PAY_PER_REQUEST, the table class update and each throughput increase
// and decrease) when the shim records the table. It then leaves out what
// AWS's answer to the operation leaves out (answerOf).
type shimmed struct {
	// table is the shim's record; nil for a table Local holds that the shim
	// never saw created, which gets WarmThroughput only.
	table *Table
	// of is the operation answered, which sets the TableStatus and the
	// members the answer carries.
	of answerOf
	// under renames the member holding the description: an UpdateTable
	// answered from Local's DescribeTable moves Table to TableDescription.
	under string
	// stream replaces Local's LatestStreamArn and LatestStreamLabel with
	// understudy's (dynamodb-streams.md 11.1 item 6); nil keeps Local's.
	stream *streamMembers
	// seen, when set, receives what Local's description says of the
	// stream the call enables, before stream replaces it.
	seen *localStreamSeen
}

// answerOf is the operation whose answer holds a table description. AWS
// gives each its own members (testdata/parity/dynamodb/R04/01-create-table.json,
// 02-describe-table.json, x-billing-to-provisioned.json,
// 99-delete-table.json).
type answerOf int

const (
	// describeAnswer is DescribeTable's, which keeps Local's TableStatus.
	describeAnswer answerOf = iota
	// createAnswer is CreateTable's: CREATING, for its global secondary
	// indexes too, without WarmThroughput or the switch to
	// PAY_PER_REQUEST in BillingModeSummary.
	createAnswer
	// updateAnswer is UpdateTable's: UPDATING.
	updateAnswer
	// deleteAnswer is DeleteTable's: DELETING, without AttributeDefinitions,
	// KeySchema, CreationDateTime or WarmThroughput.
	deleteAnswer
)

// status is the TableStatus the answer reports where Local answers ACTIVE
// (dynamodb-local.md 4.2); empty keeps Local's.
func (o answerOf) status() dynamodbapi.TableStatus {
	switch o {
	case createAnswer:
		return dynamodbapi.TableStatusCreating
	case updateAnswer:
		return dynamodbapi.TableStatusUpdating
	case deleteAnswer:
		return dynamodbapi.TableStatusDeleting
	case describeAnswer:
	}
	return ""
}

// warm reports whether the answer carries the table's WarmThroughput.
func (o answerOf) warm() bool { return o == describeAnswer || o == updateAnswer }

// trimmed is desc without the members AWS leaves out of the answer.
func (o answerOf) trimmed(desc document.Map) document.Map {
	switch o {
	case createAnswer:
		if billing, ok := member(desc, "BillingModeSummary").(document.Map); ok {
			desc = setMember(desc, "BillingModeSummary", removeMember(billing, "LastUpdateToPayPerRequestDateTime"))
		}
	case deleteAnswer:
		for _, key := range []string{"AttributeDefinitions", "KeySchema", "CreationDateTime"} {
			desc = removeMember(desc, key)
		}
	case describeAnswer, updateAnswer:
	}
	return desc
}

// localStreamSeen is what the feed needs from Local's description of a
// table whose stream a call enabled: the label of Local's stream, which
// names it in Local's ARN form, and the table's key schema, which
// DescribeStream reports.
type localStreamSeen struct {
	label string
	keys  []dynamodbstreamsapi.KeySchemaElement
}

// read takes Local's LatestStreamLabel and KeySchema from desc.
func (l *localStreamSeen) read(desc document.Map) {
	if label, ok := member(desc, "LatestStreamLabel").(document.String); ok {
		l.label = string(label)
	}
	keys, isList := member(desc, "KeySchema").(document.List)
	if !isList {
		return
	}
	for _, k := range keys {
		m, isMap := k.(document.Map)
		if !isMap {
			continue
		}
		name, named := member(m, "AttributeName").(document.String)
		kind, typed := member(m, "KeyType").(document.String)
		if named && typed {
			l.keys = append(l.keys, dynamodbstreamsapi.KeySchemaElement{AttributeName: string(name), KeyType: dynamodbstreamsapi.KeyType(kind)})
		}
	}
}

// described is how the description of name in scope is completed.
func (s *Service) described(scope aws.Scope, name string, of answerOf) *shimmed {
	d := &shimmed{of: of, stream: s.latestStream(scope, tableName(name))}
	if t, ok := s.lookup(scope, tableName(name)); ok {
		d.table = &t
	}
	return d
}

// describeTable forwards DescribeTable and completes the description.
func (s *Service) describeTable(ctx context.Context, c *kernel.Call, in *dynamodbapi.DescribeTableInput, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
	a := tableAnswer(in.TableName)
	a.shim = s.described(c.Scope(), in.TableName, describeAnswer)
	return s.forward(ctx, c, in, w, a, []string{in.TableName})
}

// complete patches the description an answer holds under Table
// (DescribeTable) or TableDescription (CreateTable, UpdateTable,
// DeleteTable), and reports whether it changed anything. An answer
// without one, such as the {} of a fake, stays as it is.
func (d *shimmed) complete(answer document.Map) bool {
	for i, e := range answer {
		if e.Key != "Table" && e.Key != "TableDescription" {
			continue
		}
		desc, ok := e.Value.(document.Map)
		if !ok {
			return false
		}
		answer[i].Value = d.of.trimmed(d.patch(desc))
		if d.under != "" {
			answer[i].Key = d.under
		}
		return true
	}
	return false
}

// patch returns desc with the shim's members set, each replacing Local's
// member of that name or appended after Local's members.
func (d *shimmed) patch(desc document.Map) document.Map {
	status, reported := member(desc, "TableStatus").(document.String)
	if !reported {
		status = document.String(dynamodbapi.TableStatusActive)
	}
	if s := d.of.status(); s != "" {
		status = document.String(s)
		desc = setMember(desc, "TableStatus", status)
	}
	if d.of.warm() {
		warm := Warm{Reads: defaultWarmReads, Writes: defaultWarmWrites}
		if t := d.table; t != nil && t.Warm != nil {
			warm = *t.Warm
		}
		desc = setMember(desc, "WarmThroughput", warmDescription(warm, status))
	}
	t := d.table
	var indexStatus dynamodbapi.IndexStatus
	if d.of == createAnswer {
		indexStatus = dynamodbapi.IndexStatusCreating
	}
	patchIndexes(desc, t, indexStatus)
	if d.seen != nil {
		d.seen.read(desc)
	}
	if d.stream != nil {
		desc = setMember(desc, "LatestStreamArn", document.String(d.stream.arn))
		desc = setMember(desc, "LatestStreamLabel", document.String(d.stream.label))
	}
	if t == nil {
		return desc
	}
	desc = setMember(desc, "TableId", document.String(t.ID))
	if member(desc, "CreationDateTime") != nil {
		desc = setMember(desc, "CreationDateTime", epochSeconds(t.Created))
	}
	if billing, ok := member(desc, "BillingModeSummary").(document.Map); ok && member(billing, "LastUpdateToPayPerRequestDateTime") != nil {
		desc = setMember(desc, "BillingModeSummary", lastOnDemandPatched(billing, t.Billing))
	}
	if t.Class != nil {
		summary := document.Map{{Key: "TableClass", Value: document.String(t.Class.Name)}}
		if !t.Class.Updated.IsZero() {
			summary = append(summary, document.Entry{Key: "LastUpdateDateTime", Value: epochSeconds(t.Class.Updated)})
		}
		desc = setMember(desc, "TableClassSummary", summary)
	}
	if t.SSE != nil {
		desc = setMember(desc, "SSEDescription", document.Map{
			{Key: "Status", Value: document.String(dynamodbapi.SSEStatusEnabled)},
			{Key: "SSEType", Value: document.String(dynamodbapi.SSETypeKms)},
			{Key: "KMSMasterKeyArn", Value: document.String(t.SSE.KMSKeyARN)},
		})
	}
	return throughputPatched(desc, t.Throughput)
}

// warmDescription is a table's or an index's WarmThroughput description.
// AWS reports the table's TableStatus as a table's Status and the index's
// IndexStatus as an index's.
func warmDescription(w Warm, status document.String) document.Map {
	return document.Map{
		{Key: "ReadUnitsPerSecond", Value: document.Number(strconv.FormatInt(w.Reads, 10))},
		{Key: "WriteUnitsPerSecond", Value: document.Number(strconv.FormatInt(w.Writes, 10))},
		{Key: "Status", Value: status},
	}
}

// lastOnDemandPatched replaces the wall-clock LastUpdateToPayPerRequestDateTime
// Local reports in billing, a BillingModeSummary, with the emulator-clock
// instant the table last became PAY_PER_REQUEST, which AWS keeps after a
// switch back to PROVISIONED. A table the shim never saw become
// PAY_PER_REQUEST reports none: Local's instant is its own wall clock.
func lastOnDemandPatched(billing document.Map, b Billing) document.Map {
	if last, ok := b.lastOnDemand(); ok {
		return setMember(billing, "LastUpdateToPayPerRequestDateTime", epochSeconds(last))
	}
	return removeMember(billing, "LastUpdateToPayPerRequestDateTime")
}

// patchIndexes completes each global secondary index desc describes, in
// place. status replaces Local's IndexStatus; empty keeps it. Every index
// gets the WarmThroughput Terraform waits on after it adds or changes an
// index: the record's IndexWarm, or AWS's default, with the index's
// IndexStatus. An index of a table the shim records (t not nil) also gets
// the emulator clock's throughput instants.
func patchIndexes(desc document.Map, t *Table, status dynamodbapi.IndexStatus) {
	indexes, ok := member(desc, "GlobalSecondaryIndexes").(document.List)
	if !ok {
		return
	}
	for i, x := range indexes {
		index, ok := x.(document.Map)
		if !ok {
			continue
		}
		var name string
		if n, named := member(index, "IndexName").(document.String); named {
			name = string(n)
		}
		indexStatus, reported := member(index, "IndexStatus").(document.String)
		if !reported {
			indexStatus = document.String(dynamodbapi.IndexStatusActive)
		}
		if status != "" {
			indexStatus = document.String(status)
			index = setMember(index, "IndexStatus", indexStatus)
		}
		warm := Warm{Reads: defaultWarmReads, Writes: defaultWarmWrites}
		if t != nil {
			if have, ok := t.IndexWarm[name]; ok {
				warm = have
			}
		}
		index = setMember(index, "WarmThroughput", warmDescription(warm, indexStatus))
		if t != nil {
			var p *Throughput
			if have, ok := t.IndexThroughput[name]; ok {
				p = &have
			}
			index = throughputPatched(index, p)
		}
		indexes[i] = index
	}
}

// throughputPatched replaces the wall-clock LastIncreaseDateTime and
// LastDecreaseDateTime of the ProvisionedThroughput in desc, a table's or
// an index's, with the emulator-clock instants of p. Where the shim saw no
// change the member is left out, as AWS leaves it out, where Local writes
// 0.
func throughputPatched(desc document.Map, p *Throughput) document.Map {
	pt, ok := member(desc, "ProvisionedThroughput").(document.Map)
	if !ok {
		return desc
	}
	var increased, decreased clock.Instant
	if p != nil {
		increased, decreased = p.Increased, p.Decreased
	}
	for _, m := range []struct {
		key string
		at  clock.Instant
	}{{"LastIncreaseDateTime", increased}, {"LastDecreaseDateTime", decreased}} {
		if m.at.IsZero() {
			pt = removeMember(pt, m.key)
			continue
		}
		pt = setMember(pt, m.key, epochSeconds(m.at))
	}
	return setMember(desc, "ProvisionedThroughput", pt)
}

// member returns the value of m's member key, nil when it has none.
func member(m document.Map, key string) document.Value {
	for _, e := range m {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

// setMember sets m's member key to v in place, or appends it.
func setMember(m document.Map, key string, v document.Value) document.Map {
	for i, e := range m {
		if e.Key == key {
			m[i].Value = v
			return m
		}
	}
	return append(m, document.Entry{Key: key, Value: v})
}

// removeMember removes m's member key in place, if it has one.
func removeMember(m document.Map, key string) document.Map {
	return slices.DeleteFunc(m, func(e document.Entry) bool { return e.Key == key })
}

// epochSeconds is i as DynamoDB writes a timestamp: Unix seconds with
// millisecond digits, as in 1791434323.298.
func epochSeconds(i clock.Instant) document.Number {
	ms := i.Wire().UnixMilli()
	sec, frac := ms/1000, ms%1000
	if frac < 0 {
		sec, frac = sec-1, frac+1000
	}
	return document.Number(fmt.Sprintf("%d.%03d", sec, frac))
}

// lookup returns the shim's record of the table name in scope.
func (s *Service) lookup(scope aws.Scope, name string) (Table, bool) {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return Table{}, false
	}
	type found struct {
		t  Table
		ok bool
	}
	f := store.Read(cell, func(st *State) found {
		t, ok := st.Tables[name]
		return found{t: t.clone(), ok: ok}
	})
	return f.t, f.ok
}
