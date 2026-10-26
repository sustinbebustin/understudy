package sts

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Session lengths of AssumeRole (iam-sts-kms.md 4.2).
const (
	// defaultRoleSession is the session of a request without
	// DurationSeconds: one hour.
	defaultRoleSession = time.Hour
	// maxChainedSession caps a session that a role session assumes.
	maxChainedSession = time.Hour
	// maxRoleSession is the largest MaxSessionDuration a role can have.
	maxRoleSession = 43200 * time.Second
)

// assumed is the role an AssumeRole request names, as the session needs
// it.
type assumed struct {
	arn     aws.ARN
	account aws.AccountID
	role    identity.RoleRef
	// maxSession is the role's MaxSessionDuration.
	maxSession time.Duration
}

// parseRoleARN reads the RoleArn of a request:
// arn:aws:iam::<account>:role<path><name>.
func parseRoleARN(s string) (assumed, *awserr.Error) {
	arn, err := aws.ParseARN(s)
	if err != nil || arn.Partition != aws.PartitionAWS().String() || arn.Service != "iam" || arn.Region != "" {
		return assumed{}, errInvalidRoleARN(s)
	}
	account, err := aws.ParseAccountID(arn.Account)
	rest, ok := strings.CutPrefix(arn.Resource, "role/")
	if err != nil || !ok {
		return assumed{}, errInvalidRoleARN(s)
	}
	path, name := aws.SplitIAMPath(rest)
	if !aws.ValidIAMName(name) || !aws.ValidIAMPath(path) {
		return assumed{}, errInvalidRoleARN(s)
	}
	return assumed{arn: arn, account: account, role: identity.RoleRef{Name: name, Path: path}}, nil
}

// assumeRole mints a role session: ASIA credentials that expire on wall
// time, and the session's identity, which GetCallerIdentity signed with
// them renders.
func (s *Service) assumeRole(ctx context.Context, c *kernel.Call, in *stsapi.AssumeRoleInput) (*stsapi.AssumeRoleOutput, *awserr.Error) {
	// A role session assuming a role is role chaining.
	chained := false
	var caller *identity.SessionAttributes
	switch p := c.Principal().(type) {
	case identity.Root, identity.User:
	case identity.AssumedRole:
		chained, caller = true, p.Attributes
	case identity.Anonymous:
		return nil, errUnsigned()
	case identity.ServicePrincipal:
		return nil, awserr.Internalf(&noAccountError{op: "AssumeRole", principal: p.Name})
	}
	target, err := parseRoleARN(in.RoleArn)
	if err != nil {
		return nil, err
	}
	if err := checkSession(in); err != nil {
		return nil, err
	}
	if target, err = s.resolve(ctx, c, target); err != nil {
		return nil, err
	}
	duration := defaultRoleSession
	if in.DurationSeconds != nil {
		duration = time.Duration(*in.DurationSeconds) * time.Second
	}
	if chained && duration > maxChainedSession {
		return nil, errChainedSession()
	}
	if duration > target.maxSession {
		return nil, errOverMaxSession()
	}
	session := identity.AssumedRole{
		Account: target.account, Role: target.role, Session: in.RoleSessionName, Source: identity.FromAssumeRole{},
		Attributes: sessionAttributes(c.Admitted(), in, caller),
	}
	creds, err := s.mint(session, duration)
	if err != nil {
		return nil, err
	}
	c.Touch(target.arn)
	return &stsapi.AssumeRoleOutput{
		Credentials: creds,
		AssumedRoleUser: &stsapi.AssumedRoleUser{
			AssumedRoleId: target.role.ID + ":" + in.RoleSessionName,
			Arn:           identity.Render(session),
		},
		SourceIdentity: in.SourceIdentity,
	}, nil
}

