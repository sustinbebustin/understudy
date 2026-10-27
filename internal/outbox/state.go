package outbox

import (
	"fmt"
	"maps"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshot sections. 2: an
// event bus entry keeps a forwarded event's envelope (forwardedRecord). 3:
// a Lambda delivery keeps its sender (senderRecord) and a queue target its
// trace header. 4: a request keeps the aws:SourceAccount its delivery sends
// (sourceAccount), which an earlier entry left implied, so an earlier
// section is refused rather than restored without it (ADR 0009).
const stateVersion = 4

// State is the outbox of one account and region, the scope of the targets
// its deliveries land in: every accepted delivery not yet delivered,
// dead-lettered or dropped, by id. Entries are values, replaced on change.
// Its persisted form is stateRecord.
type State struct {
	Entries map[ids.DeliveryID]Entry
}

func newState(aws.Scope) State { return State{Entries: map[ids.DeliveryID]Entry{}} }

// IsZero reports whether the scope has nothing pending; snapshots skip it
// then.
func (s *State) IsZero() bool { return len(s.Entries) == 0 }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// lastSeq returns the greatest Seq of the pending entries, 0 for none.
func (s *State) lastSeq() uint64 {
	var last uint64
	for id := range s.Entries {
		last = max(last, s.Entries[id].Seq)
	}
	return last
}

// Entry is one pending delivery (ARCHITECTURE.md 16).
type Entry struct {
	ID      ids.DeliveryID
	Request delivery.Request
	// Attempt counts the attempts made so far; the next one is Attempt+1.
	Attempt int
	// NextAt is when the next attempt is due.
	NextAt clock.Instant
	// Cause is the source call's event and trace: the parent of every
	// journal event the delivery produces.
	Cause ref.Cause
	// Accepted is the commit instant of the source call; retry policies
	// with a maximum age count from it.
	Accepted clock.Instant
	// Seq orders entries accepted at one instant, such as the fan-out of
	// one publish, in the order the outbox took them: it is drawn from a
	// counter of the outbox, under the cell lock, when the entry is
	// inserted.
	Seq uint64
	// Jitter seeds an EventBridge target's retry jitter and is 0 under
	// every other policy. It is drawn once, at acceptance, so the delays do
	// not depend on how concurrent attempts interleave.
	Jitter uint64
	// Running is set while an attempt of the entry is in flight, so its
	// timer firing again (Rearm arms every armable entry, a running one
	// included) starts no second attempt. It is this process's claim and is
	// never saved: a loaded entry is attempted afresh.
	Running bool
}

// The persisted forms below are the snapshot schema. The delivery types
// carry no json tags, and EventBridgeTarget's MaxAge is a time.Duration,
// which has no JSON form, so the outbox persists its own records and
// converts at the boundary. ARNs are written as their string form.

// stateRecord is the persisted form of State.
type stateRecord struct {
	Entries map[ids.DeliveryID]entryRecord `json:"entries"`
}

// entryRecord is the persisted form of an Entry; its id is the map key.
type entryRecord struct {
	Request  requestRecord `json:"request"`
	Attempt  int           `json:"attempt"`
	NextAt   clock.Instant `json:"nextAt"`
	Cause    causeRecord   `json:"cause"`
	Accepted clock.Instant `json:"accepted"`
	Seq      uint64        `json:"seq"`
	Jitter   uint64        `json:"jitter,omitzero"`
}

// causeRecord names its event ids ...EventId, so snapshot diff
// --normalize masks them (ARCHITECTURE.md 13).
type causeRecord struct {
	Parent ref.EventID `json:"parentEventId"`
	Trace  ref.EventID `json:"traceEventId"`
}

type requestRecord struct {
	Source        string             `json:"source"`
	SourceAccount aws.AccountID      `json:"sourceAccount,omitzero"`
	Mechanism     delivery.Mechanism `json:"mechanism"`
	Target        targetRecord       `json:"target"`
	Policy        policyRecord       `json:"policy"`
	DLQ           string             `json:"dlq,omitempty"`
	Sender        *senderRecord      `json:"sender,omitempty"`
}

// senderRecord is the persisted form of a delivery.LambdaSender.
type senderRecord struct {
	Role    string                 `json:"role"`
	Carries delivery.LambdaPayload `json:"carries"`
}

// targetRecord is the persisted form of a delivery.Target.
//
//sumtype:decl
type targetRecord interface{ isTargetRecord() }

type sqsQueueRecord struct {
	Queue       string            `json:"queue"`
	Body        string            `json:"body"`
	Attributes  []attributeRecord `json:"attributes,omitempty"`
	GroupID     string            `json:"groupId,omitempty"`
	DedupID     string            `json:"dedupId,omitempty"`
	TraceHeader string            `json:"traceHeader,omitempty"`
}

type lambdaFunctionRecord struct {
	Function   string                  `json:"function"`
	Qualifier  string                  `json:"qualifier,omitempty"`
	Event      []byte                  `json:"event"`
	Invocation delivery.InvocationType `json:"invocation"`
}

type snsTopicRecord struct {
	Topic      string            `json:"topic"`
	Message    string            `json:"message"`
	Subject    string            `json:"subject,omitempty"`
	Attributes []attributeRecord `json:"attributes,omitempty"`
}

type eventBusRecord struct {
	Bus     string             `json:"bus"`
	Entries []eventEntryRecord `json:"entries"`
}

type eventEntryRecord struct {
	Source     string           `json:"source"`
	DetailType string           `json:"detailType"`
	Detail     string           `json:"detail"`
	Resources  []string         `json:"resources,omitempty"`
	Forwarded  *forwardedRecord `json:"forwarded,omitempty"`
}

// forwardedRecord is the persisted form of a delivery.Forwarded.
type forwardedRecord struct {
	ID      string    `json:"id"`
	Account string    `json:"account"`
	Region  string    `json:"region"`
	Time    time.Time `json:"time"`
}

// eventEntryRecordOf returns the persisted form of e.
func eventEntryRecordOf(e delivery.EventEntry) eventEntryRecord {
	r := eventEntryRecord{Source: e.Source, DetailType: e.DetailType, Detail: e.Detail, Resources: e.Resources}
	if f := e.Forwarded; f != nil {
		r.Forwarded = &forwardedRecord{ID: f.ID, Account: f.Account, Region: f.Region, Time: f.Time}
	}
	return r
}

// eventEntryOf returns the entry r persists.
func eventEntryOf(r eventEntryRecord) delivery.EventEntry {
	e := delivery.EventEntry{Source: r.Source, DetailType: r.DetailType, Detail: r.Detail, Resources: r.Resources}
	if f := r.Forwarded; f != nil {
		e.Forwarded = &delivery.Forwarded{ID: f.ID, Account: f.Account, Region: f.Region, Time: f.Time}
	}
	return e
}

type s3ObjectRecord struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	Body   []byte `json:"body"`
}

