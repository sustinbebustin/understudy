package outbox

import (
	"fmt"
	"maps"
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

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 1

// State is the outbox of one account and region, the scope of the targets
// its deliveries land in: every accepted delivery not yet delivered,
// dead-lettered or dropped, by id. Entries are values, replaced on change.
// Its persisted form is stateRecord.
type State struct {
	Entries map[ids.DeliveryID]Entry
}

func newState(aws.Scope) State { return State{Entries: map[ids.DeliveryID]Entry{}} }

// IsZero reports a scope with nothing pending, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Entries) == 0 }

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
	Source    string             `json:"source"`
	Mechanism delivery.Mechanism `json:"mechanism"`
	Target    targetRecord       `json:"target"`
	Policy    policyRecord       `json:"policy"`
	DLQ       string             `json:"dlq,omitempty"`
}

// targetRecord is the persisted form of a delivery.Target.
//
//sumtype:decl
type targetRecord interface{ isTargetRecord() }

type sqsQueueRecord struct {
	Queue      string            `json:"queue"`
	Body       string            `json:"body"`
	Attributes []attributeRecord `json:"attributes,omitempty"`
	GroupID    string            `json:"groupId,omitempty"`
	DedupID    string            `json:"dedupId,omitempty"`
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

func (sqsQueueRecord) isTargetRecord()       {}
func (lambdaFunctionRecord) isTargetRecord() {}
func (snsTopicRecord) isTargetRecord()       {}

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

func (snsManagedRecord) isPolicyRecord()     {}
func (eventBridgeRecord) isPolicyRecord()    {}
func (s3NotificationRecord) isPolicyRecord() {}
func (noneRecord) isPolicyRecord()           {}

// recordOf returns the persisted form of e. A nil target or policy, which
// Enqueue never accepts, is written as null and refused on load.
func recordOf(e *Entry) entryRecord {
	req := e.Request
	r := entryRecord{
		Request: requestRecord{
			Source:    req.Source.String(),
			Mechanism: req.Mechanism,
			Target:    targetRecordOf(req.Target),
			Policy:    policyRecordOf(req.Policy),
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
		return delivery.Request{}, &RequestError{Field: "Policy", Reason: "is missing"}
	}
	req := delivery.Request{Source: source, Mechanism: r.Mechanism, Target: target, Policy: policy}
	if r.DLQ != "" {
		dlq, err := parseARN("DLQ", r.DLQ)
		if err != nil {
			return delivery.Request{}, err
		}
		req.DLQ = &dlq
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
		return sqsQueueRecord{Queue: t.ARN.String(), Body: t.Body, Attributes: attributeRecordsOf(t.Attributes), GroupID: t.GroupID, DedupID: t.DedupID}
	case delivery.LambdaFunction:
		return lambdaFunctionRecord{Function: t.ARN.String(), Qualifier: t.Qualifier, Event: t.Event, Invocation: t.Invocation}
	case delivery.SNSTopic:
		return snsTopicRecord{Topic: t.ARN.String(), Message: t.Message, Subject: t.Subject, Attributes: attributeRecordsOf(t.Attributes)}
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
		return delivery.SQSQueue{ARN: queue, Body: r.Body, Attributes: attrs, GroupID: r.GroupID, DedupID: r.DedupID}, nil
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
	}
	return nil, &RequestError{Field: "Target", Reason: "is missing"}
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
	}
	return nil
}

// policyOf returns the retry policy r persists; false for a nil record.
func policyOf(r policyRecord) (delivery.RetryPolicy, bool) {
	switch r := r.(type) {
	case snsManagedRecord:
		return delivery.SNSManaged{}, true
	case eventBridgeRecord:
		return delivery.EventBridgeTarget{MaxAge: time.Duration(r.MaxAgeMillis) * time.Millisecond, MaxAttempts: r.MaxAttempts}, true
	case s3NotificationRecord:
		return delivery.S3Notification{}, true
	case noneRecord:
		return delivery.None{}, true
	}
	return nil, false
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
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	policies, err := jsonx.NewUnion("outbox.policyRecord",
		jsonx.Variant[policyRecord, snsManagedRecord]("sns-managed"),
		jsonx.Variant[policyRecord, eventBridgeRecord]("eventbridge-target"),
		jsonx.Variant[policyRecord, s3NotificationRecord]("s3-notification"),
		jsonx.Variant[policyRecord, noneRecord]("none"),
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
	i := strings.LastIndexByte(s, '/')
	if i < 0 {
		return aws.Scope{}, "", false
	}
	scope, err := store.ScopeKeys().Decode(s[:i])
	if err != nil || s[i+1:] == "" {
		return aws.Scope{}, "", false
	}
	return scope, ids.DeliveryID(s[i+1:]), true
}
