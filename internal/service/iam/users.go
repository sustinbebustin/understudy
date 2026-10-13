package iam

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindUsers binds the user control plane.
func (s *Service) bindUsers(b iamapi.Binder) {
	b.CreateUser(s.createUser, registry.Partial{Note: "as PermissionsBoundary, " + unknownAWSManaged})
	b.GetUser(s.getUser, registry.Partial{Note: "without UserName, a root caller gets its root identity with the Unix epoch as CreateDate, since account creation is not tracked"})
	b.UpdateUser(s.updateUser, registry.Partial{Note: "access keys minted before a rename or a path change still resolve to the user's old name and path"})
	b.DeleteUser(s.deleteUser, registry.Full{})
	b.ListUsers(s.listUsers, registry.Full{})
	b.PutUserPermissionsBoundary(s.putUserPermissionsBoundary, registry.Partial{Note: boundaryNote})
	b.DeleteUserPermissionsBoundary(s.deleteUserPermissionsBoundary, registry.Full{})
	b.ListGroupsForUser(s.listGroupsForUser, registry.Partial{Note: "groups are not implemented, so a user belongs to none"})
}

// user finds a user by name.
func (st *State) user(name string) (User, *awserr.Error) {
	u, ok := st.Users[key(name)]
	if !ok {
		return User{}, errNoSuchUser(name)
	}
	return u, nil
}

// subject is the user an operation that defaults UserName to the caller
// acts on.
//
//sumtype:decl
type subject interface{ isSubject() }

// namedUser is an IAM user of the account: the one the request names, or
// the one whose access key signed it.
type namedUser struct{ name string }

// accountRoot is the account's root user: the caller of a request signed
// with root credentials that names no user.
type accountRoot struct{}

func (namedUser) isSubject()   {}
func (accountRoot) isSubject() {}

// callerUser is the user an operation names, or the caller when it names
// none, as IAM defaults UserName to whoever signed the request. A role
// session, a service and an anonymous caller have no user to default to.
func callerUser(c *kernel.Call, name *string) (subject, *awserr.Error) {
	if name != nil {
		return namedUser{name: *name}, nil
	}
	switch p := c.Principal().(type) {
	case identity.User:
		return namedUser{name: p.Name}, nil
	case identity.Root:
		return accountRoot{}, nil
	case identity.AssumedRole, identity.ServicePrincipal, identity.Anonymous:
	}
	return nil, errNonUserCredentials()
}

