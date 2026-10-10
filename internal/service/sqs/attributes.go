package sqs

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// Attributes are a standard queue's settable attributes. Policy, the
// redrive policies and the KMS settings are checked for form, stored and
// echoed; they have no runtime effect yet (the coverage note of CreateQueue
// says so).
type Attributes struct {
	DelaySeconds                  int32  `json:"delaySeconds"`
	MaximumMessageSize            int32  `json:"maximumMessageSize"`
	MessageRetentionPeriod        int32  `json:"messageRetentionPeriod"`
	ReceiveMessageWaitTimeSeconds int32  `json:"receiveMessageWaitTimeSeconds"`
	VisibilityTimeout             int32  `json:"visibilityTimeout"`
	SqsManagedSseEnabled          bool   `json:"sqsManagedSseEnabled"`
	KmsMasterKeyID                string `json:"kmsMasterKeyId,omitempty"`
	KmsDataKeyReusePeriodSeconds  int32  `json:"kmsDataKeyReusePeriodSeconds,omitempty"`
	Policy                        string `json:"policy,omitempty"`
	RedrivePolicy                 string `json:"redrivePolicy,omitempty"`
	RedriveAllowPolicy            string `json:"redriveAllowPolicy,omitempty"`
}

// defaultAttributes are a new standard queue's values (sqs.md 4): the
// recorded 1 MiB MaximumMessageSize, four days of retention, a 30 s
// visibility timeout and SQS-managed encryption on.
func defaultAttributes() Attributes {
	return Attributes{
		MaximumMessageSize:     1 << 20,
		MessageRetentionPeriod: 345600,
		VisibilityTimeout:      30,
		SqsManagedSseEnabled:   true,
	}
}

// attribute is one settable queue attribute: its parser, which returns the
// canonical text of a valid value or the error that refuses it, its setter
// and its current value.
type attribute struct {
	name  sqsapi.QueueAttributeName
	parse func(v string) (string, *awserr.Error)
	set   func(a *Attributes, canonical string)
	get   func(a Attributes) string
}

func intAttribute(name sqsapi.QueueAttributeName, minV, maxV int64, field func(*Attributes) *int32) attribute {
	return attribute{
		name: name,
		parse: func(v string) (string, *awserr.Error) {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
			if err != nil || n < minV || n > maxV {
				return "", errInvalidAttributeValue(name)
			}
			return strconv.FormatInt(n, 10), nil
		},
		set: func(a *Attributes, c string) {
			n, _ := strconv.ParseInt(c, 10, 32) //nolint:errcheck // c is parse's canonical text
			*field(a) = int32(n)
		},
		get: func(a Attributes) string { return strconv.FormatInt(int64(*field(&a)), 10) },
	}
}

// textAttribute is an attribute stored as the text given; parse checks it
// and returns the error that refuses it.
func textAttribute(name sqsapi.QueueAttributeName, parse func(v string) *awserr.Error, field func(*Attributes) *string) attribute {
	return attribute{
		name: name,
		parse: func(v string) (string, *awserr.Error) {
			if err := parse(v); err != nil {
				return "", err
			}
			return v, nil
		},
		set: func(a *Attributes, c string) { *field(a) = c },
		get: func(a Attributes) string { return *field(&a) },
	}
}

func anyText(string) *awserr.Error { return nil }

// parsePolicy accepts a JSON object, or "" for no policy (sqs.md 4); the
// statements are stored, not evaluated.
func parsePolicy(v string) *awserr.Error {
	var doc map[string]json.RawMessage
	if v != "" && json.Unmarshal([]byte(v), &doc) != nil {
		return errInvalidAttributeValue(sqsapi.QueueAttributeNamePolicy)
	}
	return nil
}

// parseRedrivePolicy checks a RedrivePolicy (sqs.md 5.1 and 9.1): "" for
// none, else a JSON object with a deadLetterTargetArn and a maxReceiveCount
// from 1 to 1000, given as a number or a numeric string.
func parseRedrivePolicy(v string) *awserr.Error {
	if v == "" {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(v), &doc) != nil {
		return errInvalidAttributeValue(sqsapi.QueueAttributeNameRedrivePolicy)
	}
	var target string
	if raw, ok := doc["deadLetterTargetArn"]; !ok || json.Unmarshal(raw, &target) != nil || target == "" {
		return errRedriveParameterMissing("deadLetterTargetArn")
	}
	raw, ok := doc["maxReceiveCount"]
	if !ok || string(raw) == "null" {
		return errRedriveParameterMissing("maxReceiveCount")
	}
	count := string(raw)
	var quoted string
	if json.Unmarshal(raw, &quoted) == nil {
		count = quoted
	}
	if n, err := strconv.Atoi(count); err != nil || n < 1 || n > 1000 {
		return errMaxReceiveCount(v, count)
	}
	return nil
}

// parseRedriveAllowPolicy checks a RedriveAllowPolicy (sqs.md 4 and 9.1):
// "" for none, else a JSON object whose redrivePermission is allowAll,
// denyAll or byQueue, with sourceQueueArns (at most 10) only for byQueue.
func parseRedriveAllowPolicy(v string) *awserr.Error {
	if v == "" {
		return nil
	}
	var doc struct {
		RedrivePermission string   `json:"redrivePermission"`
		SourceQueueArns   []string `json:"sourceQueueArns"`
	}
	if json.Unmarshal([]byte(v), &doc) != nil {
		return errInvalidAttributeValue(sqsapi.QueueAttributeNameRedriveAllowPolicy)
	}
	switch doc.RedrivePermission {
	case "allowAll", "denyAll":
		if len(doc.SourceQueueArns) == 0 {
			return nil
		}
	case "byQueue":
		if len(doc.SourceQueueArns) >= 1 && len(doc.SourceQueueArns) <= 10 {
			return nil
		}
	}
	return errInvalidAttributeValue(sqsapi.QueueAttributeNameRedriveAllowPolicy)
}

