package sts

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
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
	switch p := c.Principal().(type) {
	case identity.Root, identity.User:
	case identity.AssumedRole:
		chained = true
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
	session := identity.AssumedRole{Account: target.account, Role: target.role, Session: in.RoleSessionName, Source: identity.FromAssumeRole{}}
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
