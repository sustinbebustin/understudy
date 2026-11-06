package dynamodb_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
)

// localStreams is DynamoDB Local's stream behind a tableStore, as observed
// on the pinned image (dynamodb-streams.md 8): ARNs in region ddblocal and
// account 000000000000 (8.3 row 1), a 21-digit zero-padded sequence counter
// shared by every table (row 7), UUID event ids (row 8), wall-clock
// timestamps floored to the minute (row 2), "<arn>|001|<base64>" iterators
// (row 9), one shard per stream, no record for a write that changes nothing
// or a DeleteItem of an absent key (7.6), and Local's UpdateTable stream
// texts (5.1).
type localStreams struct {
	store *tableStore

	mu      sync.Mutex
	current map[string]*localStream // by table, the latest stream
	byARN   map[string]*localStream
	counter int
	labels  int
	// failStreams makes every Streams call fail, as an unreachable or
	// failing Local does.
	failStreams bool
	// statement is what an ExecuteStatement writes: the fake does not parse
	// PartiQL, the test says which table and item a statement puts.
	statement func(stmt string) (table string, it item)
}

type localStream struct {
	arn, label, table, viewType, shard string
	enabled                            bool
	records                            []localRecord
}

// localRecord is one record of Local's stream, as GetRecords answers it.
type localRecord struct {
	Seq       string
	EventName string
	Keys      item
	NewImage  item
	OldImage  item
	SizeBytes int
	EventID   string
}

func newLocalStreams() *localStreams {
	return &localStreams{store: newTableStore(), current: map[string]*localStream{}, byARN: map[string]*localStream{}}
}

const streamsTarget = "DynamoDBStreams_20120810."

func (l *localStreams) serve(op, body string) reply {
	if name, isStreams := strings.CutPrefix(op, streamsTarget); isStreams {
		return l.serveStreams(name, body)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var in struct {
		TableName           string
		StreamSpecification *struct {
			StreamEnabled  bool
			StreamViewType string
		}
		RequestItems  map[string]json.RawMessage
		TransactItems []struct {
			Put *struct {
				TableName string
				Item      item
			}
		}
		Statement string
	}
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		return localErr("com.amazon.coral.service#SerializationException", err.Error())
	}
	switch op {
	case "CreateTable":
		rep := l.store.serve(op, body)
		if rep.Status == http.StatusOK && in.StreamSpecification != nil && in.StreamSpecification.StreamEnabled {
			l.enable(in.TableName, in.StreamSpecification.StreamViewType)
			return ok(map[string]any{"TableDescription": l.desc(in.TableName)})
		}
		return rep
	case "UpdateTable":
		if in.StreamSpecification == nil {
			return l.store.serve(op, body)
		}
		s := l.current[in.TableName]
		spec := in.StreamSpecification
		switch {
		case spec.StreamEnabled && s != nil && s.enabled:
			return localErr("com.amazon.coral.validate#ValidationException", "Table already has an enabled stream")
		case !spec.StreamEnabled && (s == nil || !s.enabled):
			return localErr("com.amazon.coral.validate#ValidationException", "Table has no stream to disable")
		case spec.StreamEnabled && spec.StreamViewType == "":
			return localErr("com.amazon.coral.validate#ValidationException", "Invalid StreamViewType: Stream ViewType must not be null or empty when enabling streams.")
		case !spec.StreamEnabled && spec.StreamViewType != "":
			return localErr("com.amazon.coral.validate#ValidationException", "Invalid StreamViewType: Stream StreamEnabled was false but a StreamViewType was present.")
		case spec.StreamEnabled:
			l.enable(in.TableName, spec.StreamViewType)
		default:
			s.enabled = false
			l.store.mu.Lock()
			delete(l.store.tables[in.TableName].desc, "StreamSpecification")
			l.store.mu.Unlock()
		}
		return ok(map[string]any{"TableDescription": l.desc(in.TableName)})
	case "DeleteTable":
		if s := l.current[in.TableName]; s != nil {
			s.enabled = false
			delete(l.current, in.TableName)
		}
		return l.store.serve(op, body)
	case "PutItem", "UpdateItem", "DeleteItem":
		return l.write([]string{in.TableName}, func() reply { return l.store.serve(op, body) })
	case "BatchWriteItem":
		return l.write(slices.Sorted(maps.Keys(in.RequestItems)), func() reply { return l.store.serve(op, body) })
	case "TransactWriteItems":
		var tables []string
		for _, ti := range in.TransactItems {
			if ti.Put != nil {
				tables = append(tables, ti.Put.TableName)
			}
		}
		return l.write(tables, func() reply {
			l.store.mu.Lock()
			defer l.store.mu.Unlock()
			for _, ti := range in.TransactItems {
				if t := l.store.tables[ti.Put.TableName]; t != nil {
					t.items[t.keyOf(ti.Put.Item)] = ti.Put.Item
				}
			}
			return ok(map[string]any{})
		})
	case "ExecuteStatement":
		if l.statement == nil {
			return localErr("com.amazon.coral.validate#ValidationException", "the fake was given no statement")
		}
		table, it := l.statement(in.Statement)
		return l.write([]string{table}, func() reply {
			l.store.mu.Lock()
			defer l.store.mu.Unlock()
			t := l.store.tables[table]
			t.items[t.keyOf(it)] = it
			return ok(map[string]any{"Items": []item{}})
		})
	}
	return l.store.serve(op, body)
}

