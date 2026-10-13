package kms

import (
	"context"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// maxAliasesPerKey is AWS's quota of aliases per KMS key ("Aliases per KMS
// key: 50", KMS resource quotas); the alias/aws/ aliases AWS creates do not
// count toward it.
const maxAliasesPerKey = 50

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

// checkAliasName answers a new customer alias without the alias/ prefix or
// anything after it (CreateAlias's pattern is ^alias/[a-zA-Z0-9/_-]+$), or
// in the alias/aws/ space AWS reserves for its managed keys.
func checkAliasName(name string) *awserr.Error {
	if err := checkAliasPrefix(name); err != nil {
		return err
	}
	if isAWSManagedAlias(name) {
		return errReservedAlias()
	}
	return nil
}

// checkAliasPrefix answers a name without the alias/ prefix or anything
// after it.
func checkAliasPrefix(name string) *awserr.Error {
	if !strings.HasPrefix(name, "alias/") || name == "alias/" {
		return errAliasPrefix()
	}
	return nil
}

// isAWSManagedAlias reports a name in the alias/aws/ space, where only the
// aliases of AWS managed keys live.
func isAWSManagedAlias(name string) bool {
	return strings.HasPrefix(name, "alias/aws/")
}

// isAliasForm reports a TargetKeyId that names an alias, which an alias
// cannot point at.
func isAliasForm(id string) bool {
	return strings.HasPrefix(id, "alias/") || strings.HasPrefix(id, "arn:") && strings.Contains(id, ":alias/")
}

// checkAliasQuota answers LimitExceededException when key keyID already has
// maxAliasesPerKey customer aliases other than name, the alias about to
// point at it.
func checkAliasQuota(st *State, keyID, name, keyARN string) *awserr.Error {
	n := 0
	for other, a := range st.Aliases {
		if a.KeyID == keyID && other != name && !isAWSManagedAlias(other) {
			n++
		}
	}
	if n >= maxAliasesPerKey {
		return errAliasQuota(keyARN, maxAliasesPerKey)
	}
	return nil
}

func (s *Service) createAlias(_ context.Context, c *kernel.Call, in *kmsapi.CreateAliasInput) (*kmsapi.CreateAliasOutput, *awserr.Error) {
	r, err := s.aliasTarget(c, in.TargetKeyId)
	if err != nil {
		return nil, err
	}
	_, err = withKey(s, c, r, notPending, func(st *State, k Key, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		arn := keyARN(r.scope(), k.ID).String()
		if err := customerOnly(k, arn); err != nil {
			return struct{}{}, err
		}
		if _, ok := st.Aliases[in.AliasName]; ok {
			return struct{}{}, errAliasExists(aliasARN(r.scope(), in.AliasName).String())
		}
		if err := checkAliasQuota(st, k.ID, in.AliasName, arn); err != nil {
			return struct{}{}, err
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
// be in any state; the key it moves to may not be pending deletion. An
// alias/aws/ name is not refused up front, since UpdateAlias models no
// InvalidAliasNameException: one that does not exist is NotFoundException
// like any other, and one that does is an AWS managed key's and stays put.
func (s *Service) updateAlias(_ context.Context, c *kernel.Call, in *kmsapi.UpdateAliasInput) (*kmsapi.UpdateAliasOutput, *awserr.Error) {
	if err := checkAliasPrefix(in.AliasName); err != nil {
		return nil, err
	}
	r, err := s.aliasTarget(c, in.TargetKeyId)
	if err != nil {
		return nil, err
	}
	_, err = withKey(s, c, r, aliasDestination, func(st *State, k Key, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		arn := keyARN(r.scope(), k.ID).String()
		if err := customerOnly(k, arn); err != nil {
			return struct{}{}, err
		}
		a, ok := st.Aliases[in.AliasName]
		if !ok {
			return struct{}{}, errAliasNotFound(aliasARN(r.scope(), in.AliasName).String())
		}
		if isAWSManagedAlias(in.AliasName) {
			return struct{}{}, errAWSManagedAlias(aliasARN(r.scope(), in.AliasName).String())
		}
		if err := checkAliasQuota(st, k.ID, in.AliasName, arn); err != nil {
			return struct{}{}, err
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

// deleteAlias deletes a customer alias. An existing alias/aws/ alias
// belongs to an AWS managed key and stays; one that does not exist is
// NotFoundException like any other name.
func (s *Service) deleteAlias(_ context.Context, c *kernel.Call, in *kmsapi.DeleteAliasInput) (*kmsapi.DeleteAliasOutput, *awserr.Error) {
	scope := c.Scope()
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Aliases[in.AliasName]; !ok {
			return struct{}{}, errAliasNotFound(aliasARN(scope, in.AliasName).String())
		}
		if isAWSManagedAlias(in.AliasName) {
			return struct{}{}, errAWSManagedAlias(aliasARN(scope, in.AliasName).String())
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
// (tf-storage.md 4.2). Without KeyId the listing also holds every predefined
// alias/aws/ alias whose AWS managed key does not exist yet, with only its
// name and ARN, as AWS lists them; Terraform's aws_kms_alias data source
// finds an alias only through this listing.
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
	entries := readState(s, scope, func(st *State) []kmsapi.AliasListEntry {
		out := make([]kmsapi.AliasListEntry, 0, len(st.Aliases))
		for _, a := range st.Aliases {
			if keep(a) {
				out = append(out, kmsapi.AliasListEntry{
					AliasName:       new(a.Name),
					AliasArn:        new(aliasARN(scope, a.Name).String()),
					TargetKeyId:     new(a.KeyID),
					CreationDate:    new(a.Created.Wire()),
					LastUpdatedDate: new(a.Updated.Wire()),
				})
			}
		}
		if in.KeyId == nil {
			for _, m := range managedKeys() {
				if _, ok := st.Aliases[m.alias]; !ok {
					out = append(out, kmsapi.AliasListEntry{AliasName: new(m.alias), AliasArn: new(aliasARN(scope, m.alias).String())})
				}
			}
		}
		return out
	})
	name := func(e kmsapi.AliasListEntry) string { return deref(e.AliasName) }
	p, err := page(listingAliases, entries, name, in.Limit, limits{def: 50, max: 100}, in.Marker)
	if err != nil {
		return nil, err
	}
	return &kmsapi.ListAliasesOutput{Aliases: p.items, NextMarker: p.next, Truncated: p.truncated}, nil
}
