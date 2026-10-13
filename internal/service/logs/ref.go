package logs

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// groupRef locates a log group: its scope and name.
type groupRef struct {
	scope aws.Scope
	name  string
}

// arn is the group's ARN without the trailing ":*": logGroupArn, the form
// the tagging operations take.
func (g groupRef) arn() aws.ARN {
	return aws.ARN{
		Partition: g.scope.Partition.String(),
		Service:   "logs",
		Region:    g.scope.Region.String(),
		Account:   g.scope.Account.String(),
		Resource:  "log-group:" + g.name,
	}
}

// wildcardARN is the group's ARN with the trailing ":*" that
// DescribeLogGroups' arn member carries.
func (g groupRef) wildcardARN() string { return g.arn().String() + ":*" }

// streamARN is the ARN of the group's stream name.
func (g groupRef) streamARN(name string) aws.ARN {
	a := g.arn()
	a.Resource += ":log-stream:" + name
	return a
}

// groupOfARN reads a log group ARN back into its scope and name. The model
// patterns of logGroupIdentifier and resourceArn admit no "*", so the form
// with the trailing ":*" never arrives here.
func groupOfARN(raw string) (groupRef, bool) {
	a, err := aws.ParseARN(raw)
	if err != nil || a.Service != "logs" {
		return groupRef{}, false
	}
	name, ok := strings.CutPrefix(a.Resource, "log-group:")
	if !ok || name == "" || strings.Contains(name, ":") {
		return groupRef{}, false
	}
	scope, err := aws.ScopeOf(a)
	if err != nil {
		return groupRef{}, false
	}
	return groupRef{scope: scope, name: name}, true
}

// named is the group a logGroupName names, in the caller's scope.
func named(c *kernel.Call, name string) groupRef { return groupRef{scope: c.Scope(), name: name} }

// resolveIdentifier is the group a logGroupIdentifier names: a name in the
// caller's scope or an ARN of a group in it. An ARN of another account or
// region names no group this caller can reach.
func resolveIdentifier(c *kernel.Call, id string) (groupRef, *awserr.Error) {
	if !strings.HasPrefix(id, "arn:") {
		return named(c, id), nil
	}
	g, ok := groupOfARN(id)
	if !ok {
		return groupRef{}, errGroupNotFound()
	}
	if _, err := kernel.ResolveScope(c, kernel.SameScope{ARN: g.arn(), Mismatch: errGroupNotFound()}); err != nil {
		return groupRef{}, err
	}
	return g, nil
}

// resolveGroup is the group of an operation that takes logGroupName or
// logGroupIdentifier. The operation's check refuses an input with both
// before this runs.
func resolveGroup(c *kernel.Call, name, identifier *string) (groupRef, *awserr.Error) {
	switch {
	case name != nil:
		return named(c, *name), nil
	case identifier != nil:
		return resolveIdentifier(c, *identifier)
	}
	return groupRef{}, errNoGroup()
}

// groupTagged names the group a tagging input's resourceArn addresses as
// the call's resource, before fault rules run, when the ARN parses as a
// log group's; any other ARN names nothing.
func groupTagged[In any](arnOf func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(_ *kernel.Call, in *In) []aws.ARN {
		ref, ok := groupOfARN(arnOf(in))
		if !ok {
			return nil
		}
		return []aws.ARN{ref.arn()}
	})
}

// groupIdentified names the resource an input addresses by logGroupName
// or logGroupIdentifier as the call's resource, before fault rules run:
// arnOf picks the group's ARN or one of its streams'. An input naming no
// group the caller can reach names nothing.
func groupIdentified[In any](groupOf func(*In) (name, identifier *string), arnOf func(groupRef, *In) aws.ARN) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		name, identifier := groupOf(in)
		ref, err := resolveGroup(c, name, identifier)
		if err != nil {
			return nil
		}
		return []aws.ARN{arnOf(ref, in)}
	})
}
