package iam

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/paging"
	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindPolicies binds managed policies and their versions.
func (s *Service) bindPolicies(b iamapi.Binder) {
	stored := registry.StoredOnly{Note: "the document is validated and stored, not enforced"}
	b.CreatePolicy(s.createPolicy, stored)
	b.GetPolicy(s.getPolicy, registry.Partial{Note: catalogueSource + "; Description is the guide's text, not recorded against IAM (iam-evaluation.md 10.5)"})
	b.DeletePolicy(s.deletePolicy, registry.Full{})
	b.ListPolicies(s.listPolicies, registry.Partial{Note: catalogueSource + "; with both kinds listed, policies are ordered by name regardless of case, then by ARN, an order no recording covers"})
	b.CreatePolicyVersion(s.createPolicyVersion, stored)
	b.GetPolicyVersion(s.getPolicyVersion, registry.Partial{Note: catalogueSource + "; the document is the guide's text and the version's CreateDate is the policy's UpdateDate, neither recorded against IAM (iam-evaluation.md 10.5)"})
	b.ListPolicyVersions(s.listPolicyVersions, registry.Partial{Note: catalogueSource + "; the version's CreateDate is the policy's UpdateDate, not recorded against IAM (iam-evaluation.md 10.5)"})
	b.DeletePolicyVersion(s.deletePolicyVersion, registry.Full{})
	b.SetDefaultPolicyVersion(s.setDefaultPolicyVersion, registry.Full{})
	b.ListEntitiesForPolicy(s.listEntitiesForPolicy, registry.Partial{Note: "groups are not implemented, so PolicyGroups is always empty"})
}

// localPolicy finds the customer managed policy arn names in account: an
// ARN that names no managed policy is InvalidInput, and an AWS managed
// policy, another account's or a missing one NoSuchEntity.
func (st *State) localPolicy(account aws.AccountID, arn string) (Policy, policyRef, *awserr.Error) {
	ref, ok := parsePolicyARN(arn)
	if !ok {
		return Policy{}, policyRef{}, errInvalidPolicyARN(arn)
	}
	if !ref.ownedBy(account) {
		return Policy{}, policyRef{}, errNoSuchPolicy(arn)
	}
	p, ok := st.Policies[key(ref.name)]
	if !ok || p.Name != ref.name || p.Path != ref.path {
		return Policy{}, policyRef{}, errNoSuchPolicy(arn)
	}
	return p, ref, nil
}

// checkAttachable checks a policy ARN a role or user attaches, which named
// resolved among the AWS managed policies: a catalogued AWS managed policy
// or a customer managed policy of the account. Text that is no ARN is
// ValidationError; an ARN that names no attachable policy, of whatever
// shape, NoSuchEntity.
func (st *State) checkAttachable(account aws.AccountID, arn string, named awsPolicy) *awserr.Error {
	if _, err := aws.ParseARN(arn); err != nil {
		return errUnparsableARN()
	}
	switch named.(type) {
	case catalogued:
		return nil
	case uncatalogued:
		return errNotAttachable(arn)
	case notAWSManaged:
	}
	if _, _, err := st.localPolicy(account, arn); err != nil {
		return errNotAttachable(arn)
	}
	return nil
}

// usage counts the roles and users that attach arn, and those that use it
// as their permissions boundary.
func (st *State) usage(arn string) (attached, boundaries int32) {
	tally := func(attachedTo []string, boundary string) {
		if slices.Contains(attachedTo, arn) {
			attached++
		}
		if boundary == arn {
			boundaries++
		}
	}
	for k := range st.Roles {
		tally(st.Roles[k].Attached, st.Roles[k].Boundary)
	}
	for k := range st.Users {
		tally(st.Users[k].Attached, st.Users[k].Boundary)
	}
	return attached, boundaries
}