// httpEndpointRecord's HTTPKind is required: a record without it decodes
// to the zero kind, which Enqueue's checks refuse. It cannot be named
// "kind", the member that names the record's variant.
type httpEndpointRecord struct {
	URL                  string             `json:"url"`
	HTTPKind             delivery.HTTPKind  `json:"httpKind"`
	Headers              []httpHeaderRecord `json:"headers,omitempty"`
	Body                 []byte             `json:"body"`
	DeadLetterAttributes []attributeRecord  `json:"deadLetterAttributes,omitempty"`
}

type httpHeaderRecord struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (sqsQueueRecord) isTargetRecord()       {}
func (lambdaFunctionRecord) isTargetRecord() {}
func (snsTopicRecord) isTargetRecord()       {}
func (eventBusRecord) isTargetRecord()       {}
func (s3ObjectRecord) isTargetRecord()       {}
func (httpEndpointRecord) isTargetRecord()   {}

type attributeRecord struct {
	Name  string               `json:"name"`
	Value attributeValueRecord `json:"value"`
}

// attributeValueRecord is the persisted form of a delivery.AttributeValue.
//
//sumtype:decl
type attributeValueRecord interface{ isAttributeValueRecord() }

type stringValueRecord struct {
	Label string `json:"label,omitempty"`
	Value string `json:"value"`
}

