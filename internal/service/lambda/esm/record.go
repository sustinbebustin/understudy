package esm

import (
	"encoding/json"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// Record is one message of the SQS event a function receives, with the
// keys in the order AWS documents them (lambda-events.md 3).
type Record struct {
	MessageID     string `json:"messageId"`
	ReceiptHandle string `json:"receiptHandle"`
	// Body is the message body as sent, never parsed.
	Body string `json:"body"`
	// Attributes are the message's system attributes; {} when none.
	Attributes map[string]string `json:"attributes"`
	// MessageAttributes are the user's attributes; {} when none.
	MessageAttributes map[string]MessageAttribute `json:"messageAttributes"`
	MD5OfBody         string                      `json:"md5OfBody"`
	// MD5OfMessageAttributes is null when the message has no attributes,
	// as AWS delivers it.
	MD5OfMessageAttributes *string `json:"md5OfMessageAttributes"`
	EventSource            string  `json:"eventSource"`
	EventSourceARN         string  `json:"eventSourceARN"`
	AWSRegion              string  `json:"awsRegion"`
}

// MessageAttribute is a message attribute in an SQS event: both list keys
// always, as [] when empty, and the one value key the data type uses.
type MessageAttribute struct {
	StringValue      *string  `json:"stringValue,omitempty"`
	BinaryValue      []byte   `json:"binaryValue,omitempty"`
	StringListValues []string `json:"stringListValues"`
	BinaryListValues [][]byte `json:"binaryListValues"`
	DataType         string   `json:"dataType"`
}

// eventSource is every SQS record's eventSource.
const eventSource = "aws:sqs"

// NewRecord builds the record of a message received from queue with every
// system and message attribute selected.
func NewRecord(m *sqsapi.Message, queue aws.ARN) Record {
	r := Record{
		MessageID:              deref(m.MessageId),
		ReceiptHandle:          deref(m.ReceiptHandle),
		Body:                   deref(m.Body),
		Attributes:             make(map[string]string, len(m.Attributes)),
		MessageAttributes:      make(map[string]MessageAttribute, len(m.MessageAttributes)),
		MD5OfBody:              deref(m.MD5OfBody),
		MD5OfMessageAttributes: m.MD5OfMessageAttributes,
		EventSource:            eventSource,
		EventSourceARN:         queue.String(),
		AWSRegion:              queue.Region,
	}
	for name, v := range m.Attributes {
		r.Attributes[string(name)] = v
	}
	for name, v := range m.MessageAttributes {
		r.MessageAttributes[name] = MessageAttribute{
			StringValue:      v.StringValue,
			BinaryValue:      v.BinaryValue,
			StringListValues: nonNil(v.StringListValues),
			BinaryListValues: nonNil(v.BinaryListValues),
			DataType:         v.DataType,
		}
	}
	return r
}

// filterView is a record as filter patterns see it: the body as JSON when
// it is valid JSON, and as a string otherwise (lambda-events.md 2.4). The
// outer Body shadows the record's.
type filterView struct {
	Record
	Body json.RawMessage `json:"body"`
}

// filterEvent encodes r for filter matching.
func filterEvent(r *Record) ([]byte, error) {
	body := json.RawMessage(r.Body)
	if !json.Valid(body) {
		quoted, err := json.Marshal(r.Body)
		if err != nil {
			return nil, err
		}
		body = quoted
	}
	return json.Marshal(filterView{Record: *r, Body: body})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