// enable mints Local's new stream for table: a wall-clock label distinct
// from every earlier one, in ddblocal and 000000000000.
func (l *localStreams) enable(table, viewType string) {
	l.labels++
	label := fmt.Sprintf("2026-10-25T21:13:%02d.%03d", l.labels%60, l.labels)
	s := &localStream{
		arn:   "arn:aws:dynamodb:ddblocal:000000000000:table/" + table + "/stream/" + label,
		label: label, table: table, viewType: viewType, enabled: true,
		shard: fmt.Sprintf("shardId-00000001792962817%03d-d2065312", l.labels),
	}
	l.current[table], l.byARN[s.arn] = s, s
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	d := l.store.tables[table].desc
	d["StreamSpecification"] = map[string]any{"StreamEnabled": true, "StreamViewType": viewType}
	d["LatestStreamArn"], d["LatestStreamLabel"] = s.arn, s.label
}

func (l *localStreams) desc(table string) map[string]any {
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	return maps.Clone(l.store.tables[table].desc)
}

// write runs apply and appends a record for each item of tables it
// changed, in key order, to the table's enabled stream.
func (l *localStreams) write(tables []string, apply func() reply) reply {
	before := map[string]map[string]item{}
	for _, t := range tables {
		before[t] = l.store.items(t)
	}
	rep := apply()
	for _, t := range tables {
		s := l.current[t]
		if s == nil || !s.enabled {
			continue
		}
		after := l.store.items(t)
		keys := slices.Sorted(maps.Keys(maps.Collect(func(yield func(string, bool) bool) {
			for k := range before[t] {
				yield(k, true)
			}
			for k := range after {
				yield(k, true)
			}
		})))
		for _, k := range keys {
			old, had := before[t][k]
			now, has := after[k]
			switch {
			case !had && has:
				l.append(s, "INSERT", k, nil, now)
			case had && !has:
				l.append(s, "REMOVE", k, old, nil)
			case had && has && !sameItem(old, now):
				l.append(s, "MODIFY", k, old, now)
			}
		}
	}
	return rep
}

func sameItem(a, b item) bool {
	return maps.EqualFunc(a, b, func(x, y json.RawMessage) bool { return string(x) == string(y) })
}

// append adds a record with the images the view type keeps
// (dynamodb-streams.md 7.2) and a SizeBytes no rule computes, so a test
// can tell it was copied from Local.
func (l *localStreams) append(s *localStream, event, key string, old, now item) {
	var keys item
	if err := json.Unmarshal([]byte(key), &keys); err != nil {
		panic(err)
	}
	l.counter++
	r := localRecord{
		Seq: fmt.Sprintf("%021d", l.counter), EventName: event, Keys: keys, SizeBytes: 900 + l.counter,
		EventID: fmt.Sprintf("1b3c5d7e-0000-4000-8000-%012d", l.counter),
	}
	if s.viewType == "NEW_IMAGE" || s.viewType == "NEW_AND_OLD_IMAGES" {
		r.NewImage = now
	}
	if s.viewType == "OLD_IMAGE" || s.viewType == "NEW_AND_OLD_IMAGES" {
		r.OldImage = old
	}
	s.records = append(s.records, r)
}