// apiPolicy is a customer managed policy with its live usage counts.
func apiPolicy(st *State, account aws.AccountID, p Policy) iamapi.Policy {
	arn := localPolicyARN(account, p.Path, p.Name).String()
	attached, boundaries := st.usage(arn)
	out := iamapi.Policy{
		PolicyName:                    new(p.Name),
		PolicyId:                      new(string(p.ID)),
		Arn:                           new(arn),
		Path:                          new(p.Path),
		DefaultVersionId:              new(p.Default),
		AttachmentCount:               new(attached),
		PermissionsBoundaryUsageCount: new(boundaries),
		IsAttachable:                  true,
		CreateDate:                    new(p.Created.Wire()),
		UpdateDate:                    new(p.Updated.Wire()),
		Tags:                          apiTags(p.Tags),
	}
	if p.Description != "" {
		out.Description = new(p.Description)
	}
	return out
}

func (p Policy) version(id string) (PolicyVersion, bool) {
	i := slices.IndexFunc(p.Versions, func(v PolicyVersion) bool { return v.ID == id })
	if i < 0 {
		return PolicyVersion{}, false
	}
	return p.Versions[i], true
}

// sortKey orders versions newest first, for ListPolicyVersions: the
// version number, which IAM never reuses, subtracted from math.MaxInt64 and
// zero-padded to 19 digits. Being the version's identity rather than its
// position, it keeps a Marker valid across deletes between pages.
func (v PolicyVersion) sortKey() (string, *awserr.Error) {
	digits, ok := strings.CutPrefix(v.ID, "v")
	n, err := strconv.Atoi(digits)
	if !ok || err != nil || n < 1 {
		return "", awserr.Internalf(&versionIDError{id: v.ID})
	}
	return fmt.Sprintf("%019d", math.MaxInt64-n), nil
}

// versionIDError is a stored policy version whose id is not "v" and a
// positive number, which only a hand-edited snapshot can hold.
type versionIDError struct{ id string }

func (e *versionIDError) Error() string {
	return fmt.Sprintf("iam: stored policy version id %q is not v<n> with n >= 1; the policy's snapshot section was edited or corrupted, and its versions cannot be listed until the id is fixed", e.id)
}

func apiVersion(p Policy, v PolicyVersion, withDocument bool) iamapi.PolicyVersion {
	out := iamapi.PolicyVersion{VersionId: new(v.ID), IsDefaultVersion: v.ID == p.Default, CreateDate: new(v.Created.Wire())}
	if withDocument {
		out.Document = new(v.Document)
	}
	return out
}

