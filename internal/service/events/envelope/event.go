package envelope

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Event is one event on a bus, with the members EventBridge delivers.
type Event struct {
	// ID is the event's UUID, the EventId PutEvents returned for it.
	ID         string
	DetailType string
	Source     string
	Account    string
	// Time is when the event happened: the caller's Time, or the instant
	// EventBridge took the event in.
	Time      time.Time
	Region    string
	Resources []string
	Detail    document.Map
}

// Scheduled is the event a scheduled rule sends at each fire
// (secrets-logs-events-alarms.md 4.3): detail-type "Scheduled Event",
// source "aws.events", the rule's ARN as the one resource and an empty
// detail.
func Scheduled(id, account, region, ruleARN string, at time.Time) Event {
	return Event{
		ID:         id,
		DetailType: "Scheduled Event",
		Source:     "aws.events",
		Account:    account,
		Time:       at,
		Region:     region,
		Resources:  []string{ruleARN},
		Detail:     document.Map{},
	}
}

// formatTime writes t as event times are written: UTC, to the second,
// with a Z suffix (2016-12-30T18:44:49Z).
func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// Document returns the envelope in the recorded member order.
func (e Event) Document() document.Map {
	return document.Map{
		{Key: "version", Value: document.String("0")},
		{Key: "id", Value: document.String(e.ID)},
		{Key: "detail-type", Value: document.String(e.DetailType)},
		{Key: "source", Value: document.String(e.Source)},
		{Key: "account", Value: document.String(e.Account)},
		{Key: "time", Value: document.String(formatTime(e.Time))},
		{Key: "region", Value: document.String(e.Region)},
		{Key: "resources", Value: e.resources()},
		{Key: "detail", Value: e.detail()},
	}
}

// JSON returns the envelope as compact JSON, the payload a target without
// input shaping receives. An error is a detail value JSON cannot hold, such
// as a nil value or a Number that is not a JSON number, which ParseDetail
// never produces.
func (e Event) JSON() ([]byte, error) { return encode(e.Document()) }

// withoutDetail is the aws.events.event variable: the recorded members
// and order, with detailType in camel case and no detail.
func (e Event) withoutDetail() document.Map {
	return document.Map{
		{Key: "id", Value: document.String(e.ID)},
		{Key: "account", Value: document.String(e.Account)},
		{Key: "detailType", Value: document.String(e.DetailType)},
		{Key: "time", Value: document.String(formatTime(e.Time))},
		{Key: "source", Value: document.String(e.Source)},
		{Key: "region", Value: document.String(e.Region)},
		{Key: "resources", Value: e.resources()},
		{Key: "version", Value: document.String("0")},
	}
}

func (e Event) resources() document.List {
	out := make(document.List, len(e.Resources))
	for i, r := range e.Resources {
		out[i] = document.String(r)
	}
	return out
}

func (e Event) detail() document.Map {
	if e.Detail == nil {
		return document.Map{}
	}
	return e.Detail
}

// ParseDetail reads a PutEvents Detail: it must be one JSON object.
func ParseDetail(s string) (document.Map, bool) {
	v, ok := ParseJSON(s)
	if !ok {
		return nil, false
	}
	m, ok := v.(document.Map)
	return m, ok
}

// ParseJSON reads s as exactly one JSON value: a target's constant Input,
// an event TestEventPattern takes.
func ParseJSON(s string) (document.Value, bool) {
	d := jsonx.NewDecoder([]byte(s))
	v, err := d.ReadValue()
	if err != nil {
		return nil, false
	}
	if d.End() != nil {
		return nil, false
	}
	return v, true
}

// encode writes v as compact JSON. Numbers keep their text and object
// members their order. An error (*jsonx.UsageError) is a value JSON cannot
// hold: a nil value or a Number that is not a JSON number.
func encode(v document.Value) ([]byte, error) {
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(v)
	return e.Bytes()
}
