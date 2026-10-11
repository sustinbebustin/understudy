package iam

import (
	"context"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindAccessKeys binds the access keys of users. The identity store mints
// each key, so a request signed with it runs as the user.
func (s *Service) bindAccessKeys(b iamapi.Binder) {
	b.CreateAccessKey(s.createAccessKey, registry.Partial{Note: "only for IAM users: a root caller must name a user"})
	b.ListAccessKeys(s.listAccessKeys, registry.Partial{Note: "only for IAM users: a root caller must name a user"})
	b.UpdateAccessKey(s.updateAccessKey, registry.StoredOnly{Note: "an Inactive key still authenticates, since phase 1 does not enforce IAM"})
	b.DeleteAccessKey(s.deleteAccessKey, registry.Full{})
}

// accessKeyIndex finds a user's access key by id.
func (u User) accessKeyIndex(id string) int {
	return slices.IndexFunc(u.AccessKeys, func(k AccessKey) bool { return string(k.ID) == id })
}

// principal is the identity a request signed with one of the user's keys
// runs as.
func (u User) principal(account aws.AccountID) identity.User {
	return identity.User{Account: account, Path: u.Path, Name: u.Name, ID: string(u.ID)}
}

// createAccessKey mints a long-term key for a user. The secret is in this
// response alone; IAM never returns it again.
func (s *Service) createAccessKey(_ context.Context, c *kernel.Call, in *iamapi.CreateAccessKeyInput) (*iamapi.CreateAccessKeyOutput, *awserr.Error) {
	name, err := callerUser(c, in.UserName)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	type created struct {
		user User
		key  iamapi.AccessKey
	}
	out, err := kernel.Write(c, s.cells, account, func(st *State, now clock.Instant, _ *kernel.Tx) (created, *awserr.Error) {
		u, err := st.user(name)
		if err != nil {
			return created{}, err
		}
		if len(u.AccessKeys) >= maxAccessKeys {
			return created{}, errQuota("AccessKeysPerUser", maxAccessKeys)
		}
		creds, merr := s.identity.MintLongTerm(u.principal(account))
		if merr != nil {
			return created{}, awserr.Internalf(merr)
		}
		k := AccessKey{ID: creds.AccessKeyID, Status: iamapi.StatusTypeActive, Created: now}
		u.AccessKeys = append(slices.Clone(u.AccessKeys), k)
		st.Users[key(u.Name)] = u
		return created{user: u, key: iamapi.AccessKey{
			UserName:        u.Name,
			AccessKeyId:     string(k.ID),
			Status:          k.Status,
			SecretAccessKey: string(creds.SecretAccessKey),
			CreateDate:      new(now.Wire()),
		}}, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(userARN(account, out.user.Path, out.user.Name))
	return &iamapi.CreateAccessKeyOutput{AccessKey: out.key}, nil
}

// listAccessKeys lists a user's keys by id. Every entry carries the user's
// name, which Terraform reads back (tf-remaining.md 3.2).
func (s *Service) listAccessKeys(_ context.Context, c *kernel.Call, in *iamapi.ListAccessKeysInput) (*iamapi.ListAccessKeysOutput, *awserr.Error) {
	name, err := callerUser(c, in.UserName)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	type listed struct {
		user User
		p    page[AccessKey]
	}
	l, err := view(s, account, func(st *State) (listed, *awserr.Error) {
		u, err := st.user(name)
		if err != nil {
			return listed{}, err
		}
		keys := slices.SortedFunc(slices.Values(u.AccessKeys), func(a, b AccessKey) int { return strings.Compare(string(a.ID), string(b.ID)) })
		p, err := paginate(keys, func(k AccessKey) string { return string(k.ID) }, in.Marker, in.MaxItems)
		return listed{user: u, p: p}, err
	})
	if err != nil {
		return nil, err
	}
	c.Touch(userARN(account, l.user.Path, l.user.Name))
	out := &iamapi.ListAccessKeysOutput{AccessKeyMetadata: make([]iamapi.AccessKeyMetadata, len(l.p.items)), IsTruncated: l.p.truncated, Marker: l.p.marker}
	for i, k := range l.p.items {
		out.AccessKeyMetadata[i] = iamapi.AccessKeyMetadata{
			UserName:    new(l.user.Name),
			AccessKeyId: new(string(k.ID)),
			Status:      new(k.Status),
			CreateDate:  new(k.Created.Wire()),
		}
	}
	return out, nil
}

// changeAccessKey applies change to one of a user's keys under the
// account's cell, or answers that the user has no such key.
func (s *Service) changeAccessKey(c *kernel.Call, userName *string, id string, change func(u User, i int) User) *awserr.Error {
	name, err := callerUser(c, userName)
	if err != nil {
		return err
	}
	account := c.Scope().Account
	u, err := kernel.Write(c, s.cells, account, func(st *State, _ clock.Instant, _ *kernel.Tx) (User, *awserr.Error) {
		u, err := st.user(name)
		if err != nil {
			return User{}, err
		}
		i := u.accessKeyIndex(id)
		if i < 0 {
			return User{}, errNoSuchAccessKey(id)
		}
		u = change(u, i)
		st.Users[key(u.Name)] = u
		return u, nil
	})
	if err != nil {
		return err
	}
	c.Touch(userARN(account, u.Path, u.Name))
	return nil
}

func (s *Service) updateAccessKey(_ context.Context, c *kernel.Call, in *iamapi.UpdateAccessKeyInput) (*iamapi.UpdateAccessKeyOutput, *awserr.Error) {
	return unit[iamapi.UpdateAccessKeyOutput](s.changeAccessKey(c, in.UserName, in.AccessKeyId, func(u User, i int) User {
		u.AccessKeys = slices.Clone(u.AccessKeys)
		u.AccessKeys[i].Status = in.Status
		return u
	}))
}

// deleteAccessKey deletes a key and revokes it, so a request signed with
// it runs as the default account's root from then on.
func (s *Service) deleteAccessKey(_ context.Context, c *kernel.Call, in *iamapi.DeleteAccessKeyInput) (*iamapi.DeleteAccessKeyOutput, *awserr.Error) {
	return unit[iamapi.DeleteAccessKeyOutput](s.changeAccessKey(c, in.UserName, in.AccessKeyId, func(u User, i int) User {
		s.identity.Revoke(in.AccessKeyId)
		u.AccessKeys = slices.Delete(slices.Clone(u.AccessKeys), i, i+1)
		if len(u.AccessKeys) == 0 {
			u.AccessKeys = nil
		}
		return u
	}))
}
