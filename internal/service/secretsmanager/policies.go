package secretsmanager

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// publicPolicyUnchecked is the check-not-run advisory of a put that sets
// BlockPublicPolicy.
const publicPolicyUnchecked = "understudy did not check whether this resource policy prevents broad access: BlockPublicPolicy was true, " +
	"and AWS refuses a broad policy then with PublicPolicyException (iam-enforcement-surfaces.md 7.6); the policy was stored"

// putResourcePolicy stores the policy verbatim once it parses: Terraform
// compares what GetResourcePolicy returns with what it sent
// (tf-remaining.md 5). iam.enforce evaluates it under advise and enforce.
// BlockPublicPolicy is not checked under any mode, since which policies AWS
// counts as broad access is not researched; a put that sets it journals a
// check-not-run advisory, where AWS answers PublicPolicyException to a
// broad policy (iam-enforcement-surfaces.md 7.6).
func (s *Service) putResourcePolicy(ctx context.Context, c *kernel.Call, in *secretsmanagerapi.PutResourcePolicyInput) (*secretsmanagerapi.PutResourcePolicyOutput, *awserr.Error) {
	if _, perr := policydoc.Parse([]byte(in.ResourcePolicy)); perr != nil {
		return nil, secretsmanagerapi.ErrMalformedPolicyDocumentException(perr.Message())
	}
	r, sec, err := s.update(c, in.SecretId, func(sec *Secret) *awserr.Error {
		sec.Policy = new(in.ResourcePolicy)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if in.BlockPublicPolicy != nil && *in.BlockPublicPolicy {
		c.Record(ctx, c.Scope(), journal.Advisory{Kind: journal.AdvisoryCheckNotRun, Resource: sec.arn(r.scope), Text: publicPolicyUnchecked})
	}
	return &secretsmanagerapi.PutResourcePolicyOutput{ARN: new(sec.arn(r.scope).String()), Name: new(sec.Name)}, nil
}

// getResourcePolicy omits ResourcePolicy when the secret has none, which
// Terraform reads as no policy.
func (s *Service) getResourcePolicy(_ context.Context, c *kernel.Call, in *secretsmanagerapi.GetResourcePolicyInput) (*secretsmanagerapi.GetResourcePolicyOutput, *awserr.Error) {
	r, sec, err := s.findUsable(c, in.SecretId)
	if err != nil {
		return nil, err
	}
	return &secretsmanagerapi.GetResourcePolicyOutput{ARN: new(sec.arn(r.scope).String()), Name: new(sec.Name), ResourcePolicy: sec.Policy}, nil
}

func (s *Service) deleteResourcePolicy(_ context.Context, c *kernel.Call, in *secretsmanagerapi.DeleteResourcePolicyInput) (*secretsmanagerapi.DeleteResourcePolicyOutput, *awserr.Error) {
	r, sec, err := s.update(c, in.SecretId, func(sec *Secret) *awserr.Error {
		sec.Policy = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &secretsmanagerapi.DeleteResourcePolicyOutput{ARN: new(sec.arn(r.scope).String()), Name: new(sec.Name)}, nil
}