type numberValueRecord struct {
	Label string `json:"label,omitempty"`
	Value string `json:"value"`
}

type binaryValueRecord struct {
	Label string `json:"label,omitempty"`
	Value []byte `json:"value"`
}

func (stringValueRecord) isAttributeValueRecord() {}
func (numberValueRecord) isAttributeValueRecord() {}
func (binaryValueRecord) isAttributeValueRecord() {}

// policyRecord is the persisted form of a delivery.RetryPolicy.
//
//sumtype:decl
type policyRecord interface{ isPolicyRecord() }

type snsManagedRecord struct{}

type eventBridgeRecord struct {
	MaxAgeMillis int64 `json:"maxAgeMillis"`
	MaxAttempts  int   `json:"maxAttempts"`
}

type s3NotificationRecord struct{}

type noneRecord struct{}

// snsHTTPRecord is an SNSHTTP policy, its delay targets in whole
// milliseconds. Every member is required, so each is a pointer that a
// decoded record must set; policyOf refuses a record that lacks one.
type snsHTTPRecord struct {
	NoDelay              *int                      `json:"noDelay"`
	MinDelay             *int                      `json:"minDelay"`
	Backoff              *int                      `json:"backoff"`
	MaxDelay             *int                      `json:"maxDelay"`
	MinDelayTargetMillis *int64                    `json:"minDelayTargetMillis"`
	MaxDelayTargetMillis *int64                    `json:"maxDelayTargetMillis"`
	BackoffFunction      *delivery.BackoffFunction `json:"backoffFunction"`
	MaxReceivesPerSecond *int                      `json:"maxReceivesPerSecond"`
}

// missing names the first member r lacks, "" when it has every one.
func (r snsHTTPRecord) missing() string {
	members := []struct {
		name string
		set  bool
	}{
		{"noDelay", r.NoDelay != nil},
		{"minDelay", r.MinDelay != nil},
		{"backoff", r.Backoff != nil},
		{"maxDelay", r.MaxDelay != nil},
		{"minDelayTargetMillis", r.MinDelayTargetMillis != nil},
		{"maxDelayTargetMillis", r.MaxDelayTargetMillis != nil},
		{"backoffFunction", r.BackoffFunction != nil},
		{"maxReceivesPerSecond", r.MaxReceivesPerSecond != nil},
	}
	for _, m := range members {
		if !m.set {
			return m.name
		}
	}
	return ""
}

func (snsManagedRecord) isPolicyRecord()     {}
func (eventBridgeRecord) isPolicyRecord()    {}
func (s3NotificationRecord) isPolicyRecord() {}
func (noneRecord) isPolicyRecord()           {}
func (snsHTTPRecord) isPolicyRecord()        {}

// recordOf returns the persisted form of e. A nil target or policy, which
// Enqueue never accepts, is written as null and refused on load.
func recordOf(e *Entry) entryRecord {
	req := e.Request
	r := entryRecord{
		Request: requestRecord{
			Source:        req.Source.String(),
			SourceAccount: req.SourceAccount,
			Mechanism:     req.Mechanism,
			Target:        targetRecordOf(req.Target),
			Policy:        policyRecordOf(req.Policy),
		},
		Attempt:  e.Attempt,
		NextAt:   e.NextAt,
		Cause:    causeRecord{Parent: e.Cause.Parent, Trace: e.Cause.Trace},
		Accepted: e.Accepted,
		Seq:      e.Seq,
		Jitter:   e.Jitter,
	}
	if req.DLQ != nil {
		r.Request.DLQ = req.DLQ.String()
	}
	if s := req.Sender; s != nil {
		r.Request.Sender = &senderRecord{Role: s.Role.String(), Carries: s.Carries}
	}
	return r
}