func (r localRecord) wire(viewType string) map[string]any {
	d := map[string]any{"ApproximateCreationDateTime": json.Number("1792962840.000"), "Keys": r.Keys, "SequenceNumber": r.Seq, "SizeBytes": r.SizeBytes, "StreamViewType": viewType}
	if r.NewImage != nil {
		d["NewImage"] = r.NewImage
	}
	if r.OldImage != nil {
		d["OldImage"] = r.OldImage
	}
	return map[string]any{"awsRegion": "ddblocal", "dynamodb": d, "eventID": r.EventID, "eventName": r.EventName, "eventSource": "aws:dynamodb", "eventVersion": "1.1"}
}

type localIterator struct {
	ARN   string
	Index int
}

func (l *localStreams) serveStreams(op, body string) reply {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failStreams {
		return reply{Status: http.StatusInternalServerError, Body: `{"__type":"com.amazonaws.dynamodb.v20120810#InternalFailure","Message":"The request processing has failed because of an unknown error, exception or failure."}`}
	}
	var in struct {
		StreamArn, ShardIteratorType, ShardIterator string
		ShardID                                     string `json:"ShardId"`
		SequenceNumber                              *string
		Limit                                       *int
	}
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		return localErr("com.amazon.coral.service#SerializationException", err.Error())
	}
	switch op {
	case "DescribeStream":
		s := l.byARN[in.StreamArn]
		if s == nil {
			return localErr("com.amazonaws.dynamodb.v20120810#ResourceNotFoundException", "Requested resource not found: Stream: "+in.StreamArn+" not found")
		}
		status := "ENABLED"
		if !s.enabled {
			status = "DISABLED"
		}
		return ok(map[string]any{"StreamDescription": map[string]any{
			"StreamArn": s.arn, "StreamLabel": s.label, "StreamStatus": status, "StreamViewType": s.viewType, "TableName": s.table,
			"Shards": []map[string]any{{"ShardId": s.shard, "SequenceNumberRange": map[string]any{"StartingSequenceNumber": "000000000000000000001"}}},
		}})
	case "GetShardIterator":
		s := l.byARN[in.StreamArn]
		if s == nil {
			return localErr("com.amazonaws.dynamodb.v20120810#ResourceNotFoundException", "Stream does not exist")
		}
		if in.ShardID != s.shard {
			return localErr("com.amazonaws.dynamodb.v20120810#ResourceNotFoundException", "Shard does not exist")
		}
		idx := 0
		switch in.ShardIteratorType {
		case "LATEST":
			idx = len(s.records)
		case "AT_SEQUENCE_NUMBER", "AFTER_SEQUENCE_NUMBER":
			if in.SequenceNumber == nil {
				return localErr("com.amazon.coral.validate#ValidationException", "One or more parameter values were invalid: Invalid SequenceNumber")
			}
			idx = len(s.records)
			for i, r := range s.records {
				if r.Seq >= *in.SequenceNumber {
					idx = i
					if r.Seq == *in.SequenceNumber && in.ShardIteratorType == "AFTER_SEQUENCE_NUMBER" {
						idx++
					}
					break
				}
			}
		}
		return ok(map[string]any{"ShardIterator": l.iterator(s, idx)})
	case "GetRecords":
		arnPart, rest, found := strings.Cut(in.ShardIterator, "|001|")
		raw, err := base64.StdEncoding.DecodeString(rest)
		var it localIterator
		if !found || err != nil || json.Unmarshal(raw, &it) != nil || it.ARN != arnPart || l.byARN[it.ARN] == nil {
			return localErr("com.amazon.coral.validate#ValidationException", "Invalid ShardIterator")
		}
		s := l.byARN[it.ARN]
		limit := 1000
		if in.Limit != nil {
			limit = *in.Limit
		}
		end := min(it.Index+limit, len(s.records))
		out := []map[string]any{}
		for _, r := range s.records[it.Index:end] {
			out = append(out, r.wire(s.viewType))
		}
		answer := map[string]any{"Records": out}
		if s.enabled || end < len(s.records) {
			answer["NextShardIterator"] = l.iterator(s, end)
		}
		return ok(answer)
	}
	return localErr("com.amazonaws.dynamodb.v20120810#UnknownOperationException", "An unknown operation was requested.")
}