// settable lists the attributes CreateQueue takes on a standard queue, with
// the ranges of sqs.md 4.
func settable() []attribute {
	return []attribute{
		intAttribute(sqsapi.QueueAttributeNameDelaySeconds, 0, 900, func(a *Attributes) *int32 { return &a.DelaySeconds }),
		intAttribute(sqsapi.QueueAttributeNameMaximumMessageSize, 1024, 1<<20, func(a *Attributes) *int32 { return &a.MaximumMessageSize }),
		intAttribute(sqsapi.QueueAttributeNameMessageRetentionPeriod, 60, 1209600, func(a *Attributes) *int32 { return &a.MessageRetentionPeriod }),
		intAttribute(sqsapi.QueueAttributeNameReceiveMessageWaitTimeSeconds, 0, 20, func(a *Attributes) *int32 { return &a.ReceiveMessageWaitTimeSeconds }),
		intAttribute(sqsapi.QueueAttributeNameVisibilityTimeout, 0, 43200, func(a *Attributes) *int32 { return &a.VisibilityTimeout }),
		intAttribute(sqsapi.QueueAttributeNameKmsDataKeyReusePeriodSeconds, 60, 86400, func(a *Attributes) *int32 { return &a.KmsDataKeyReusePeriodSeconds }),
		{
			name: sqsapi.QueueAttributeNameSqsManagedSseEnabled,
			parse: func(v string) (string, *awserr.Error) {
				lower := strings.ToLower(v)
				if lower != "true" && lower != "false" {
					return "", errInvalidAttributeValue(sqsapi.QueueAttributeNameSqsManagedSseEnabled)
				}
				return lower, nil
			},
			set: func(a *Attributes, c string) { a.SqsManagedSseEnabled = c == "true" },
			get: func(a Attributes) string { return strconv.FormatBool(a.SqsManagedSseEnabled) },
		},
		textAttribute(sqsapi.QueueAttributeNameKmsMasterKeyId, anyText, func(a *Attributes) *string { return &a.KmsMasterKeyID }),
		textAttribute(sqsapi.QueueAttributeNamePolicy, parsePolicy, func(a *Attributes) *string { return &a.Policy }),
		textAttribute(sqsapi.QueueAttributeNameRedrivePolicy, parseRedrivePolicy, func(a *Attributes) *string { return &a.RedrivePolicy }),
		textAttribute(sqsapi.QueueAttributeNameRedriveAllowPolicy, parseRedriveAllowPolicy, func(a *Attributes) *string { return &a.RedriveAllowPolicy }),
	}
}

// fifoOnly are the attributes AWS accepts only with FifoQueue=true.
func fifoOnly(name sqsapi.QueueAttributeName) bool {
	return slices.Contains([]sqsapi.QueueAttributeName{
		sqsapi.QueueAttributeNameContentBasedDeduplication,
		sqsapi.QueueAttributeNameDeduplicationScope,
		sqsapi.QueueAttributeNameFifoThroughputLimit,
	}, name)
}

// setting is one parsed attribute of a request.
type setting struct {
	attr      attribute
	canonical string
}

// parseAttributes reads CreateQueue's attributes in name order, so the first
// conflict reported is the same on every run. FifoQueue was handled before
// (FIFO queues are refused; FifoQueue=false is unknown on a standard
// queue).
func parseAttributes(in map[sqsapi.QueueAttributeName]string) ([]setting, *awserr.Error) {
	table := settable()
	var out []setting
	for _, name := range slices.Sorted(maps.Keys(in)) {
		i := slices.IndexFunc(table, func(a attribute) bool { return a.name == name })
		switch {
		case fifoOnly(name):
			return nil, errFifoOnlyAttribute(name)
		case i < 0:
			return nil, errUnknownAttribute(string(name))
		}
		canonical, err := table[i].parse(in[name])
		if err != nil {
			return nil, err
		}
		out = append(out, setting{attr: table[i], canonical: canonical})
	}
	if err := checkEncryption(out); err != nil {
		return nil, err
	}
	return out, nil
}

// checkEncryption refuses KMS and SQS-managed encryption together.
func checkEncryption(ss []setting) *awserr.Error {
	kms, sqsManaged := false, false
	for _, s := range ss {
		if s.attr.name == sqsapi.QueueAttributeNameKmsMasterKeyId {
			kms = s.canonical != ""
		}
		if s.attr.name == sqsapi.QueueAttributeNameSqsManagedSseEnabled {
			sqsManaged = s.canonical == "true"
		}
	}
	if kms && sqsManaged {
		return errTwoEncryptionTypes()
	}
	return nil
}

// newAttributes applies a creation's settings to the defaults. A KMS key
// turns SQS-managed encryption off unless the request set it (sqs.md 4).
func newAttributes(ss []setting) Attributes {
	a := defaultAttributes()
	for _, s := range ss {
		if s.attr.name == sqsapi.QueueAttributeNameKmsMasterKeyId && s.canonical != "" {
			a.SqsManagedSseEnabled = false
			if a.KmsDataKeyReusePeriodSeconds == 0 {
				a.KmsDataKeyReusePeriodSeconds = 300
			}
		}
	}
	for _, s := range ss {
		s.attr.set(&a, s.canonical)
	}
	return a
}

// firstConflict returns the first requested attribute whose value differs
// from the queue's current one (sqs.md 5.1), and false when all agree.
func firstConflict(current Attributes, ss []setting) (sqsapi.QueueAttributeName, bool) {
	for _, s := range ss {
		if s.attr.get(current) != s.canonical {
			return s.attr.name, true
		}
	}
	return "", false
}
