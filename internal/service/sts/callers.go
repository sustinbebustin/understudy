package sts

import (
	"context"
	"fmt"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// getCallerIdentity answers who signed the request, for every principal
// with an account (iam-sts-kms.md 4.1). It needs no permission, so it never
// fails for a signed request.
func (s *Service) getCallerIdentity(_ context.Context, c *kernel.Call, _ *stsapi.GetCallerIdentityInput) (*stsapi.GetCallerIdentityOutput, *awserr.Error) {
	var userID string
	var account aws.AccountID
	switch p := c.Principal().(type) {
	case identity.Root:
		userID, account = p.Account.String(), p.Account
	case identity.User:
		userID, account = p.ID, p.Account
	case identity.AssumedRole:
		userID, account = p.Role.ID+":"+p.Session, p.Account
	case identity.Anonymous:
		return nil, errUnsigned()
	case identity.ServicePrincipal:
		return nil, awserr.Internalf(&noAccountError{op: "GetCallerIdentity", principal: p.Name})
	}
	return &stsapi.GetCallerIdentityOutput{
		UserId:  new(userID),
		Account: new(account.String()),
		Arn:     new(identity.Render(c.Principal())),
	}, nil
}

// Session lengths of GetSessionToken (iam-sts-kms.md 4).
const (
	// defaultUserSession is the session of an IAM user that asks for no
	// length: twelve hours.
	defaultUserSession = 43200 * time.Second
	// maxRootSession caps a root user's session at one hour; a longer
	// request is shortened, not refused.
	maxRootSession = time.Hour
)

// getSessionToken mints temporary credentials for the user or root that
// signed the request; a role session cannot ask for one.
func (s *Service) getSessionToken(_ context.Context, c *kernel.Call, in *stsapi.GetSessionTokenInput) (*stsapi.GetSessionTokenOutput, *awserr.Error) {
	duration := defaultUserSession
	if in.DurationSeconds != nil {
		duration = time.Duration(*in.DurationSeconds) * time.Second
	}
	switch p := c.Principal().(type) {
	case identity.Root:
		duration = min(duration, maxRootSession)
	case identity.User:
	case identity.AssumedRole:
		return nil, errSessionCredentials()
	case identity.Anonymous:
		return nil, errUnsigned()
	case identity.ServicePrincipal:
		return nil, awserr.Internalf(&noAccountError{op: "GetSessionToken", principal: p.Name})
	}
	creds, err := s.mint(c.Principal(), duration)
	if err != nil {
		return nil, err
	}
	return &stsapi.GetSessionTokenOutput{Credentials: creds}, nil
}

// mint issues temporary credentials for p that last d of wall time:
// Expiration is wall time, which SDKs compare with their own clocks
// (iam-sts-kms.md implication 8).
func (s *Service) mint(p identity.Principal, d time.Duration) (*stsapi.Credentials, *awserr.Error) {
	creds, err := s.identity.MintTemporary(p, s.clock.Wall().Add(d))
	if err != nil {
		return nil, awserr.Internalf(err)
	}
	t, ok := creds.Lifetime.(identity.Temporary)
	if !ok {
		return nil, awserr.Internalf(&lifetimeError{got: creds.Lifetime})
	}
	return &stsapi.Credentials{
		AccessKeyId:     string(creds.AccessKeyID),
		SecretAccessKey: string(creds.SecretAccessKey),
		SessionToken:    string(t.SessionToken),
		Expiration:      t.Expiry,
	}, nil
}

// lifetimeError is temporary credentials the identity store minted without
// a session token and an expiry.
type lifetimeError struct{ got identity.Lifetime }

func (e *lifetimeError) Error() string {
	return fmt.Sprintf("identity.Store.MintTemporary returned a %T lifetime, want identity.Temporary", e.got)
}

// noAccountError is an internal call made as a service principal to an STS
// operation that answers for an account's principal: no internal path
// does that, so it is a bug.
type noAccountError struct {
	op, principal string
}

func (e *noAccountError) Error() string {
	return "sts." + e.op + " called as the service principal " + e.principal + ", which has no account; internal calls to STS run as the caller or an execution role"
}
