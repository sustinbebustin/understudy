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

// busRef is the bus an EventBusName names, read for a caller in a scope.
// Every account and region has exactly one bus, its default bus.
//
//sumtype:decl
type busRef interface{ isBusRef() }

// ownBus is the caller's default bus: no name, the name default, or its ARN.
type ownBus struct{}

// foreignBus is, by ARN, the default bus of another served account or
// region. It exists, but the caller does not own it.
type foreignBus struct{ scope aws.Scope }

// missingBus is any other bus, which does not exist. name is the bus name
// AWS's not-found text quotes; arn is the bus the name or ARN denotes.
type missingBus struct {
	name string
	arn  aws.ARN
}

func (ownBus) isBusRef()     {}
func (foreignBus) isBusRef() {}
func (missingBus) isBusRef() {}

// arn is the ARN of the default bus.
func (f foreignBus) arn() aws.ARN { return busARN(f.scope, defaultBus) }

// parseBus reads an EventBusName, a name or an ARN, for a caller in scope;
// nil and empty name the caller's default bus. An ARN names its own account
// and region, not the caller's.
func parseBus(scope aws.Scope, name *string) busRef {
	if name == nil || *name == "" || *name == defaultBus {
		return ownBus{}
	}
	arn, err := aws.ParseARN(*name)
	if err != nil {
		return missingBus{name: *name, arn: busARN(scope, *name)}
	}
	bus, isBus := strings.CutPrefix(arn.Resource, "event-bus/")
	switch {
	case arn.Service != "events" || !isBus:
		return missingBus{name: *name, arn: arn}
	case bus != defaultBus:
		return missingBus{name: bus, arn: arn}
	}
	at, serr := aws.ScopeOf(arn)
	switch {
	case serr != nil:
		return missingBus{name: bus, arn: arn}
	case at == scope:
		return ownBus{}
	}
	return foreignBus{scope: at}
}

// requireDefaultBus admits only the caller's default bus to a control-plane
// call: another served account's or region's default bus is NotImplemented,
// and any other bus does not exist.
func requireDefaultBus(scope aws.Scope, name *string) *awserr.Error {
	switch b := parseBus(scope, name).(type) {
	case ownBus:
		return nil
	case foreignBus:
		return errForeignBus(b.arn())
	case missingBus:
		return errBusNotFound(b.name)
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
	scope, err := aws.ScopeOf(arn)
	if err != nil {
		return ruleRef{}, false
	}
	return ruleRef{scope: scope, name: name}, true
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
// item; one understudy did not issue answers ValidationException. An empty
// page is an empty slice, never nil: the serializers leave a nil list out,
// and AWS sends an empty one ({"Rules": []}).
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
	if start == end {
		return []T{}, nil, nil
	}
	if end == len(items) {
		return items[start:end], nil, nil
	}
	return items[start:end], new(strconv.Itoa(end)), nil
}
