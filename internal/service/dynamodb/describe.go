package dynamodb

import (
	"context"
	"fmt"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
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
// on, always; TableId, TableClassSummary, SSEDescription and the emulator
// clock's timestamps (creation, the switch to PAY_PER_REQUEST, the table
// class update and each throughput increase and decrease) when the shim
// records the table.
type shimmed struct {
	// table is the shim's record; nil for a table Local holds that the shim
	// never saw created, which gets WarmThroughput only.
	table *Table
	// status replaces Local's TableStatus; empty keeps it. DeleteTable sets
	// DELETING where Local answers ACTIVE (dynamodb-local.md 4.2).
	status dynamodbapi.TableStatus
	// under renames the member holding the description: an UpdateTable
	// answered from Local's DescribeTable moves Table to TableDescription.
	under string
}

// described is how the description of name in scope is completed.
func (s *Service) described(scope aws.Scope, name string, status dynamodbapi.TableStatus) *shimmed {
	t, ok := s.lookup(scope, tableName(name))
	if !ok {
		return &shimmed{status: status}
	}
	return &shimmed{table: &t, status: status}
}

// describeTable forwards DescribeTable and completes the description.
func (s *Service) describeTable(ctx context.Context, c *kernel.Call, in *dynamodbapi.DescribeTableInput, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
	a := tableAnswer(in.TableName)
	a.shim = s.described(c.Scope(), in.TableName, "")
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
		answer[i].Value = d.patch(desc)
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
	if d.status != "" {
		status = document.String(d.status)
		desc = setMember(desc, "TableStatus", status)
	}
	reads, writes := int64(defaultWarmReads), int64(defaultWarmWrites)
	if t := d.table; t != nil && t.Warm != nil {
		reads, writes = t.Warm.Reads, t.Warm.Writes
	}
	desc = setMember(desc, "WarmThroughput", document.Map{
		{Key: "ReadUnitsPerSecond", Value: document.Number(strconv.FormatInt(reads, 10))},
		{Key: "WriteUnitsPerSecond", Value: document.Number(strconv.FormatInt(writes, 10))},
		{Key: "Status", Value: status},
	})
	t := d.table
	if t == nil {
		return desc
	}
	desc = setMember(desc, "TableId", document.String(t.ID))
	if member(desc, "CreationDateTime") != nil {
		desc = setMember(desc, "CreationDateTime", epochSeconds(t.Created))
	}
	if billing, ok := member(desc, "BillingModeSummary").(document.Map); ok && !t.OnDemandSince.IsZero() &&
		member(billing, "LastUpdateToPayPerRequestDateTime") != nil {
		desc = setMember(desc, "BillingModeSummary", setMember(billing, "LastUpdateToPayPerRequestDateTime", epochSeconds(t.OnDemandSince)))
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
	desc = throughputPatched(desc, t.Throughput)
	patchIndexes(desc, t.IndexThroughput)
	return desc
}

// patchIndexes patches the throughput of each global secondary index desc
// describes, in place, from the record by index name.
func patchIndexes(desc document.Map, record map[string]Throughput) {
	indexes, ok := member(desc, "GlobalSecondaryIndexes").(document.List)
	if !ok {
		return
	}
	for i, x := range indexes {
		index, ok := x.(document.Map)
		if !ok {
			continue
		}
		var p *Throughput
		if name, ok := member(index, "IndexName").(document.String); ok {
			if have, ok := record[string(name)]; ok {
				p = &have
			}
		}
		indexes[i] = throughputPatched(index, p)
	}
}

// throughputPatched replaces the wall-clock LastIncreaseDateTime and
// LastDecreaseDateTime of the ProvisionedThroughput in desc, a table's or
// an index's, with the emulator-clock instants of p, or with 0 where the
// shim saw no change, as Local reports a table whose throughput never
// changed.
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
		switch {
		case !m.at.IsZero():
			pt = setMember(pt, m.key, epochSeconds(m.at))
		case member(pt, m.key) != nil:
			pt = setMember(pt, m.key, document.Number("0"))
		}
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
