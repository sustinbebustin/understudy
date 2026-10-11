package sqs

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// Attributes are the settable attributes every queue has. Policy is
// checked for form and size, KmsDataKeyReusePeriodSeconds for range, and
// KmsMasterKeyId not at all; all three are stored and echoed and have no
// runtime effect (the package doc says why).
type Attributes struct {
	DelaySeconds                  int32  `json:"delaySeconds"`
	MaximumMessageSize            int32  `json:"maximumMessageSize"`
	MessageRetentionPeriod        int32  `json:"messageRetentionPeriod"`
	ReceiveMessageWaitTimeSeconds int32  `json:"receiveMessageWaitTimeSeconds"`
	VisibilityTimeout             int32  `json:"visibilityTimeout"`
	SqsManagedSseEnabled          bool   `json:"sqsManagedSseEnabled"`
	KmsMasterKeyID                string `json:"kmsMasterKeyId,omitempty"`
	// KmsDataKeyReusePeriodSeconds is reported only while a KMS key is
	// set (sqs.md 4).
	KmsDataKeyReusePeriodSeconds int32               `json:"kmsDataKeyReusePeriodSeconds"`
	Policy                       string              `json:"policy,omitempty"`
	RedrivePolicy                *RedrivePolicy      `json:"redrivePolicy,omitempty"`
	RedriveAllowPolicy           *RedriveAllowPolicy `json:"redriveAllowPolicy,omitempty"`
}

// defaultAttributes are a new queue's values (sqs.md 4): the recorded
// 1 MiB MaximumMessageSize, four days of retention, a 30 s visibility
// timeout, SQS-managed encryption on and a 300 s data key reuse period.
func defaultAttributes() Attributes {
	return Attributes{
		MaximumMessageSize:           1 << 20,
		MessageRetentionPeriod:       345600,
		VisibilityTimeout:            30,
		SqsManagedSseEnabled:         true,
		KmsDataKeyReusePeriodSeconds: 300,
	}
}

// RedrivePolicy sends a message to DeadLetterTarget, its
// deadLetterTargetArn, on the receive that would exceed MaxReceiveCount
// (sqs.md 9.2).
type RedrivePolicy struct {
	DeadLetterTarget queueRef `json:"deadLetterTargetArn"`
	MaxReceiveCount  int      `json:"maxReceiveCount"`
}

// RedrivePermission says which source queues may name a queue as their
// dead-letter queue.
type RedrivePermission string

// RedrivePermission values.
const (
	RedriveAllowAll RedrivePermission = "allowAll"
	RedriveDenyAll  RedrivePermission = "denyAll"
	RedriveByQueue  RedrivePermission = "byQueue"
)

// RedriveAllowPolicy limits the queues that may use a queue as their
// dead-letter queue (sqs.md 9.1).
type RedriveAllowPolicy struct {
	RedrivePermission RedrivePermission `json:"redrivePermission"`
	SourceQueueArns   []string          `json:"sourceQueueArns,omitempty"`
}

// String renders p the way GetQueueAttributes returns it: compact, keys
// sorted, the count a number. Terraform plans a change on every other
// spelling (tf-messaging.md 4.1).
func (p *RedrivePolicy) String() string {
	return redrivePolicyText(p.DeadLetterTarget.arn().String(), p.MaxReceiveCount)
}

// redrivePolicyText renders a redrive policy as RedrivePolicy.String does,
// from a target ARN that may not parse, for the error that refuses it.
func redrivePolicyText(target string, maxReceiveCount int) string {
	b := append([]byte(`{"deadLetterTargetArn":`), jsonx.AppendString(nil, target)...)
	b = append(b, `,"maxReceiveCount":`...)
	b = strconv.AppendInt(b, int64(maxReceiveCount), 10)
	return string(append(b, '}'))
}

// String renders p compact with its keys sorted, as RedrivePolicy.String.
func (p *RedriveAllowPolicy) String() string {
	b := append([]byte(`{"redrivePermission":`), jsonx.AppendString(nil, string(p.RedrivePermission))...)
	if len(p.SourceQueueArns) > 0 {
		b = append(b, `,"sourceQueueArns":[`...)
		for i, arn := range p.SourceQueueArns {
			if i > 0 {
				b = append(b, ',')
			}
			b = jsonx.AppendString(b, arn)
		}
		b = append(b, ']')
	}
	return string(append(b, '}'))
}

// allows reports whether p lets the queue source name its queue as their
// dead-letter queue; no policy allows every queue.
func (p *RedriveAllowPolicy) allows(source string) bool {
	if p == nil {
		return true
	}
	switch p.RedrivePermission {
	case RedriveAllowAll:
		return true
	case RedriveByQueue:
		return slices.Contains(p.SourceQueueArns, source)
	case RedriveDenyAll:
	}
	return false
}