func (s *Service) createPolicy(_ context.Context, c *kernel.Call, in *iamapi.CreatePolicyInput) (*iamapi.CreatePolicyOutput, *awserr.Error) {
	if err := checkManagedPolicy(in.PolicyDocument); err != nil {
		return nil, err
	}
	tags, err := newTags(exactKeys, in.Tags)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	p := Policy{Name: in.PolicyName, Path: pathOrRoot(in.Path), Tags: tags, Default: "v1", Next: 2}
	if in.Description != nil {
		p.Description = *in.Description
	}
	out, err := kernel.Write(c, s.cells, account, func(st *State, now clock.Instant, _ *kernel.Tx) (iamapi.Policy, *awserr.Error) {
		if _, ok := st.Policies[key(p.Name)]; ok {
			return iamapi.Policy{}, errPolicyExists(p.Name)
		}
		p.ID = s.ids.PolicyID()
		p.Created, p.Updated = now, now
		p.Versions = []PolicyVersion{{ID: "v1", Document: in.PolicyDocument, Created: now}}
		st.Policies[key(p.Name)] = p
		return apiPolicy(st, account, p), nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(localPolicyARN(account, p.Path, p.Name))
	return &iamapi.CreatePolicyOutput{Policy: &out}, nil
}

func (s *Service) getPolicy(_ context.Context, c *kernel.Call, in *iamapi.GetPolicyInput) (*iamapi.GetPolicyOutput, *awserr.Error) {
	named, err := s.lookup(in.PolicyArn)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	out, err := view(s, account, func(st *State) (iamapi.Policy, *awserr.Error) {
		switch named := named.(type) {
		case catalogued:
			return apiAWSManagedPolicy(st, named.policy), nil
		case uncatalogued:
			return iamapi.Policy{}, errNoSuchPolicy(in.PolicyArn)
		case notAWSManaged:
		}
		p, _, err := st.localPolicy(account, in.PolicyArn)
		if err != nil {
			return iamapi.Policy{}, err
		}
		return apiPolicy(st, account, p), nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(policyTouch(in.PolicyArn))
	return &iamapi.GetPolicyOutput{Policy: &out}, nil
}

// policyTouch is the ARN a policy operation touches, the one it names; a
// handler calls it only after resolving the ARN.
func policyTouch(arn string) aws.ARN {
	ref, _ := parsePolicyARN(arn)
	return ref.arn
}

// changePolicy applies change to the customer managed policy arn names,
// under the account's cell, and touches it.
func (s *Service) changePolicy(c *kernel.Call, arn string, change func(st *State, p Policy, now clock.Instant) (Policy, *awserr.Error)) (Policy, *awserr.Error) {
	account := c.Scope().Account
	p, err := kernel.Write(c, s.cells, account, func(st *State, now clock.Instant, _ *kernel.Tx) (Policy, *awserr.Error) {
		p, _, err := st.localPolicy(account, arn)
		if err != nil {
			return Policy{}, err
		}
		if p, err = change(st, p, now); err != nil {
			return Policy{}, err
		}
		st.Policies[key(p.Name)] = p
		return p, nil
	})
	if err != nil {
		return Policy{}, err
	}
	c.Touch(policyTouch(arn))
	return p, nil
}

// deletePolicy deletes a customer managed policy no role or user uses and
// that has no version besides the default, which goes with it.
func (s *Service) deletePolicy(_ context.Context, c *kernel.Call, in *iamapi.DeletePolicyInput) (*iamapi.DeletePolicyOutput, *awserr.Error) {
	account := c.Scope().Account
	_, err := kernel.Write(c, s.cells, account, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		p, _, err := st.localPolicy(account, in.PolicyArn)
		if err != nil {
			return struct{}{}, err
		}
		if attached, boundaries := st.usage(in.PolicyArn); attached+boundaries > 0 {
			return struct{}{}, errPolicyAttached()
		}
		if len(p.Versions) > 1 {
			return struct{}{}, errPolicyHasVersions()
		}
		delete(st.Policies, key(p.Name))
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(policyTouch(in.PolicyArn))
	return &iamapi.DeletePolicyOutput{}, nil
}

// listPolicies lists the account's customer managed policies (Scope Local),
// the catalogue's AWS managed policies (Scope AWS), or both (All, or no
// Scope), by name regardless of case, then by ARN.
func (s *Service) listPolicies(_ context.Context, c *kernel.Call, in *iamapi.ListPoliciesInput) (*iamapi.ListPoliciesOutput, *awserr.Error) {
	listsLocal := in.Scope == nil || *in.Scope != iamapi.PolicyScopeTypeAws
	// Scope Local loads no catalogue, so the empty one stands in.
	var managed *policycatalogue.Catalogue
	if in.Scope == nil || *in.Scope != iamapi.PolicyScopeTypeLocal {
		cat, err := s.loadCatalogue()
		if err != nil {
			return nil, err
		}
		managed = cat
	}
	account := c.Scope().Account
	p, err := view(s, account, func(st *State) (paging.Page[listedPolicy], *awserr.Error) {
		var all []listedPolicy
		add := func(v iamapi.Policy) {
			if l, ok := listPolicy(in, v); ok {
				all = append(all, l)
			}
		}
		if listsLocal {
			local := sortedWhere(st.Policies, func(p *Policy) bool { return underPrefix(p.Path, in.PathPrefix) })
			for i := range local {
				add(apiPolicy(st, account, local[i]))
			}
		}
		for p := range managed.All() {
			if underPrefix(p.Path, in.PathPrefix) {
				add(apiAWSManagedPolicy(st, p))
			}
		}
		slices.SortFunc(all, func(a, b listedPolicy) int { return strings.Compare(a.key, b.key) })
		return paginate(policiesListing, all, func(l listedPolicy) string { return l.key }, in.Marker, in.MaxItems)
	})
	if err != nil {
		return nil, err
	}
	// Non-nil, so an empty page still sends <Policies/>.
	out := &iamapi.ListPoliciesOutput{Policies: make([]iamapi.Policy, len(p.Items)), IsTruncated: p.Truncated, Marker: p.Next}
	for i := range p.Items {
		out.Policies[i] = p.Items[i].policy
	}
	return out, nil
}

// policiesListing is the listing ListPolicies pages through.
const policiesListing paging.Listing = "policies"

// listedPolicy is one ListPolicies entry with its sort key, bounded so that
// the Marker naming it fits maxMarkerLen.
type listedPolicy struct {
	key    string
	policy iamapi.Policy
}

// listPolicy is v as ListPolicies lists it, without tags or description,
// and false when in's OnlyAttached or PolicyUsageFilter leaves it out.
func listPolicy(in *iamapi.ListPoliciesInput, v iamapi.Policy) (listedPolicy, bool) {
	attached, boundary := *v.AttachmentCount > 0, *v.PermissionsBoundaryUsageCount > 0
	if in.OnlyAttached && !attached || in.PolicyUsageFilter != nil && !inUse(attached, boundary, in.PolicyUsageFilter) {
		return listedPolicy{}, false
	}
	v.Tags, v.Description = nil, nil
	// A space sorts below every character of a policy name, so the key
	// orders by name, then by ARN. A customer and an AWS managed policy may
	// share a name, never an ARN.
	return listedPolicy{key: boundedSortKey(policiesListing, key(*v.PolicyName)+" "+*v.Arn), policy: v}, true
}

// inUse reports whether a policy attached or used as a permissions boundary
// is in use the way filter asks: attached, as a boundary, or either when
// filter is nil. ListPolicies applies it only with a filter, since a policy
// in use in neither way is listed there too.
func inUse(attached, boundary bool, filter *iamapi.PolicyUsageType) bool {
	switch {
	case filter == nil:
		return attached || boundary
	case *filter == iamapi.PolicyUsageTypePermissionsBoundary:
		return boundary
	default:
		return attached
	}
}

func (s *Service) createPolicyVersion(_ context.Context, c *kernel.Call, in *iamapi.CreatePolicyVersionInput) (*iamapi.CreatePolicyVersionOutput, *awserr.Error) {
	if err := checkManagedPolicy(in.PolicyDocument); err != nil {
		return nil, err
	}
	var created PolicyVersion
	p, err := s.changePolicy(c, in.PolicyArn, func(_ *State, p Policy, now clock.Instant) (Policy, *awserr.Error) {
		if len(p.Versions) >= maxPolicyVersions {
			return Policy{}, errTooManyVersions()
		}
		created = PolicyVersion{ID: "v" + strconv.Itoa(p.Next), Document: in.PolicyDocument, Created: now}
		p.Versions = append(slices.Clone(p.Versions), created)
		p.Next++
		p.Updated = now
		if in.SetAsDefault {
			p.Default = created.ID
		}
		return p, nil
	})
	if err != nil {
		return nil, err
	}
	return &iamapi.CreatePolicyVersionOutput{PolicyVersion: new(apiVersion(p, created, false))}, nil
}

// getPolicyVersion shows a version with its document. A catalogued AWS
// managed policy has only its default version (iam-evaluation.md 12.2).
func (s *Service) getPolicyVersion(_ context.Context, c *kernel.Call, in *iamapi.GetPolicyVersionInput) (*iamapi.GetPolicyVersionOutput, *awserr.Error) {
	named, err := s.lookup(in.PolicyArn)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	out, err := view(s, account, func(st *State) (iamapi.PolicyVersion, *awserr.Error) {
		switch named := named.(type) {
		case catalogued:
			if in.VersionId != named.policy.VersionID {
				return iamapi.PolicyVersion{}, errNoSuchVersion(in.PolicyArn, in.VersionId)
			}
			return apiAWSManagedVersion(named.policy, true), nil
		case uncatalogued:
			return iamapi.PolicyVersion{}, errNoSuchPolicy(in.PolicyArn)
		case notAWSManaged:
		}
		p, _, err := st.localPolicy(account, in.PolicyArn)
		if err != nil {
			return iamapi.PolicyVersion{}, err
		}
		v, ok := p.version(in.VersionId)
		if !ok {
			return iamapi.PolicyVersion{}, errNoSuchVersion(in.PolicyArn, in.VersionId)
		}
		return apiVersion(p, v, true), nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(policyTouch(in.PolicyArn))
	return &iamapi.GetPolicyVersionOutput{PolicyVersion: &out}, nil
}

// listPolicyVersions lists versions newest first, without documents
// (iam-sts-kms.md 3.2); a catalogued AWS managed policy lists its one
// version, the default.
func (s *Service) listPolicyVersions(_ context.Context, c *kernel.Call, in *iamapi.ListPolicyVersionsInput) (*iamapi.ListPolicyVersionsOutput, *awserr.Error) {
	named, err := s.lookup(in.PolicyArn)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	p, err := view(s, account, func(st *State) (paging.Page[listedVersion], *awserr.Error) {
		all, err := st.listedVersions(account, in.PolicyArn, named)
		if err != nil {
			return paging.Page[listedVersion]{}, err
		}
		return paginate("policy-versions", all, func(l listedVersion) string { return l.key }, in.Marker, in.MaxItems)
	})
	if err != nil {
		return nil, err
	}
	c.Touch(policyTouch(in.PolicyArn))
	out := &iamapi.ListPolicyVersionsOutput{Versions: make([]iamapi.PolicyVersion, len(p.Items)), IsTruncated: p.Truncated, Marker: p.Next}
	for i, l := range p.Items {
		out.Versions[i] = l.version
	}
	return out, nil
}

// listedVersion is one ListPolicyVersions entry with its sort key.
type listedVersion struct {
	key     string
	version iamapi.PolicyVersion
}

// listedVersions are the versions of the policy at arn, which named
// resolved among the AWS managed policies, newest first.
func (st *State) listedVersions(account aws.AccountID, arn string, named awsPolicy) ([]listedVersion, *awserr.Error) {
	switch named := named.(type) {
	case catalogued:
		// The one version needs no ordering, so its id is its key.
		return []listedVersion{{key: named.policy.VersionID, version: apiAWSManagedVersion(named.policy, false)}}, nil
	case uncatalogued:
		return nil, errNoSuchPolicy(arn)
	case notAWSManaged:
	}
	pol, _, err := st.localPolicy(account, arn)
	if err != nil {
		return nil, err
	}
	all := make([]listedVersion, 0, len(pol.Versions))
	for _, v := range slices.Backward(pol.Versions) {
		l, err := listVersion(v, apiVersion(pol, v, false))
		if err != nil {
			return nil, err
		}
		all = append(all, l)
	}
	return all, nil
}

func listVersion(v PolicyVersion, shown iamapi.PolicyVersion) (listedVersion, *awserr.Error) {
	k, err := v.sortKey()
	if err != nil {
		return listedVersion{}, err
	}
	return listedVersion{key: k, version: shown}, nil
}

func (s *Service) deletePolicyVersion(_ context.Context, c *kernel.Call, in *iamapi.DeletePolicyVersionInput) (*iamapi.DeletePolicyVersionOutput, *awserr.Error) {
	_, err := s.changePolicy(c, in.PolicyArn, func(_ *State, p Policy, _ clock.Instant) (Policy, *awserr.Error) {
		if _, ok := p.version(in.VersionId); !ok {
			return Policy{}, errNoSuchVersion(in.PolicyArn, in.VersionId)
		}
		if in.VersionId == p.Default {
			return Policy{}, errDeleteDefaultVersion()
		}
		p.Versions = slices.DeleteFunc(slices.Clone(p.Versions), func(v PolicyVersion) bool { return v.ID == in.VersionId })
		p.Updated = p.lastVersionCreated()
		return p, nil
	})
	return unit[iamapi.DeletePolicyVersionOutput](err)
}

// lastVersionCreated is the policy's UpdateDate as the Policy shape defines
// it: when its newest version was created, or when the policy was created
// once it has one version left.
func (p Policy) lastVersionCreated() clock.Instant {
	if len(p.Versions) < 2 {
		return p.Created
	}
	return p.Versions[len(p.Versions)-1].Created
}

// setDefaultPolicyVersion changes the default version, which leaves the
// UpdateDate alone: it tracks version creation, not the default.
func (s *Service) setDefaultPolicyVersion(_ context.Context, c *kernel.Call, in *iamapi.SetDefaultPolicyVersionInput) (*iamapi.SetDefaultPolicyVersionOutput, *awserr.Error) {
	_, err := s.changePolicy(c, in.PolicyArn, func(_ *State, p Policy, _ clock.Instant) (Policy, *awserr.Error) {
		if _, ok := p.version(in.VersionId); !ok {
			return Policy{}, errNoSuchVersion(in.PolicyArn, in.VersionId)
		}
		p.Default = in.VersionId
		return p, nil
	})
	return unit[iamapi.SetDefaultPolicyVersionOutput](err)
}

// listEntitiesForPolicy lists the roles and users that use a policy, as an
// attachment, a permissions boundary or either (PolicyUsageFilter).
func (s *Service) listEntitiesForPolicy(_ context.Context, c *kernel.Call, in *iamapi.ListEntitiesForPolicyInput) (*iamapi.ListEntitiesForPolicyOutput, *awserr.Error) {
	named, err := s.lookup(in.PolicyArn)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	p, err := view(s, account, func(st *State) (paging.Page[policyEntity], *awserr.Error) {
		switch named.(type) {
		case catalogued:
		case uncatalogued:
			return paging.Page[policyEntity]{}, errNoSuchPolicy(in.PolicyArn)
		case notAWSManaged:
			if _, _, err := st.localPolicy(account, in.PolicyArn); err != nil {
				return paging.Page[policyEntity]{}, err
			}
		}
		return paginate("entities-for-policy", st.policyEntities(in), func(e policyEntity) string { return e.sortKey }, in.Marker, in.MaxItems)
	})
	if err != nil {
		return nil, err
	}
	c.Touch(policyTouch(in.PolicyArn))
	// IAM sends all three lists, empty ones included.
	out := &iamapi.ListEntitiesForPolicyOutput{PolicyGroups: []iamapi.PolicyGroup{}, PolicyUsers: []iamapi.PolicyUser{}, PolicyRoles: []iamapi.PolicyRole{}, IsTruncated: p.Truncated, Marker: p.Next}
	for _, e := range p.Items {
		if e.kind == iamapi.EntityTypeRole {
			out.PolicyRoles = append(out.PolicyRoles, iamapi.PolicyRole{RoleName: new(e.name), RoleId: new(e.id)})
		} else {
			out.PolicyUsers = append(out.PolicyUsers, iamapi.PolicyUser{UserName: new(e.name), UserId: new(e.id)})
		}
	}
	return out, nil
}

// policyEntity is a role or user that uses a policy, as
// ListEntitiesForPolicy lists it; kind is EntityTypeRole or EntityTypeUser.
type policyEntity struct {
	sortKey  string
	kind     iamapi.EntityType
	name, id string
}

// policyEntities lists the roles, then the users, that use in's policy the
// way its filters ask, each kind by name.
func (st *State) policyEntities(in *iamapi.ListEntitiesForPolicyInput) []policyEntity {
	uses := func(path string, attached []string, boundary string) bool {
		return underPrefix(path, in.PathPrefix) && inUse(slices.Contains(attached, in.PolicyArn), boundary == in.PolicyArn, in.PolicyUsageFilter)
	}
	wants := func(t iamapi.EntityType) bool { return in.EntityFilter == nil || *in.EntityFilter == t }
	var out []policyEntity
	if wants(iamapi.EntityTypeRole) {
		roles := sortedWhere(st.Roles, func(r *Role) bool { return uses(r.Path, r.Attached, r.Boundary) })
		for i := range roles {
			out = append(out, policyEntity{sortKey: "role/" + key(roles[i].Name), kind: iamapi.EntityTypeRole, name: roles[i].Name, id: string(roles[i].ID)})
		}
	}
	if wants(iamapi.EntityTypeUser) {
		users := sortedWhere(st.Users, func(u *User) bool { return uses(u.Path, u.Attached, u.Boundary) })
		for i := range users {
			out = append(out, policyEntity{sortKey: "user/" + key(users[i].Name), kind: iamapi.EntityTypeUser, name: users[i].Name, id: string(users[i].ID)})
		}
	}
	return out
}