// sessionAttributes are what a session keeps of its AssumeRole request for
// IAM evaluation (iam-evaluation.md 2.7, 8.1): the session policies, the
// session tags, the transitive tag keys, the source identity and the issue
// time. A session a role session assumes inherits caller's transitive tags
// whose keys the request does not set, compared regardless of case, and
// its source identity when the request sets none ("by default tags do not
// pass to the next session; transitive tags do", 2.7). Whether AWS refuses
// a request that sets a tag key or source identity the caller passes on is
// unrecorded; the request's value is kept.
func sessionAttributes(issued clock.Instant, in *stsapi.AssumeRoleInput, caller *identity.SessionAttributes) *identity.SessionAttributes {
	a := &identity.SessionAttributes{Issued: issued, TransitiveTagKeys: slices.Clone(in.TransitiveTagKeys)}
	if in.Policy != nil {
		a.Policy = *in.Policy
	}
	for _, p := range in.PolicyArns {
		if p.Arn != nil {
			a.PolicyARNs = append(a.PolicyARNs, *p.Arn)
		}
	}
	for _, t := range in.Tags {
		a.Tags = append(a.Tags, identity.SessionTag{Key: t.Key, Value: t.Value})
	}
	if in.SourceIdentity != nil {
		a.SourceIdentity = *in.SourceIdentity
	}
	if caller == nil {
		return a
	}
	set := len(a.Tags)
	for _, t := range caller.Tags {
		if foldedIn(caller.TransitiveTagKeys, t.Key) && !slices.ContainsFunc(a.Tags[:set], func(r identity.SessionTag) bool { return strings.EqualFold(r.Key, t.Key) }) {
			a.Tags = append(a.Tags, t)
		}
	}
	for _, k := range caller.TransitiveTagKeys {
		if !foldedIn(a.TransitiveTagKeys, k) {
			a.TransitiveTagKeys = append(a.TransitiveTagKeys, k)
		}
	}
	if a.SourceIdentity == "" {
		a.SourceIdentity = caller.SourceIdentity
	}
	return a
}

// foldedIn reports whether keys holds key, regardless of case: session tag
// keys are not case sensitive (AssumeRole API reference, Tags).
func foldedIn(keys []string, key string) bool {
	return slices.ContainsFunc(keys, func(k string) bool { return strings.EqualFold(k, key) })
}

// resolve reads the role from IAM in the role's account: its id and its
// MaxSessionDuration. A role IAM does not hold is AccessDenied, as the
// trust check of a missing role is in AWS, unless the iam.assumeRole switch
// is lenient.
func (s *Service) resolve(ctx context.Context, c *kernel.Call, target assumed) (assumed, *awserr.Error) {
	scope := c.Scope()
	scope.Account = target.account
	out, err := kernel.Invoke(ctx, s.k, c, iamapi.OpResolveRole,
		kernel.Target{Scope: scope, As: kernel.AsCaller{}, Via: ref.STSAssumeRole},
		&iamapi.ResolveRoleInput{RoleArn: target.arn.String()})
	switch {
	case err == nil:
		target.role.ID = out.Role.RoleId
		target.maxSession = maxRoleSession
		if out.Role.MaxSessionDuration != nil {
			target.maxSession = time.Duration(*out.Role.MaxSessionDuration) * time.Second
		}
		return target, nil
	case err.ID.Shape != iamapi.IdentityNoSuchEntityException().Shape:
		return assumed{}, err
	case s.opts.AssumeRole == RoleLenient:
		target.role.ID = lenientRoleID(target.arn)
		target.maxSession = maxRoleSession
		return target, nil
	default:
		return assumed{}, errNotAuthorized(identity.Render(c.Principal()), target.arn.String())
	}
}

// lenientRoleID is the role id of a session of a role IAM does not hold:
// AROA and 17 base32 characters derived from the ARN, so every session of
// the role shows the same id.
func lenientRoleID(arn aws.ARN) string {
	sum := sha256.Sum256([]byte(arn.String()))
	return "AROA" + base32.StdEncoding.EncodeToString(sum[:])[:17]
}