func (s *Service) createUser(_ context.Context, c *kernel.Call, in *iamapi.CreateUserInput) (*iamapi.CreateUserOutput, *awserr.Error) {
	if err := checkPath("path", in.Path); err != nil {
		return nil, err
	}
	tags, err := newTags(foldedKeys, in.Tags)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	u := User{Name: in.UserName, Path: pathOrRoot(in.Path), Tags: tags}
	if in.PermissionsBoundary != nil {
		u.Boundary = *in.PermissionsBoundary
	}
	out, err := kernel.Write(c, s.cells, account, func(st *State, now clock.Instant, _ *kernel.Tx) (iamapi.User, *awserr.Error) {
		if _, ok := st.Users[key(u.Name)]; ok {
			return iamapi.User{}, errUserExists(u.Name)
		}
		if u.Boundary != "" {
			if err := st.boundary(account, u.Boundary); err != nil {
				return iamapi.User{}, err
			}
		}
		u.ID = s.ids.UserID()
		u.Created = now
		st.Users[key(u.Name)] = u
		return apiUserDetail(account, u), nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(userARN(account, u.Path, u.Name))
	return &iamapi.CreateUserOutput{User: &out}, nil
}

func (s *Service) getUser(_ context.Context, c *kernel.Call, in *iamapi.GetUserInput) (*iamapi.GetUserOutput, *awserr.Error) {
	who, err := callerUser(c, in.UserName)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	var name string
	switch who := who.(type) {
	case accountRoot:
		c.Touch(rootARN(account))
		return &iamapi.GetUserOutput{User: apiRootUser(account)}, nil
	case namedUser:
		name = who.name
	}
	u, err := view(s, account, func(st *State) (User, *awserr.Error) { return st.user(name) })
	if err != nil {
		return nil, err
	}
	c.Touch(userARN(account, u.Path, u.Name))
	return &iamapi.GetUserOutput{User: apiUserDetail(account, u)}, nil
}

// changeUser applies change to the named user under the account's cell
// and touches it.
func (s *Service) changeUser(c *kernel.Call, name string, change func(st *State, u User) (User, *awserr.Error)) *awserr.Error {
	account := c.Scope().Account
	u, err := kernel.Write(c, s.cells, account, func(st *State, _ clock.Instant, _ *kernel.Tx) (User, *awserr.Error) {
		u, err := st.user(name)
		if err != nil {
			return User{}, err
		}
		if u, err = change(st, u); err != nil {
			return User{}, err
		}
		st.Users[key(name)] = u
		return u, nil
	})
	if err != nil {
		return err
	}
	c.Touch(userARN(account, u.Path, u.Name))
	return nil
}

// updateUser renames a user or moves it to a new path.
func (s *Service) updateUser(_ context.Context, c *kernel.Call, in *iamapi.UpdateUserInput) (*iamapi.UpdateUserOutput, *awserr.Error) {
	if err := checkPath("newPath", in.NewPath); err != nil {
		return nil, err
	}
	account := c.Scope().Account
	u, err := kernel.Write(c, s.cells, account, func(st *State, _ clock.Instant, _ *kernel.Tx) (User, *awserr.Error) {
		u, err := st.user(in.UserName)
		if err != nil {
			return User{}, err
		}
		if in.NewUserName != nil && key(*in.NewUserName) != key(u.Name) {
			if _, ok := st.Users[key(*in.NewUserName)]; ok {
				return User{}, errUserExists(*in.NewUserName)
			}
		}
		delete(st.Users, key(u.Name))
		if in.NewUserName != nil {
			u.Name = *in.NewUserName
		}
		if in.NewPath != nil {
			u.Path = *in.NewPath
		}
		st.Users[key(u.Name)] = u
		return u, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(userARN(account, u.Path, u.Name))
	return &iamapi.UpdateUserOutput{}, nil
}

// deleteUser deletes a user that holds nothing: IAM removes nothing itself,
// so attached policies, inline policies and access keys each refuse the
// delete.
func (s *Service) deleteUser(_ context.Context, c *kernel.Call, in *iamapi.DeleteUserInput) (*iamapi.DeleteUserOutput, *awserr.Error) {
	account := c.Scope().Account
	u, err := kernel.Write(c, s.cells, account, func(st *State, _ clock.Instant, _ *kernel.Tx) (User, *awserr.Error) {
		u, err := st.user(in.UserName)
		if err != nil {
			return User{}, err
		}
		switch {
		case len(u.Attached) > 0:
			return User{}, errMustDetachPolicies()
		case len(u.Inline) > 0:
			return User{}, errMustDeletePolicies()
		case len(u.AccessKeys) > 0:
			return User{}, errMustDeleteAccessKeys()
		}
		delete(st.Users, key(u.Name))
		return u, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(userARN(account, u.Path, u.Name))
	return &iamapi.DeleteUserOutput{}, nil
}

func (s *Service) listUsers(_ context.Context, c *kernel.Call, in *iamapi.ListUsersInput) (*iamapi.ListUsersOutput, *awserr.Error) {
	account := c.Scope().Account
	p, err := view(s, account, func(st *State) (page[User], *awserr.Error) {
		users := sortedWhere(st.Users, func(u *User) bool { return underPrefix(u.Path, in.PathPrefix) })
		return paginate(users, func(u User) string { return key(u.Name) }, in.Marker, in.MaxItems)
	})
	if err != nil {
		return nil, err
	}
	out := &iamapi.ListUsersOutput{Users: make([]iamapi.User, len(p.items)), IsTruncated: p.truncated, Marker: p.marker}
	for i := range p.items {
		out.Users[i] = apiUser(account, p.items[i])
	}
	return out, nil
}

func (s *Service) putUserPermissionsBoundary(_ context.Context, c *kernel.Call, in *iamapi.PutUserPermissionsBoundaryInput) (*iamapi.PutUserPermissionsBoundaryOutput, *awserr.Error) {
	account := c.Scope().Account
	return unit[iamapi.PutUserPermissionsBoundaryOutput](s.changeUser(c, in.UserName, func(st *State, u User) (User, *awserr.Error) {
		if err := st.boundary(account, in.PermissionsBoundary); err != nil {
			return User{}, err
		}
		u.Boundary = in.PermissionsBoundary
		return u, nil
	}))
}

func (s *Service) deleteUserPermissionsBoundary(_ context.Context, c *kernel.Call, in *iamapi.DeleteUserPermissionsBoundaryInput) (*iamapi.DeleteUserPermissionsBoundaryOutput, *awserr.Error) {
	return unit[iamapi.DeleteUserPermissionsBoundaryOutput](s.changeUser(c, in.UserName, func(_ *State, u User) (User, *awserr.Error) {
		u.Boundary = ""
		return u, nil
	}))
}

// listGroupsForUser answers for an existing user with no groups, the call
// Terraform makes before every user delete (tf-remaining.md 3.1).
func (s *Service) listGroupsForUser(_ context.Context, c *kernel.Call, in *iamapi.ListGroupsForUserInput) (*iamapi.ListGroupsForUserOutput, *awserr.Error) {
	account := c.Scope().Account
	u, err := view(s, account, func(st *State) (User, *awserr.Error) { return st.user(in.UserName) })
	if err != nil {
		return nil, err
	}
	c.Touch(userARN(account, u.Path, u.Name))
	return &iamapi.ListGroupsForUserOutput{}, nil
}
