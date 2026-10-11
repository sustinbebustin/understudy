package kms

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindAliases binds the alias operations. Terraform reads an alias only
// through ListAliases (tf-storage.md 4.2).
func (s *Service) bindAliases(b kmsapi.Binder) {
	b.CreateAlias(s.createAlias, registry.Full{}, kernel.WithCheck(func(in *kmsapi.CreateAliasInput) *awserr.Error {
		return checkAliasName(in.AliasName)
	}))
	b.UpdateAlias(s.updateAlias, registry.Full{})
	b.DeleteAlias(s.deleteAlias, registry.Full{})
	b.ListAliases(s.listAliases, registry.Full{})
}

// checkAliasName answers a customer alias without the alias/ prefix or in
// the alias/aws/ space AWS reserves for its managed keys.
func checkAliasName(name string) *awserr.Error {
	switch {
	case !strings.HasPrefix(name, "alias/"):
		return errAliasPrefix()
	case strings.HasPrefix(name, "alias/aws/"):
		return errReservedAlias()
	}
	return nil
}

// isAliasForm reports a TargetKeyId that names an alias, which an alias
// cannot point at.
func isAliasForm(id string) bool {
	return strings.HasPrefix(id, "alias/") || strings.HasPrefix(id, "arn:") && strings.Contains(id, ":alias/")
}

func (s *Service) createAlias(_ context.Context, c *kernel.Call, in *kmsapi.CreateAliasInput) (*kmsapi.CreateAliasOutput, *awserr.Error) {
	r, err := s.aliasTarget(c, in.TargetKeyId)
	if err != nil {
		return nil, err
	}
	_, err = withKey(s, c, r, notPending, func(st *State, k Key, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if err := customerOnly(k, keyARN(r.scope(), k.ID)); err != nil {
			return struct{}{}, err
		}
		if _, ok := st.Aliases[in.AliasName]; ok {
			return struct{}{}, errAliasExists(aliasARN(r.scope(), in.AliasName))
		}
		st.Aliases[in.AliasName] = Alias{Name: in.AliasName, KeyID: k.ID, Created: now, Updated: now}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &kmsapi.CreateAliasOutput{}, nil
}

// updateAlias points an existing alias at another key. The key it leaves may
// be in any state; the key it moves to may not be pending deletion.
func (s *Service) updateAlias(_ context.Context, c *kernel.Call, in *kmsapi.UpdateAliasInput) (*kmsapi.UpdateAliasOutput, *awserr.Error) {
	if err := checkAliasName(in.AliasName); err != nil {
		return nil, err
	}
	r, err := s.aliasTarget(c, in.TargetKeyId)
	if err != nil {
		return nil, err
	}
	_, err = withKey(s, c, r, aliasDestination, func(st *State, k Key, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if err := customerOnly(k, keyARN(r.scope(), k.ID)); err != nil {
			return struct{}{}, err
		}
		a, ok := st.Aliases[in.AliasName]
		if !ok {
			return struct{}{}, errAliasNotFound(aliasARN(r.scope(), in.AliasName))
		}
		a.KeyID, a.Updated = k.ID, now
		st.Aliases[in.AliasName] = a
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &kmsapi.UpdateAliasOutput{}, nil
}

// aliasTarget reads a TargetKeyId: a key id or key ARN in the caller's own
// account and Region, since an alias and its key share both.
func (s *Service) aliasTarget(c *kernel.Call, id string) (keyByID, *awserr.Error) {
	if isAliasForm(id) {
		return keyByID{}, errAliasTarget()
	}
	r, err := parseKeyID(c, id)
	if err != nil {
		return keyByID{}, err
	}
	if r.scope() != c.Scope() {
		return keyByID{}, errInvalidKeyID(id)
	}
	return r, nil
}

// deleteAlias deletes a customer alias; the alias/aws/ aliases belong to
// AWS managed keys and stay.
func (s *Service) deleteAlias(_ context.Context, c *kernel.Call, in *kmsapi.DeleteAliasInput) (*kmsapi.DeleteAliasOutput, *awserr.Error) {
	scope := c.Scope()
	if strings.HasPrefix(in.AliasName, "alias/aws/") {
		return nil, errAWSManagedAlias(aliasARN(scope, in.AliasName))
	}
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Aliases[in.AliasName]; !ok {
			return struct{}{}, errAliasNotFound(aliasARN(scope, in.AliasName))
		}
		delete(st.Aliases, in.AliasName)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &kmsapi.DeleteAliasOutput{}, nil
}

// listAliases lists the caller's aliases by name, or those of the one key
// KeyId names (a key id or key ARN). TargetKeyId is always the bare key id
// (tf-storage.md 4.2).
func (s *Service) listAliases(_ context.Context, c *kernel.Call, in *kmsapi.ListAliasesInput) (*kmsapi.ListAliasesOutput, *awserr.Error) {
	scope := c.Scope()
	keep := func(Alias) bool { return true }
	if in.KeyId != nil {
		k, err := readKey(s, c, *in.KeyId, keyOnly, ownAccount)
		if err != nil {
			return nil, err
		}
		scope = k.scope
		keep = func(a Alias) bool { return a.KeyID == k.ID }
	}
	aliases := readState(s, scope, func(st *State) []Alias {
		out := make([]Alias, 0, len(st.Aliases))
		for _, name := range slices.Sorted(maps.Keys(st.Aliases)) {
			if a := st.Aliases[name]; keep(a) {
				out = append(out, a)
			}
		}
		return out
	})
	p, err := page(aliases, func(a Alias) string { return a.Name }, in.Limit, limits{def: 50, max: 100}, in.Marker)
	if err != nil {
		return nil, err
	}
	out := &kmsapi.ListAliasesOutput{Aliases: make([]kmsapi.AliasListEntry, 0, len(p.items)), NextMarker: p.next, Truncated: p.truncated}
	for _, a := range p.items {
		out.Aliases = append(out.Aliases, kmsapi.AliasListEntry{
			AliasName:       new(a.Name),
			AliasArn:        new(aliasARN(scope, a.Name)),
			TargetKeyId:     new(a.KeyID),
			CreationDate:    new(a.Created.Wire()),
			LastUpdatedDate: new(a.Updated.Wire()),
		})
	}
	return out, nil
}