// entryOf returns the Entry r persists under id, or the *EntryError that
// says why the outbox could not attempt it.
func entryOf(id ids.DeliveryID, r *entryRecord) (Entry, error) {
	req, rerr := requestOf(&r.Request)
	if rerr == nil {
		_, rerr = checkRequest(req)
	}
	if rerr != nil {
		return Entry{}, &EntryError{ID: id, Err: rerr}
	}
	return Entry{
		ID:       id,
		Request:  req,
		Attempt:  r.Attempt,
		NextAt:   r.NextAt,
		Cause:    ref.Cause{Parent: r.Cause.Parent, Trace: r.Cause.Trace},
		Accepted: r.Accepted,
		Seq:      r.Seq,
		Jitter:   r.Jitter,
	}, nil
}

// EntryError is a snapshot entry that holds no delivery the outbox can
// attempt: an ARN that does not parse, a missing target or policy, or any
// request Enqueue would refuse. Import then keeps the outbox as it was.
type EntryError struct {
	ID  ids.DeliveryID
	Err *RequestError
}

func (e *EntryError) Error() string {
	return fmt.Sprintf("outbox: snapshot entry %s holds a delivery the outbox cannot attempt: %s; the snapshot was edited or written by another build, and the outbox kept its state",
		e.ID, e.Err.problem())
}

func (e *EntryError) Unwrap() error { return e.Err }

// requestOf returns the request r persists. It parses r's ARNs; whether
// the request can be attempted is checkRequest's to decide.
func requestOf(r *requestRecord) (delivery.Request, *RequestError) {
	source, err := parseARN("Source", r.Source)
	if err != nil {
		return delivery.Request{}, err
	}
	target, err := targetOf(r.Target)
	if err != nil {
		return delivery.Request{}, err
	}
	policy, ok := policyOf(r.Policy)
	if !ok {
		reason := "is missing"
		if h, isHTTP := r.Policy.(snsHTTPRecord); isHTTP {
			reason = "is an sns-http policy without its required member " + h.missing()
		}
		return delivery.Request{}, &RequestError{Field: "Policy", Reason: reason}
	}
	req := delivery.Request{Source: source, SourceAccount: r.SourceAccount, Mechanism: r.Mechanism, Target: target, Policy: policy}
	if r.DLQ != "" {
		dlq, err := parseARN("DLQ", r.DLQ)
		if err != nil {
			return delivery.Request{}, err
		}
		req.DLQ = &dlq
	}
	if s := r.Sender; s != nil {
		role, err := parseARN("Sender.Role", s.Role)
		if err != nil {
			return delivery.Request{}, err
		}
		req.Sender = &delivery.LambdaSender{Role: role, Carries: s.Carries}
	}
	return req, nil
}

func parseARN(field, s string) (aws.ARN, *RequestError) {
	a, err := aws.ParseARN(s)
	if err != nil {
		return aws.ARN{}, &RequestError{Field: field, Reason: "is not an ARN", Err: err}
	}
	return a, nil
}

