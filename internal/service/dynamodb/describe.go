package dynamodb

import (
	"context"
	"fmt"
	"slices"
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
	// status replaces Local's TableStatus; empty keeps it. CreateTable sets
	// CREATING, UpdateTable UPDATING and DeleteTable DELETING where Local
	// answers ACTIVE (dynamodb-local.md 4.2). A table CREATING has its
	// global secondary indexes CREATING too.
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
	warm := Warm{Reads: defaultWarmReads, Writes: defaultWarmWrites}
	if t := d.table; t != nil && t.Warm != nil {
		warm = *t.Warm
	}
	desc = setMember(desc, "WarmThroughput", warmDescription(warm, status))
	t := d.table
	var indexStatus dynamodbapi.IndexStatus
	if d.status == dynamodbapi.TableStatusCreating {
		indexStatus = dynamodbapi.IndexStatusCreating
	}
	patchIndexes(desc, t, indexStatus)
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
