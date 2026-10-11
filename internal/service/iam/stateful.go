package iam

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// Name returns "iam".
func (*Service) Name() ref.ServiceID { return "iam" }

// SchemaVersion returns the version of State.
func (*Service) SchemaVersion() int { return stateVersion }

// Export serializes every non-empty account to one section.
func (s *Service) Export() ([]store.Section, error) {
	return s.cells.Export(store.AccountKeys(), stateVersion)
}

// Import loads sections written by Export of this schema version.
func (s *Service) Import(ss []store.Section) error {
	return s.cells.Import(store.AccountKeys(), stateVersion, ss)
}

// Reset drops the accounts rs covers, IAM having no regions, and revokes
// the access keys of their users, so no key resolves to a user that no
// longer exists. It runs under the gate's exclusive side, where no cell can
// be read, so it finds the keys through the cells' sections. A codec
// failure there is a bug in this package; Reset then logs it and drops the
// accounts anyway, leaving their users' keys valid.
func (s *Service) Reset(rs kernel.ResetScope) {
	covered := func(k aws.AccountID) bool { return rs.Covers(aws.Scope{Partition: aws.PartitionAWS(), Account: k}) }
	keys, err := s.accessKeys(covered)
	s.cells.Reset(func(k aws.AccountID) bool { return !covered(k) })
	if err != nil {
		s.log.LogAttrs(context.Background(), slog.LevelError, "iam reset could not read the users of the accounts it dropped; their access keys stay valid and resolve to users that no longer exist until the server restarts",
			slog.String("error", err.Error()))
		return
	}
	for _, k := range keys {
		s.identity.Revoke(string(k))
	}
}

// accessKeys lists the access keys of the users of every account covered
// selects. The caller holds the gate's exclusive side.
func (s *Service) accessKeys(covered func(aws.AccountID) bool) ([]ids.AccessKeyID, error) {
	sections, err := s.cells.Export(store.AccountKeys(), stateVersion)
	if err != nil {
		return nil, err
	}
	var keys []ids.AccessKeyID
	for _, sec := range sections {
		account, err := aws.ParseAccountID(strings.TrimSuffix(sec.Path, ".json"))
		if err != nil {
			return nil, fmt.Errorf("iam section %q names no account: %w", sec.Path, err)
		}
		if !covered(account) {
			continue
		}
		st := newState(account)
		if err := s.codec.Unmarshal(sec.Data, &st); err != nil {
			return nil, fmt.Errorf("decoding iam section %q: %w", sec.Path, err)
		}
		for name := range st.Users {
			for _, k := range st.Users[name].AccessKeys {
				keys = append(keys, k.ID)
			}
		}
	}
	return keys, nil
}

// Rearm does nothing: the state arms no timers.
func (*Service) Rearm(clock.Instant) {}

// Resources lists the roles, users, managed policies and instance profiles
// of the scope's account, each kind by name.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope.Account)
	if !ok {
		return nil
	}
	account := scope.Account
	return store.Read(cell, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Roles)+len(st.Users)+len(st.Policies)+len(st.InstanceProfiles))
		for _, k := range slices.Sorted(maps.Keys(st.Roles)) {
			r := st.Roles[k]
			out = append(out, kernel.Resource{ARN: roleARN(account, r.Path, r.Name), Summary: count(len(r.Attached), "attached policy", "attached policies")})
		}
		for _, k := range slices.Sorted(maps.Keys(st.Users)) {
			u := st.Users[k]
			out = append(out, kernel.Resource{ARN: userARN(account, u.Path, u.Name), Summary: count(len(u.AccessKeys), "access key", "access keys")})
		}
		for _, k := range slices.Sorted(maps.Keys(st.Policies)) {
			p := st.Policies[k]
			out = append(out, kernel.Resource{ARN: localPolicyARN(account, p.Path, p.Name), Summary: "default " + p.Default})
		}
		for _, k := range slices.Sorted(maps.Keys(st.InstanceProfiles)) {
			p := st.InstanceProfiles[k]
			summary := "no role"
			if r, ok := st.Roles[p.Role]; ok {
				summary = "role " + r.Name
			}
			out = append(out, kernel.Resource{ARN: instanceProfileARN(account, p.Path, p.Name), Summary: summary})
		}
		return out
	})
}

// Scopes returns the accounts that hold a state cell, each with a zero
// region; IAM has no regions.
func (s *Service) Scopes() []aws.Scope {
	accounts := s.cells.Keys(store.AccountKeys())
	out := make([]aws.Scope, len(accounts))
	for i, a := range accounts {
		out[i] = aws.Scope{Partition: aws.PartitionAWS(), Account: a}
	}
	return out
}

// count renders n with the noun's singular or plural form.
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
