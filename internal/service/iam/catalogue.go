package iam

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/policycatalogue"
)

// catalogueSource is the coverage note of the operations that read AWS
// managed policies, iam-evaluation.md 12.2 verbatim.
const catalogueSource = "AWS managed policies come from the shipped catalogue (iam-evaluation.md 10): CreateDate and UpdateDate have minute precision, PolicyId is derived, AttachmentCount counts this emulator's attachments, and only the default version exists, so another VersionId answers NoSuchEntity"

// catalogueShown is the coverage note of the operations that show an AWS
// managed policy's IsAttachable and boundary count, neither of which the
// guide publishes (iam-evaluation.md 10.5).
const catalogueShown = "IsAttachable is true for every catalogued policy, service-linked role policies included, and PermissionsBoundaryUsageCount counts this emulator's boundaries; the guide publishes neither and AWS's values are not recorded (iam-evaluation.md 10.5)"

// catalogueMembership is the coverage note of the operations that attach
// an AWS managed policy or make one a permissions boundary.
const catalogueMembership = "an AWS managed policy ARN must be in the shipped catalogue (iam-evaluation.md 10), so a policy AWS published after the catalogue was retrieved is NoSuchEntity"

// awsPolicy is what a policy ARN names among the AWS managed policies. A
// handler resolves it with lookup before it takes the account's state cell,
// and only an AWS managed ARN loads the catalogue.
//
//sumtype:decl
type awsPolicy interface{ isAWSPolicy() }

// notAWSManaged is an ARN that is not an arn:aws:iam::aws:policy ARN: a
// customer managed policy's, or no policy's. The handler resolves it
// against the account's state.
type notAWSManaged struct{}

// catalogued is an AWS managed policy the catalogue holds.
type catalogued struct {
	ref    policyRef
	policy policycatalogue.Policy
}

// uncatalogued is an arn:aws:iam::aws:policy ARN the catalogue does not
// hold. It names no policy: understudy never serves a made-up one
// (iam-evaluation.md 12.6).
type uncatalogued struct{}

func (notAWSManaged) isAWSPolicy() {}
func (catalogued) isAWSPolicy()    {}
func (uncatalogued) isAWSPolicy()  {}

// lookup resolves arn among the AWS managed policies, loading the
// catalogue only when arn is an AWS managed policy's. A catalogue that
// fails to load is InternalFailure, never a missing policy.
func (s *Service) lookup(arn string) (awsPolicy, *awserr.Error) {
	ref, ok := parsePolicyARN(arn)
	if !ok || !ref.awsManaged() {
		return notAWSManaged{}, nil
	}
	cat, err := s.loadCatalogue()
	if err != nil {
		return nil, err
	}
	p, ok := cat.Lookup(arn)
	if !ok {
		return uncatalogued{}, nil
	}
	return catalogued{ref: ref, policy: p}, nil
}

// loadCatalogue returns the catalogue IAM serves: the empty one without a
// loader (Options.Catalogue).
func (s *Service) loadCatalogue() (*policycatalogue.Catalogue, *awserr.Error) {
	if s.catalogue == nil {
		return nil, nil
	}
	cat, err := s.catalogue()
	if err != nil {
		return nil, awserr.Internalf(&catalogueError{err: err})
	}
	return cat, nil
}

// catalogueError is a catalogue that failed to load. Every call that names
// an AWS managed policy, or lists them, fails with it; customer managed
// policies, roles and users are unaffected.
type catalogueError struct{ err error }

func (e *catalogueError) Error() string {
	return fmt.Sprintf("iam: the AWS managed policy catalogue did not load, so calls naming arn:aws:iam::aws:policy ARNs or listing them fail: %v; 'go run ./internal/cmd/managedpolicies -verify' reports what is wrong with the embedded data", e.err)
}

func (e *catalogueError) Unwrap() error { return e.err }

// apiAWSManagedPolicy is a catalogued AWS managed policy as GetPolicy shows
// it: the guide's name, path, default version, times and description, a
// PolicyId derived from the ARN, its usage in the account from used, and no
// tags.
func apiAWSManagedPolicy(p policycatalogue.Policy, used map[string]policyUsage) iamapi.Policy {
	u := used[p.ARN]
	out := iamapi.Policy{
		PolicyName:                    new(p.Name),
		PolicyId:                      new(string(awsManagedPolicyID(p.ARN))),
		Arn:                           new(p.ARN),
		Path:                          new(p.Path),
		DefaultVersionId:              new(p.VersionID),
		AttachmentCount:               new(u.attached),
		PermissionsBoundaryUsageCount: new(u.boundaries),
		IsAttachable:                  true,
		CreateDate:                    new(p.Created),
		UpdateDate:                    new(p.Updated),
	}
	if p.Description != "" {
		out.Description = new(p.Description)
	}
	return out
}

// apiAWSManagedVersion is a catalogued policy's one version, the default,
// with the catalogue's document when withDocument is set. Its CreateDate is
// the policy's UpdateDate, which the Policy shape defines as when its
// newest version was created.
func apiAWSManagedVersion(p policycatalogue.Policy, withDocument bool) iamapi.PolicyVersion {
	out := iamapi.PolicyVersion{VersionId: new(p.VersionID), IsDefaultVersion: true, CreateDate: new(p.Updated)}
	if withDocument {
		out.Document = new(p.Document)
	}
	return out
}