func targetRecordOf(t delivery.Target) targetRecord {
	switch t := t.(type) {
	case delivery.SQSQueue:
		return sqsQueueRecord{
			Queue: t.ARN.String(), Body: t.Body, Attributes: attributeRecordsOf(t.Attributes), GroupID: t.GroupID, DedupID: t.DedupID, TraceHeader: t.TraceHeader,
		}
	case delivery.LambdaFunction:
		return lambdaFunctionRecord{Function: t.ARN.String(), Qualifier: t.Qualifier, Event: t.Event, Invocation: t.Invocation}
	case delivery.SNSTopic:
		return snsTopicRecord{Topic: t.ARN.String(), Message: t.Message, Subject: t.Subject, Attributes: attributeRecordsOf(t.Attributes)}
	case delivery.EventBus:
		entries := make([]eventEntryRecord, len(t.Entries))
		for i, e := range t.Entries {
			entries[i] = eventEntryRecordOf(e)
		}
		return eventBusRecord{Bus: t.ARN.String(), Entries: entries}
	case delivery.S3Object:
		return s3ObjectRecord{Bucket: t.Bucket, Key: t.Key, Body: t.Body}
	case delivery.HTTPEndpoint:
		var headers []httpHeaderRecord
		for _, h := range t.Headers {
			headers = append(headers, httpHeaderRecord(h))
		}
		return httpEndpointRecord{
			URL: t.URL.String(), HTTPKind: t.Kind, Headers: headers, Body: t.Body,
			DeadLetterAttributes: attributeRecordsOf(t.DeadLetterAttributes),
		}
	}
	return nil
}

func targetOf(r targetRecord) (delivery.Target, *RequestError) {
	switch r := r.(type) {
	case sqsQueueRecord:
		queue, err := parseARN("Target.ARN", r.Queue)
		if err != nil {
			return nil, err
		}
		attrs, err := attributesOf(r.Attributes)
		if err != nil {
			return nil, err
		}
		return delivery.SQSQueue{ARN: queue, Body: r.Body, Attributes: attrs, GroupID: r.GroupID, DedupID: r.DedupID, TraceHeader: r.TraceHeader}, nil
	case lambdaFunctionRecord:
		function, err := parseARN("Target.ARN", r.Function)
		if err != nil {
			return nil, err
		}
		return delivery.LambdaFunction{ARN: function, Qualifier: r.Qualifier, Event: r.Event, Invocation: r.Invocation}, nil
	case snsTopicRecord:
		topic, err := parseARN("Target.ARN", r.Topic)
		if err != nil {
			return nil, err
		}
		attrs, err := attributesOf(r.Attributes)
		if err != nil {
			return nil, err
		}
		return delivery.SNSTopic{ARN: topic, Message: r.Message, Subject: r.Subject, Attributes: attrs}, nil
	case eventBusRecord:
		bus, err := parseARN("Target.ARN", r.Bus)
		if err != nil {
			return nil, err
		}
		entries := make([]delivery.EventEntry, len(r.Entries))
		for i, e := range r.Entries {
			entries[i] = eventEntryOf(e)
		}
		return delivery.EventBus{ARN: bus, Entries: entries}, nil
	case s3ObjectRecord:
		return delivery.S3Object{Bucket: r.Bucket, Key: r.Key, Body: r.Body}, nil
	case httpEndpointRecord:
		return httpEndpointOf(r)
	}
	return nil, &RequestError{Field: "Target", Reason: "is missing"}
}

func httpEndpointOf(r httpEndpointRecord) (delivery.Target, *RequestError) {
	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, &RequestError{Field: "Target.URL", Reason: "is not a URL", Err: err}
	}
	var headers []delivery.HTTPHeader
	for _, h := range r.Headers {
		headers = append(headers, delivery.HTTPHeader(h))
	}
	attrs, rerr := attributesOf(r.DeadLetterAttributes)
	if rerr != nil {
		return nil, rerr
	}
	return delivery.HTTPEndpoint{URL: *u, Kind: r.HTTPKind, Headers: headers, Body: r.Body, DeadLetterAttributes: attrs}, nil
}

// attributeRecordsOf returns the persisted form of attrs; nil for none.
func attributeRecordsOf(attrs []delivery.MessageAttribute) []attributeRecord {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attributeRecord, len(attrs))
	for i, a := range attrs {
		out[i].Name = a.Name
		switch v := a.Value.(type) {
		case delivery.StringAttribute:
			out[i].Value = stringValueRecord{Label: v.Label, Value: v.Value}
		case delivery.NumberAttribute:
			out[i].Value = numberValueRecord{Label: v.Label, Value: v.Value}
		case delivery.BinaryAttribute:
			out[i].Value = binaryValueRecord{Label: v.Label, Value: v.Value}
		}
	}
	return out
}