// attribute is one settable queue attribute: parse returns the canonical
// text of a valid value and how to apply it, or the error that refuses
// it; get returns a queue's current value in the same canonical text.
type attribute struct {
	name sqsapi.QueueAttributeName
	// fifo marks the attributes only FIFO queues take.
	fifo  bool
	parse func(v string) (string, func(*Queue), *awserr.Error)
	get   func(q *Queue) string
}

func intAttribute(name sqsapi.QueueAttributeName, minV, maxV int64, field func(*Attributes) *int32) attribute {
	return attribute{
		name: name,
		parse: func(v string) (string, func(*Queue), *awserr.Error) {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
			if err != nil || n < minV || n > maxV {
				return "", nil, errInvalidAttributeValue(name)
			}
			return strconv.FormatInt(n, 10), func(q *Queue) { *field(&q.Attributes) = int32(n) }, nil //nolint:gosec // ParseInt with bitSize 32 bounds n to int32
		},
		get: func(q *Queue) string { return strconv.FormatInt(int64(*field(&q.Attributes)), 10) },
	}
}

// boolAttribute takes true or false in any case.
func boolAttribute(name sqsapi.QueueAttributeName, fifo bool, set func(q *Queue, v bool), get func(q *Queue) bool) attribute {
	return attribute{
		name: name,
		fifo: fifo,
		parse: func(v string) (string, func(*Queue), *awserr.Error) {
			b, ok := parseBool(v)
			if !ok {
				return "", nil, errInvalidAttributeValue(name)
			}
			return strconv.FormatBool(b), func(q *Queue) { set(q, b) }, nil
		},
		get: func(q *Queue) string { return strconv.FormatBool(get(q)) },
	}
}

