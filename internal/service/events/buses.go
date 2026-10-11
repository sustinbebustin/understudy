package events

import (
	"context"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// defaultBus is the name of the one bus each account and region has.
const defaultBus = "default"

// busARN is arn:aws:events:<region>:<account>:event-bus/<name>.
func busARN(scope aws.Scope, name string) aws.ARN {
	return aws.ARN{
		Partition: scope.Partition.String(),
		Service:   "events",
		Region:    scope.Region.String(),
		Account:   scope.Account.String(),
		Resource:  "event-bus/" + name,
	}
}

// busName is the bus an EventBusName names, a name or an ARN; nil and
// empty name the default bus. isDefault says whether it is scope's default
// bus, the only bus that exists.
func busName(scope aws.Scope, name *string) (string, bool) {
	if name == nil || *name == "" || *name == defaultBus {
		return defaultBus, true
	}
	if arn, err := aws.ParseARN(*name); err == nil && arn.Service == "events" {
		bus, ok := strings.CutPrefix(arn.Resource, "event-bus/")
		if !ok {
			return *name, false
		}
		return bus, arn == busARN(scope, defaultBus)
	}
	return *name, false
}

// requireDefaultBus answers ResourceNotFoundException for any bus other
// than the caller's default bus.
func requireDefaultBus(scope aws.Scope, name *string) *awserr.Error {
	if bus, ok := busName(scope, name); !ok {
		return errBusNotFound(bus)
	}
	return nil
}

// ruleRef locates a rule of a default bus: its scope and name.
type ruleRef struct {
	scope aws.Scope
	name  string
}

// arn returns arn:aws:events:<region>:<account>:rule/<name>, the
// default-bus form.
func (r ruleRef) arn() aws.ARN {
	return aws.ARN{
		Partition: r.scope.Partition.String(),
		Service:   "events",
		Region:    r.scope.Region.String(),
		Account:   r.scope.Account.String(),
		Resource:  "rule/" + r.name,
	}
}

// parseRuleARN reads a default-bus rule ARN back into its scope and name.
func parseRuleARN(s string) (ruleRef, bool) {
	arn, err := aws.ParseARN(s)
	if err != nil || arn.Service != "events" {
		return ruleRef{}, false
	}
	name, ok := strings.CutPrefix(arn.Resource, "rule/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return ruleRef{}, false
	}
	scope, ok := scopeOf(arn)
	if !ok {
		return ruleRef{}, false
	}
	return ruleRef{scope: scope, name: name}, true
}

// scopeOf returns the account and region an ARN names, when it names one
// understudy serves: the aws partition, an account ID and a known region.
func scopeOf(arn aws.ARN) (aws.Scope, bool) {
	var s aws.Scope
	if err := s.Partition.UnmarshalText([]byte(arn.Partition)); err != nil || s.Partition != aws.PartitionAWS() {
		return aws.Scope{}, false
	}
	account, err := aws.ParseAccountID(arn.Account)
	if err != nil {
		return aws.Scope{}, false
	}
	s.Account = account
	if err := s.Region.UnmarshalText([]byte(arn.Region)); err != nil || s.Region.IsZero() {
		return aws.Scope{}, false
	}
	return s, true
}

// bindBuses binds the event bus operations: the default bus only.
func (s *Service) bindBuses(b eventbridgeapi.Binder) {
	b.DescribeEventBus(s.describeEventBus, registry.Partial{Note: "the default bus only, with its Name and Arn; custom buses are not implemented"})
	b.ListEventBuses(s.listEventBuses, registry.Partial{Note: "the default bus only, with its Name and Arn; custom buses are not implemented"})
}

func (*Service) describeEventBus(_ context.Context, c *kernel.Call, in *eventbridgeapi.DescribeEventBusInput) (*eventbridgeapi.DescribeEventBusOutput, *awserr.Error) {
	if err := requireDefaultBus(c.Scope(), in.Name); err != nil {
		return nil, err
	}
	arn := busARN(c.Scope(), defaultBus)
	c.Touch(arn)
	return &eventbridgeapi.DescribeEventBusOutput{Name: new(defaultBus), Arn: new(arn.String())}, nil
}

func (*Service) listEventBuses(_ context.Context, c *kernel.Call, in *eventbridgeapi.ListEventBusesInput) (*eventbridgeapi.ListEventBusesOutput, *awserr.Error) {
	var buses []eventbridgeapi.EventBus
	if in.NamePrefix == nil || strings.HasPrefix(defaultBus, *in.NamePrefix) {
		buses = append(buses, eventbridgeapi.EventBus{Name: new(defaultBus), Arn: new(busARN(c.Scope(), defaultBus).String())})
	}
	out, next, err := page(buses, in.NextToken, in.Limit)
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.ListEventBusesOutput{EventBuses: out, NextToken: next}, nil
}

// defaultPageSize is the page size of a List call without Limit; the
// models cap Limit at 100 and AWS documents no default.
const defaultPageSize = 100

// page returns the page of items a NextToken and Limit select, and the
// token of the next page. A token is the decimal offset of the page's first
// item; one understudy did not issue answers ValidationException.
func page[T any](items []T, token *string, limit *int32) (out []T, next *string, err *awserr.Error) {
	start := 0
	if token != nil {
		n, err := strconv.Atoi(*token)
		if err != nil || n < 0 || n > len(items) || strconv.Itoa(n) != *token {
			return nil, nil, errInvalidParameter("NextToken")
		}
		start = n
	}
	size := defaultPageSize
	if limit != nil && *limit > 0 {
		size = int(*limit)
	}
	end := min(start+size, len(items))
	if end == len(items) {
		return items[start:end], nil, nil
	}
	return items[start:end], new(strconv.Itoa(end)), nil
}