// attributesOf returns the attributes rs persist; nil for none.
func attributesOf(rs []attributeRecord) ([]delivery.MessageAttribute, *RequestError) {
	if len(rs) == 0 {
		return nil, nil
	}
	out := make([]delivery.MessageAttribute, len(rs))
	for i, r := range rs {
		out[i].Name = r.Name
		switch v := r.Value.(type) {
		case stringValueRecord:
			out[i].Value = delivery.StringAttribute{Label: v.Label, Value: v.Value}
		case numberValueRecord:
			out[i].Value = delivery.NumberAttribute{Label: v.Label, Value: v.Value}
		case binaryValueRecord:
			out[i].Value = delivery.BinaryAttribute{Label: v.Label, Value: v.Value}
		default:
			return nil, &RequestError{Field: "Target.Attributes", Reason: fmt.Sprintf("has no value for attribute %q", r.Name)}
		}
	}
	return out, nil
}

// policyRecordOf returns the persisted form of p; nil for a nil policy,
// which Enqueue never accepts.
func policyRecordOf(p delivery.RetryPolicy) policyRecord {
	switch p := p.(type) {
	case delivery.SNSManaged:
		return snsManagedRecord{}
	case delivery.EventBridgeTarget:
		return eventBridgeRecord{MaxAgeMillis: p.MaxAge.Milliseconds(), MaxAttempts: p.MaxAttempts}
	case delivery.S3Notification:
		return s3NotificationRecord{}
	case delivery.None:
		return noneRecord{}
	case delivery.SNSHTTP:
		minMillis, maxMillis := p.MinDelayTarget.Milliseconds(), p.MaxDelayTarget.Milliseconds()
		return snsHTTPRecord{
			NoDelay: &p.NoDelay, MinDelay: &p.MinDelay, Backoff: &p.Backoff, MaxDelay: &p.MaxDelay,
			MinDelayTargetMillis: &minMillis, MaxDelayTargetMillis: &maxMillis,
			BackoffFunction: &p.BackoffFunction, MaxReceivesPerSecond: &p.MaxReceivesPerSecond,
		}
	}
	return nil
}

// policyOf returns the retry policy r persists; false for a nil record and
// for an sns-http record that lacks a member.
func policyOf(r policyRecord) (delivery.RetryPolicy, bool) {
	switch r := r.(type) {
	case snsManagedRecord:
		return delivery.SNSManaged{}, true
	case eventBridgeRecord:
		return delivery.EventBridgeTarget{MaxAge: millis(r.MaxAgeMillis), MaxAttempts: r.MaxAttempts}, true
	case s3NotificationRecord:
		return delivery.S3Notification{}, true
	case noneRecord:
		return delivery.None{}, true
	case snsHTTPRecord:
		if r.missing() != "" {
			return nil, false
		}
		return delivery.SNSHTTP{
			NoDelay: *r.NoDelay, MinDelay: *r.MinDelay, Backoff: *r.Backoff, MaxDelay: *r.MaxDelay,
			MinDelayTarget: millis(*r.MinDelayTargetMillis), MaxDelayTarget: millis(*r.MaxDelayTargetMillis),
			BackoffFunction: *r.BackoffFunction, MaxReceivesPerSecond: *r.MaxReceivesPerSecond,
		}, true
	}
	return nil, false
}

// millis is ms milliseconds, saturated at the bounds of time.Duration, so
// an edited record's out-of-range value stays out of range for the checks
// rather than wrapping into it.
func millis(ms int64) time.Duration {
	const most = int64(math.MaxInt64 / time.Millisecond)
	switch {
	case ms > most:
		return math.MaxInt64
	case ms < -most:
		return math.MinInt64
	}
	return time.Duration(ms) * time.Millisecond
}

// CodecError is a value the State codec was handed that is not a *State.
type CodecError struct {
	Type string
}

