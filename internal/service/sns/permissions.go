package sns

import (
	"context"
	"path"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindPermissions binds AddPermission and RemovePermission, which edit the
// topic's stored Policy; IAM is not enforced.
func (s *Service) bindPermissions(b snsapi.Binder) {
	const note = "statements are stored in the topic Policy; not enforced"
	b.AddPermission(s.addPermission, registry.StoredOnly{Note: note},
		arnResource(func(in *snsapi.AddPermissionInput) string { return in.TopicArn }))
	b.RemovePermission(s.removePermission, registry.StoredOnly{Note: note},
		arnResource(func(in *snsapi.RemovePermissionInput) string { return in.TopicArn }))
}

// errActionOutOfScope is the detail for a policy action SNS does not
// serve, AWS-recorded at AddPermission (sns.md 2.3) and at CreateTopic
// (ballerina topics-tests.bal).
const errActionOutOfScope = "Policy statement action out of service scope!"

// permissionActions are the actions AddPermission may grant (sns.md 2.3).
func permissionActions() []string {
	return []string{
		"GetTopicAttributes", "SetTopicAttributes", "AddPermission", "RemovePermission", "DeleteTopic",
		"Subscribe", "ListSubscriptionsByTopic", "Publish", "Receive",
	}
}

// topicPolicyActions are the actions a topic Policy may name: those
// AddPermission grants and those the SNS policy reference adds
// (https://docs.aws.amazon.com/sns/latest/dg/sns-access-policy-language-api-permissions-reference.html).
func topicPolicyActions() []string {
	return append(permissionActions(), "GetDataProtectionPolicy", "PutDataProtectionPolicy", "ListTagsForResource")
}

// inServiceScope reports whether every Action and NotAction of doc names
// SNS topic actions: "*", or "sns:" in any case followed by an action of
// topicPolicyActions or a * and ? pattern that matches one. Accepting a
// pattern that matches some action is inferred; the recorded refusal is of
// a misspelled action.
func inServiceScope(doc policydoc.Document) bool {
	for _, st := range doc.Statements {
		var patterns []string
		switch a := st.Action.(type) {
		case policydoc.Action:
			patterns = a
		case policydoc.NotAction:
			patterns = a
		}
		if slices.ContainsFunc(patterns, func(p string) bool { return !topicActionPattern(p) }) {
			return false
		}
	}
	return true
}

// topicActionPattern reports whether p is "*" or an sns: pattern that
// matches an action of topicPolicyActions.
func topicActionPattern(p string) bool {
	if p == "*" {
		return true
	}
	vendor, action, ok := strings.Cut(strings.ToLower(p), ":")
	if !ok || vendor != "sns" {
		return false
	}
	return slices.ContainsFunc(topicPolicyActions(), func(known string) bool {
		matched, err := path.Match(action, strings.ToLower(known))
		return err == nil && matched
	})
}

// addPermission appends a statement named Label that grants the accounts
// the actions (sns.md 2.3): one principal or action is a string, several
// an array. An AWSAccountId that is not a 12-digit account ID is refused
// with the text AWS-recorded in ballerina permission-tests.bal.
func (s *Service) addPermission(_ context.Context, c *kernel.Call, in *snsapi.AddPermissionInput) (*snsapi.AddPermissionOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "TopicArn", in.TopicArn)
	if err != nil {
		return nil, err
	}
	actions := make([]document.Value, 0, len(in.ActionName))
	for _, a := range in.ActionName {
		if !slices.Contains(permissionActions(), a) {
			return nil, errInvalidParameter(errActionOutOfScope)
		}
		actions = append(actions, document.String("SNS:"+a))
	}
	principals := make([]document.Value, 0, len(in.AWSAccountId))
	for _, raw := range in.AWSAccountId {
		account, err := aws.ParseAccountID(raw)
		if err != nil {
			return nil, errInvalidParameter("A provided account ID is not valid")
		}
		principals = append(principals, document.String(aws.RootARN(ref.scope.Partition, account.String()).String()))
	}
	statement := document.Map{
		{Key: "Sid", Value: document.String(in.Label)},
		{Key: "Effect", Value: document.String("Allow")},
		{Key: "Principal", Value: document.Map{{Key: "AWS", Value: oneOrMany(principals)}}},
		{Key: "Action", Value: oneOrMany(actions)},
		{Key: "Resource", Value: document.String(ref.arn().String())},
	}
	_, err = writeTopic(s, c, ref, func(t *Topic, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		policy, statements, ok := policyStatements(ref, t)
		if !ok {
			return struct{}{}, errInvalidParameter("Policy")
		}
		if slices.ContainsFunc(statements, func(v document.Value) bool { return sidOf(v) == in.Label }) {
			return struct{}{}, errInvalidParameter("Statement already exists")
		}
		return struct{}{}, storeStatements(t, policy, append(statements, statement))
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.AddPermissionOutput{}, nil
}

// removePermission removes the statement named Label; a label no
// statement has changes nothing (inferred: no recorded error).
func (s *Service) removePermission(_ context.Context, c *kernel.Call, in *snsapi.RemovePermissionInput) (*snsapi.RemovePermissionOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "TopicArn", in.TopicArn)
	if err != nil {
		return nil, err
	}
	_, err = writeTopic(s, c, ref, func(t *Topic, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		policy, statements, ok := policyStatements(ref, t)
		if !ok {
			return struct{}{}, errInvalidParameter("Policy")
		}
		kept := slices.DeleteFunc(statements, func(v document.Value) bool { return sidOf(v) == in.Label })
		return struct{}{}, storeStatements(t, policy, kept)
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.RemovePermissionOutput{}, nil
}

// oneOrMany is a single value as itself and several as an array.
func oneOrMany(vs []document.Value) document.Value {
	if len(vs) == 1 {
		return vs[0]
	}
	return document.List(vs)
}

// policyStatements parses t's Policy, the default when none is set, and
// returns it with its statements as a list; false means the stored policy
// is not a JSON object.
func policyStatements(ref topicRef, t *Topic) (document.Map, []document.Value, bool) {
	text, ok := t.Attributes[attrPolicy]
	if !ok {
		text = defaultPolicy(ref)
	}
	policy, ok := parseObject(text)
	if !ok {
		return nil, nil, false
	}
	switch v, _ := lookup(policy, "Statement"); v := v.(type) {
	case document.List:
		return policy, slices.Clone(v), true
	case document.Map:
		return policy, []document.Value{v}, true
	case document.Null, document.Bool, document.Number, document.String:
		// Not a statement or a list of them: the policy has none.
	}
	return policy, nil, true
}

// storeStatements writes policy back to t with statements in place of its
// Statement member.
func storeStatements(t *Topic, policy document.Map, statements []document.Value) *awserr.Error {
	text, ok := encode(set(policy, document.Entry{Key: "Statement", Value: document.List(statements)}))
	if !ok {
		return errInvalidParameter("Policy")
	}
	storedTopicAttribute(attrPolicy, text).apply(t)
	return nil
}

// sidOf returns a statement's Sid, "" when it has none.
func sidOf(v document.Value) string {
	m, ok := v.(document.Map)
	if !ok {
		return ""
	}
	return stringMember(m, "Sid")
}
