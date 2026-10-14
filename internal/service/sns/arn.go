package sns

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// topicRef names a topic: the scope it lives in and its name.
type topicRef struct {
	scope aws.Scope
	name  string
}

func (t topicRef) arn() aws.ARN {
	return aws.ARN{
		Partition: t.scope.Partition.String(),
		Service:   "sns",
		Region:    t.scope.Region.String(),
		Account:   t.scope.Account.String(),
		Resource:  t.name,
	}
}

// fifo reports a FIFO topic, whose name ends in .fifo.
func (t topicRef) fifo() bool { return strings.HasSuffix(t.name, fifoSuffix) }

// subscriptionRef names a subscription: its topic and the id ending its ARN.
type subscriptionRef struct {
	topic topicRef
	id    string
}

func (s subscriptionRef) arn() aws.ARN {
	a := s.topic.arn()
	a.Resource += ":" + s.id
	return a
}

// parseARN parses the SNS ARN in member, answering AWS's texts: the count
// of colon-separated elements when there are fewer than six (sns.md 1),
// else "Invalid parameter: <member>".
func parseARN(member, s string) (aws.ARN, *awserr.Error) {
	if n := strings.Count(s, ":") + 1; n < 6 {
		return aws.ARN{}, errARNElements(member, n)
	}
	a, err := aws.ParseARN(s)
	if err != nil || a.Service != "sns" {
		return aws.ARN{}, errInvalidParameter(member)
	}
	return a, nil
}

// resolve returns the scope an SNS ARN in member names. A resource in
// another region answers "Invalid parameter: <member>" through
// kernel.ResolveScope (sns.md 1, AWS-recorded for topics); one in another
// account of the caller's region resolves there, since IAM and topic
// policies are not enforced.
func resolve(c *kernel.Call, member string, a aws.ARN) (aws.Scope, *awserr.Error) {
	account, err := aws.ParseAccountID(a.Account)
	if err != nil {
		return aws.Scope{}, errInvalidParameter(member)
	}
	own := c.Scope()
	var r kernel.ScopeRef = kernel.CrossAccount{Account: account}
	if a.Region != own.Region.String() || a.Partition != own.Partition.String() {
		r = kernel.SameScope{ARN: a, Mismatch: errInvalidParameter(member)}
	}
	return kernel.ResolveScope(c, r)
}

// resolveTopic resolves the topic ARN in member.
func resolveTopic(c *kernel.Call, member, s string) (topicRef, *awserr.Error) {
	a, err := parseARN(member, s)
	if err != nil {
		return topicRef{}, err
	}
	if strings.Contains(a.Resource, ":") {
		return topicRef{}, errInvalidParameter(member)
	}
	scope, err := resolve(c, member, a)
	if err != nil {
		return topicRef{}, err
	}
	return topicRef{scope: scope, name: a.Resource}, nil
}

// resolveSubscription resolves the subscription ARN in SubscriptionArn. A
// six-element ARN without a subscription id is "Invalid parameter:
// SubscriptionId" (sns.md 3.2).
func resolveSubscription(c *kernel.Call, s string) (subscriptionRef, *awserr.Error) {
	const member = "SubscriptionArn"
	a, err := parseARN(member, s)
	if err != nil {
		return subscriptionRef{}, err
	}
	topic, id, ok := strings.Cut(a.Resource, ":")
	if !ok || id == "" || strings.Contains(id, ":") {
		return subscriptionRef{}, errInvalidParameter("SubscriptionId")
	}
	scope, err := resolve(c, member, a)
	if err != nil {
		return subscriptionRef{}, err
	}
	return subscriptionRef{topic: topicRef{scope: scope, name: topic}, id: id}, nil
}

// subscriptionOf parses a subscription ARN a link names. Links are
// unsigned, so the ARN alone says where the subscription lives; false means
// no scope with SNS state matches it.
func (s *Service) subscriptionOf(arn string) (subscriptionRef, bool) {
	a, err := aws.ParseARN(arn)
	if err != nil || a.Service != "sns" {
		return subscriptionRef{}, false
	}
	topic, id, ok := strings.Cut(a.Resource, ":")
	if !ok || id == "" {
		return subscriptionRef{}, false
	}
	scope, ok := s.scopeOf(a.Partition, a.Account, a.Region)
	return subscriptionRef{topic: topicRef{scope: scope, name: topic}, id: id}, ok
}

// topicOf parses a topic ARN a link names, as subscriptionOf does.
func (s *Service) topicOf(arn string) (topicRef, bool) {
	a, err := aws.ParseARN(arn)
	if err != nil || a.Service != "sns" || strings.Contains(a.Resource, ":") {
		return topicRef{}, false
	}
	scope, ok := s.scopeOf(a.Partition, a.Account, a.Region)
	return topicRef{scope: scope, name: a.Resource}, ok
}

// scopeOf returns the scope with SNS state whose partition, account and
// region are spelled as given. Matching existing cells rather than parsing
// the region accepts configured extra regions and never creates a cell.
func (s *Service) scopeOf(partition, account, region string) (aws.Scope, bool) {
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		if scope.Partition.String() == partition && scope.Account.String() == account && scope.Region.String() == region {
			return scope, true
		}
	}
	return aws.Scope{}, false
}