func (e *CodecError) Error() string {
	return "outbox: the state codec encodes only *outbox.State, not " + e.Type + "; the outbox's Partitioned must be built with its own codec"
}

// codec returns the snapshot codec of State: the jsonx form of stateRecord
// with the tagged-union tables of its sums. Decoding refuses, with an
// *EntryError, an entry Enqueue would have refused. The tables are static,
// so an error building them is a bug in them; the codec then fails every
// section with it.
func codec() store.Codec {
	c, err := unions()
	if err != nil {
		return store.Codec{
			Marshal:   func(any) ([]byte, error) { return nil, err },
			Unmarshal: func([]byte, any) error { return err },
		}
	}
	return store.Codec{
		Marshal: func(v any) ([]byte, error) {
			st, ok := v.(*State)
			if !ok {
				return nil, &CodecError{Type: fmt.Sprintf("%T", v)}
			}
			out := stateRecord{Entries: make(map[ids.DeliveryID]entryRecord, len(st.Entries))}
			for id := range st.Entries {
				e := st.Entries[id]
				out.Entries[id] = recordOf(&e)
			}
			return c.Marshal(&out)
		},
		Unmarshal: func(data []byte, v any) error {
			st, ok := v.(*State)
			if !ok {
				return &CodecError{Type: fmt.Sprintf("%T", v)}
			}
			var in stateRecord
			if err := c.Unmarshal(data, &in); err != nil {
				return err
			}
			// Sorted, so a snapshot with several bad entries always names
			// the same one.
			for _, id := range slices.Sorted(maps.Keys(in.Entries)) {
				r := in.Entries[id]
				e, err := entryOf(id, &r)
				if err != nil {
					return err
				}
				st.Entries[id] = e
			}
			return nil
		},
	}
}

func unions() (jsonx.Codec, error) {
	targets, err := jsonx.NewUnion("outbox.targetRecord",
		jsonx.Variant[targetRecord, sqsQueueRecord]("sqs-queue"),
		jsonx.Variant[targetRecord, lambdaFunctionRecord]("lambda-function"),
		jsonx.Variant[targetRecord, snsTopicRecord]("sns-topic"),
		jsonx.Variant[targetRecord, eventBusRecord]("event-bus"),
		jsonx.Variant[targetRecord, s3ObjectRecord]("s3-object"),
		jsonx.Variant[targetRecord, httpEndpointRecord]("http-endpoint"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	policies, err := jsonx.NewUnion("outbox.policyRecord",
		jsonx.Variant[policyRecord, snsManagedRecord]("sns-managed"),
		jsonx.Variant[policyRecord, eventBridgeRecord]("eventbridge-target"),
		jsonx.Variant[policyRecord, s3NotificationRecord]("s3-notification"),
		jsonx.Variant[policyRecord, noneRecord]("none"),
		jsonx.Variant[policyRecord, snsHTTPRecord]("sns-http"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	values, err := jsonx.NewUnion("outbox.attributeValueRecord",
		jsonx.Variant[attributeValueRecord, stringValueRecord]("string"),
		jsonx.Variant[attributeValueRecord, numberValueRecord]("number"),
		jsonx.Variant[attributeValueRecord, binaryValueRecord]("binary"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	return jsonx.NewCodec(targets, policies, values), nil
}

// timerID is the attempt timer's ID of delivery id in scope:
// "<account>/<region>/<delivery-id>", so the timer names its cell.
func timerID(scope aws.Scope, id ids.DeliveryID) string {
	return store.ScopeKeys().Encode(scope) + "/" + string(id)
}

// parseTimerID reverses timerID.
func parseTimerID(s string) (aws.Scope, ids.DeliveryID, bool) {
	before, after, ok := strings.CutLast(s, "/")
	if !ok {
		return aws.Scope{}, "", false
	}
	scope, err := store.ScopeKeys().Decode(before)
	if err != nil || after == "" {
		return aws.Scope{}, "", false
	}
	return scope, ids.DeliveryID(after), true
}
