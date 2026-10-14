package secretsmanager

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// putResourcePolicy stores the policy verbatim once it parses: Terraform
// compares what GetResourcePolicy returns with what it sent
// (tf-remaining.md 5). understudy does not enforce it.
func (s *Service) putResourcePolicy(_ context.Context, c *kernel.Call, in *secretsmanagerapi.PutResourcePolicyInput) (*secretsmanagerapi.PutResourcePolicyOutput, *awserr.Error) {
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