func (l *localStreams) iterator(s *localStream, idx int) string {
	b, err := json.Marshal(localIterator{ARN: s.arn, Index: idx})
	if err != nil {
		panic(err)
	}
	return s.arn + "|001|" + base64.StdEncoding.EncodeToString(b)
}

// records returns a copy of the records of table's latest Local stream.
func (l *localStreams) records(table string) []localRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.current[table]; s != nil {
		return slices.Clone(s.records)
	}
	return nil
}

func (l *localStreams) setFail(fail bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failStreams = fail
}

// streamsHarness is a kernel with DynamoDB and DynamoDB Streams over a
// fake Local with streams, and the edge in front of both.
type streamsHarness struct {
	*harness
	ls      *localStreams
	streams *dynamodb.Streams
}

func runStreams(t *testing.T, opts dynamodb.Options, fn func(h *streamsHarness)) {
	t.Helper()
	ls := newLocalStreams()
	local := &fakeLocal{answer: ls.serve}
	kerneltest.Run(t, func(k *kerneltest.K) {
		opts.Local = local
		svc := dynamodb.New(k.Deps(dynamodbapi.Service()), opts)
		streams := dynamodb.NewStreams(k.Deps(dynamodbstreamsapi.Service()), svc)
		front, err := edge.New(edge.Options{
			Kernel: k.Kernel, Registry: k.Kernel.Registry(), Identity: k.Identity, Regions: aws.KnownRegions(),
			Codecs:   map[ref.ProtocolID]edge.Codec{ref.AWSJSON10: jsonCodec{awsjson.V10}, ref.AWSJSON11: jsonCodec{awsjson.V11}},
			Listener: ref.ListenerMain, Unclaimed: ref.AWSJSON10,
			UnknownOperationError: func(id ref.ServiceID, f awserr.Family) (awserr.Identity, bool) {
				if id == dynamodbstreamsapi.Service().ID {
					return dynamodbstreamsapi.UnknownOperationError(f)
				}
				return dynamodbapi.UnknownOperationError(f)
			},
		})
		if err != nil {
			k.Fatal(err)
		}
		fn(&streamsHarness{harness: &harness{K: k, local: local, svc: svc, front: front}, ls: ls, streams: streams})
	}, kerneltest.Services(dynamodbapi.Service(), dynamodbstreamsapi.Service()))
}

// streamsCall sends a DynamoDB Streams operation signed for dynamodb, as
// every SDK signs it (dynamodb-streams.md 3.2), from from.
func (h *streamsHarness) streamsCall(op, body string, from *caller) response {
	h.Helper()
	return h.post(http.Header{"Content-Type": {"application/x-amz-json-1.0"}, "X-Amz-Target": {streamsTarget + op}}, body, from)
}

// ok200 decodes a 200 answer into v, failing the test on any other.
func (h *streamsHarness) ok200(r response, v any) {
	h.Helper()
	if r.Status != http.StatusOK {
		h.Fatalf("answer %d %s, want 200", r.Status, r.Body)
	}
	if err := json.Unmarshal([]byte(r.Body), v); err != nil {
		h.Fatalf("answer %s: %v", r.Body, err)
	}
}

// awsError is an error answer's body, with the message member in either
// casing.
type awsError struct {
	Type     string `json:"__type"`
	Message  string `json:"message"`
	MessageU string `json:"Message"`
}

func (e awsError) text() string { return e.Message + e.MessageU }

func (h *streamsHarness) errorOf(r response, status int) awsError {
	h.Helper()
	if r.Status != status {
		h.Fatalf("answer %d %s, want %d", r.Status, r.Body, status)
	}
	var e awsError
	if err := json.Unmarshal([]byte(r.Body), &e); err != nil {
		h.Fatalf("error body %s: %v", r.Body, err)
	}
	return e
}