func parseBool(v string) (value, ok bool) {
	switch strings.ToLower(v) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// enumAttribute is a FIFO attribute with a closed set of values.
func enumAttribute[T ~string](name sqsapi.QueueAttributeName, values []T, field func(*FIFO) *T) attribute {
	return attribute{
		name: name,
		fifo: true,
		parse: func(v string) (string, func(*Queue), *awserr.Error) {
			i := slices.Index(values, T(v))
			if i < 0 {
				return "", nil, errInvalidAttributeValue(name)
			}
			return v, func(q *Queue) { *field(q.FIFO) = values[i] }, nil
		},
		get: func(q *Queue) string {
			if q.FIFO == nil {
				return ""
			}
			return string(*field(q.FIFO))
		},
	}
}

// The Policy limits understudy enforces, of those sqs.md 4 documents; the
// counts per statement are not (policyLimits says why).
const (
	maxPolicyBytes      = 8192
	maxPolicyStatements = 20
)

// parsePolicy accepts a JSON object of at most 8,192 bytes and 20
// statements, or "" for no policy (sqs.md 4); the statements are stored
// verbatim, not evaluated. JSON null decodes to no object, so it is
// refused too. Statement is one object or a list.
func parsePolicy(v string) (canonical string, apply func(*Queue), err *awserr.Error) {
	apply = func(q *Queue) { q.Attributes.Policy = v }
	if v == "" {
		return v, apply, nil
	}
	if len(v) > maxPolicyBytes {
		return "", nil, errPolicyTooLarge(len(v))
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(v), &doc) != nil || doc == nil {
		return "", nil, errInvalidAttributeValue(sqsapi.QueueAttributeNamePolicy)
	}
	var statements []json.RawMessage
	if json.Unmarshal(doc["Statement"], &statements) == nil && len(statements) > maxPolicyStatements {
		return "", nil, errPolicyStatements(len(statements))
	}
	return v, apply, nil
}

// parseRedrivePolicy checks a RedrivePolicy (sqs.md 5.1 and 9.1): "" for
// none, else a JSON object with a deadLetterTargetArn and a maxReceiveCount
// from 1 to 1000, given as a number or a numeric string. Whether the target
// exists and accepts the source is checked against the state
// (checkRedrive).
func parseRedrivePolicy(v string) (canonical string, apply func(*Queue), err *awserr.Error) {
	if v == "" {
		return "", func(q *Queue) { q.Attributes.RedrivePolicy = nil }, nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(v), &doc) != nil {
		return "", nil, errInvalidAttributeValue(sqsapi.QueueAttributeNameRedrivePolicy)
	}
	var target string
	if raw, ok := doc["deadLetterTargetArn"]; !ok || json.Unmarshal(raw, &target) != nil || target == "" {
		return "", nil, errRedriveParameterMissing("deadLetterTargetArn")
	}
	raw, ok := doc["maxReceiveCount"]
	if !ok || string(raw) == "null" {
		return "", nil, errRedriveParameterMissing("maxReceiveCount")
	}
	count := string(raw)
	var quoted string
	if json.Unmarshal(raw, &quoted) == nil {
		count = quoted
	}
	n, nerr := strconv.Atoi(count)
	if nerr != nil || n < 1 || n > 1000 {
		return "", nil, errMaxReceiveCount(v, count)
	}
	dlq, ok := parseQueueARN(target)
	if !ok {
		return "", nil, errRedriveTarget(redrivePolicyText(target, n), "Invalid value for deadLetterTargetArn.")
	}
	p := RedrivePolicy{DeadLetterTarget: dlq, MaxReceiveCount: n}
	return p.String(), func(q *Queue) { q.Attributes.RedrivePolicy = &p }, nil
}

// parseRedriveAllowPolicy checks a RedriveAllowPolicy (sqs.md 4 and 9.1):
// "" for none, else a JSON object whose redrivePermission is allowAll,
// denyAll or byQueue, with sourceQueueArns (at most 10) only for byQueue.
func parseRedriveAllowPolicy(v string) (canonical string, apply func(*Queue), err *awserr.Error) {
	if v == "" {
		return "", func(q *Queue) { q.Attributes.RedriveAllowPolicy = nil }, nil
	}
	var p RedriveAllowPolicy
	if json.Unmarshal([]byte(v), &p) != nil {
		return "", nil, errInvalidAttributeValue(sqsapi.QueueAttributeNameRedriveAllowPolicy)
	}
	valid := false
	switch p.RedrivePermission {
	case RedriveAllowAll, RedriveDenyAll:
		valid = len(p.SourceQueueArns) == 0
	case RedriveByQueue:
		valid = len(p.SourceQueueArns) >= 1 && len(p.SourceQueueArns) <= 10
	}
	if !valid {
		return "", nil, errInvalidAttributeValue(sqsapi.QueueAttributeNameRedriveAllowPolicy)
	}
	return p.String(), func(q *Queue) { q.Attributes.RedriveAllowPolicy = &p }, nil
}

// settable lists the attributes CreateQueue and SetQueueAttributes take,
// with the ranges of sqs.md 4.
func settable() []attribute {
	return []attribute{
		intAttribute(sqsapi.QueueAttributeNameDelaySeconds, 0, 900, func(a *Attributes) *int32 { return &a.DelaySeconds }),
		intAttribute(sqsapi.QueueAttributeNameMaximumMessageSize, 1024, 1<<20, func(a *Attributes) *int32 { return &a.MaximumMessageSize }),
		intAttribute(sqsapi.QueueAttributeNameMessageRetentionPeriod, 60, 1209600, func(a *Attributes) *int32 { return &a.MessageRetentionPeriod }),
		intAttribute(sqsapi.QueueAttributeNameReceiveMessageWaitTimeSeconds, 0, 20, func(a *Attributes) *int32 { return &a.ReceiveMessageWaitTimeSeconds }),
		intAttribute(sqsapi.QueueAttributeNameVisibilityTimeout, 0, 43200, func(a *Attributes) *int32 { return &a.VisibilityTimeout }),
		intAttribute(sqsapi.QueueAttributeNameKmsDataKeyReusePeriodSeconds, 60, 86400, func(a *Attributes) *int32 { return &a.KmsDataKeyReusePeriodSeconds }),
		// SQS-managed encryption and a KMS key exclude each other: setting
		// one clears the other (sqs.md 4).
		boolAttribute(sqsapi.QueueAttributeNameSqsManagedSseEnabled, false,
			func(q *Queue, v bool) {
				q.Attributes.SqsManagedSseEnabled = v
				if v {
					q.Attributes.KmsMasterKeyID = ""
				}
			},
			func(q *Queue) bool { return q.Attributes.SqsManagedSseEnabled }),
		{
			name: sqsapi.QueueAttributeNameKmsMasterKeyId,
			parse: func(v string) (string, func(*Queue), *awserr.Error) {
				return v, func(q *Queue) {
					q.Attributes.KmsMasterKeyID = v
					if v != "" {
						q.Attributes.SqsManagedSseEnabled = false
					}
				}, nil
			},
			get: func(q *Queue) string { return q.Attributes.KmsMasterKeyID },
		},
		{name: sqsapi.QueueAttributeNamePolicy, parse: parsePolicy, get: func(q *Queue) string { return q.Attributes.Policy }},
		{name: sqsapi.QueueAttributeNameRedrivePolicy, parse: parseRedrivePolicy, get: func(q *Queue) string {
			if p := q.Attributes.RedrivePolicy; p != nil {
				return p.String()
			}
			return ""
		}},
		{name: sqsapi.QueueAttributeNameRedriveAllowPolicy, parse: parseRedriveAllowPolicy, get: func(q *Queue) string {
			if p := q.Attributes.RedriveAllowPolicy; p != nil {
				return p.String()
			}
			return ""
		}},
		boolAttribute(sqsapi.QueueAttributeNameContentBasedDeduplication, true,
			func(q *Queue, v bool) { q.FIFO.ContentBasedDeduplication = v },
			func(q *Queue) bool { return q.FIFO != nil && q.FIFO.ContentBasedDeduplication }),
		enumAttribute(sqsapi.QueueAttributeNameDeduplicationScope, []DedupScope{DedupQueue, DedupMessageGroup},
			func(f *FIFO) *DedupScope { return &f.DeduplicationScope }),
		enumAttribute(sqsapi.QueueAttributeNameFifoThroughputLimit, []ThroughputLimit{ThroughputPerQueue, ThroughputPerMessageGroupID},
			func(f *FIFO) *ThroughputLimit { return &f.FifoThroughputLimit }),
	}
}

// setting is one parsed attribute of a request.
type setting struct {
	name      sqsapi.QueueAttributeName
	canonical string
	apply     func(*Queue)
	get       func(*Queue) string
}

// parseAttributes reads a CreateQueue or SetQueueAttributes request for a
// queue of the given type, in name order, so the first error and the
// first conflict reported are the same on every run. FifoQueue is
// accepted as "true" on a FIFO queue, where it changes nothing; on a
// standard queue SQS does not know it (sqs.md 4).
func parseAttributes(in map[sqsapi.QueueAttributeName]string, fifo bool) ([]setting, *awserr.Error) {
	table := settable()
	var out []setting
	for _, name := range slices.Sorted(maps.Keys(in)) {
		if name == sqsapi.QueueAttributeNameFifoQueue && fifo {
			if b, ok := parseBool(in[name]); !ok || !b {
				return nil, errModifyQueueType()
			}
			continue
		}
		i := slices.IndexFunc(table, func(a attribute) bool { return a.name == name })
		switch {
		case i < 0:
			return nil, errUnknownAttribute(string(name))
		case table[i].fifo && !fifo:
			return nil, errFifoOnlyAttribute(name)
		}
		canonical, apply, err := table[i].parse(in[name])
		if err != nil {
			return nil, err
		}
		out = append(out, setting{name: name, canonical: canonical, apply: apply, get: table[i].get})
	}
	if err := checkEncryption(out); err != nil {
		return nil, err
	}
	return out, nil
}

// checkEncryption refuses KMS and SQS-managed encryption in one request.
func checkEncryption(ss []setting) *awserr.Error {
	kms, sqsManaged := false, false
	for _, s := range ss {
		if s.name == sqsapi.QueueAttributeNameKmsMasterKeyId {
			kms = s.canonical != ""
		}
		if s.name == sqsapi.QueueAttributeNameSqsManagedSseEnabled {
			sqsManaged = s.canonical == "true"
		}
	}
	if kms && sqsManaged {
		return errTwoEncryptionTypes()
	}
	return nil
}

// checkThroughput refuses FifoThroughputLimit=perMessageGroupId without
// DeduplicationScope=messageGroup, the documented pairing (sqs.md 4).
func checkThroughput(q *Queue) *awserr.Error {
	if q.FIFO != nil && q.FIFO.FifoThroughputLimit == ThroughputPerMessageGroupID && q.FIFO.DeduplicationScope != DedupMessageGroup {
		return errInvalidAttributeValue(sqsapi.QueueAttributeNameFifoThroughputLimit)
	}
	return nil
}

// firstConflict returns the first requested attribute whose value differs
// from the queue's current one (sqs.md 5.1), and false when all agree.
func firstConflict(current *Queue, ss []setting) (sqsapi.QueueAttributeName, bool) {
	for _, s := range ss {
		if s.get(current) != s.canonical {
			return s.name, true
		}
	}
	return "", false
}

// sets reports whether ss sets name.
func sets(ss []setting, name sqsapi.QueueAttributeName) bool {
	return slices.ContainsFunc(ss, func(s setting) bool { return s.name == name })
}

// checkRedrive checks source's redrive policy against the state when the
// request set one (sqs.md 9.1): the target is a queue of the same account,
// region and type whose redrive allow policy admits source. AWS rejects
// the other cases; its texts are unrecorded.
func checkRedrive(st *State, scope aws.Scope, source *Queue) *awserr.Error {
	p := source.Attributes.RedrivePolicy
	if p == nil {
		return nil
	}
	policy := p.String()
	if p.DeadLetterTarget.scope != scope {
		return errRedriveTarget(policy, "Invalid value for deadLetterTargetArn.")
	}
	dlq, ok := st.Queues[p.DeadLetterTarget.name]
	if !ok {
		return errRedriveTarget(policy, "Dead letter target does not exist.")
	}
	if (dlq.FIFO == nil) != (source.FIFO == nil) {
		return errRedriveTarget(policy, "Dead-letter queue must be the same type of queue as the source queue.")
	}
	sourceARN := queueRef{scope: scope, name: source.Name}.arn().String()
	if !dlq.Attributes.RedriveAllowPolicy.allows(sourceARN) {
		return errRedriveTarget(policy, "Queue "+p.DeadLetterTarget.arn().String()+" does not allow "+sourceARN+" to use it as a dead-letter queue.")
	}
	return nil
}

// report returns every attribute GetQueueAttributes shows for q at now
// (sqs.md 4): the settable ones that apply, the counts, the timestamps
// and the ARN. Policies and the KMS settings show only when set.
func report(q *Queue, arn aws.ARN, now clock.Instant) map[sqsapi.QueueAttributeName]string {
	a := q.Attributes
	itoa := func(n int32) string { return strconv.FormatInt(int64(n), 10) }
	var available, inFlight, delayed int
	for i := range q.Messages {
		m := &q.Messages[i]
		switch {
		case m.visible(now):
			available++
		case m.Receives != nil:
			inFlight++
		default:
			delayed++
		}
	}
	out := map[sqsapi.QueueAttributeName]string{
		sqsapi.QueueAttributeNameApproximateNumberOfMessages:           strconv.Itoa(available),
		sqsapi.QueueAttributeNameApproximateNumberOfMessagesNotVisible: strconv.Itoa(inFlight),
		sqsapi.QueueAttributeNameApproximateNumberOfMessagesDelayed:    strconv.Itoa(delayed),
		sqsapi.QueueAttributeNameCreatedTimestamp:                      strconv.FormatInt(q.Created.Wire().Unix(), 10),
		sqsapi.QueueAttributeNameLastModifiedTimestamp:                 strconv.FormatInt(q.Modified.Wire().Unix(), 10),
		sqsapi.QueueAttributeNameQueueArn:                              arn.String(),
		sqsapi.QueueAttributeNameDelaySeconds:                          itoa(a.DelaySeconds),
		sqsapi.QueueAttributeNameMaximumMessageSize:                    itoa(a.MaximumMessageSize),
		sqsapi.QueueAttributeNameMessageRetentionPeriod:                itoa(a.MessageRetentionPeriod),
		sqsapi.QueueAttributeNameReceiveMessageWaitTimeSeconds:         itoa(a.ReceiveMessageWaitTimeSeconds),
		sqsapi.QueueAttributeNameVisibilityTimeout:                     itoa(a.VisibilityTimeout),
		sqsapi.QueueAttributeNameSqsManagedSseEnabled:                  strconv.FormatBool(a.SqsManagedSseEnabled),
	}
	if a.KmsMasterKeyID != "" {
		out[sqsapi.QueueAttributeNameKmsMasterKeyId] = a.KmsMasterKeyID
		out[sqsapi.QueueAttributeNameKmsDataKeyReusePeriodSeconds] = itoa(a.KmsDataKeyReusePeriodSeconds)
	}
	if a.Policy != "" {
		out[sqsapi.QueueAttributeNamePolicy] = a.Policy
	}
	if a.RedrivePolicy != nil {
		out[sqsapi.QueueAttributeNameRedrivePolicy] = a.RedrivePolicy.String()
	}
	if a.RedriveAllowPolicy != nil {
		out[sqsapi.QueueAttributeNameRedriveAllowPolicy] = a.RedriveAllowPolicy.String()
	}
	if f := q.FIFO; f != nil {
		out[sqsapi.QueueAttributeNameFifoQueue] = "true"
		out[sqsapi.QueueAttributeNameContentBasedDeduplication] = strconv.FormatBool(f.ContentBasedDeduplication)
		out[sqsapi.QueueAttributeNameDeduplicationScope] = string(f.DeduplicationScope)
		out[sqsapi.QueueAttributeNameFifoThroughputLimit] = string(f.FifoThroughputLimit)
	}
	return out
}
