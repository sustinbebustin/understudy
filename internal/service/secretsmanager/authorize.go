package secretsmanager

import (
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// resolvedFirst is the coverage note of the operations that take a
// SecretId: a name or partial ARN gives the secret's ARN only once Secrets
// Manager resolved it (its suffix is the secret's own), so the call is
// authorized then, and a secret that does not exist answers
// ResourceNotFoundException before authorization. Whether AWS denies first
// is UNVERIFIED (iam-enforcement-surfaces.md 5.3, IAM-ENF-14).
const resolvedFirst = "under iam.enforce, a secret that does not exist answers ResourceNotFoundException before authorization (secretsmanager.enforce.missing-resource.by-name)"

// declareAuthorization states what each operation that takes a SecretId is
// authorized on: the secret it names, by its full ARN, once Secrets Manager
// resolved it (iam-evaluation.md 2.1; kernel.AuthorizedAfterResolve). It
// runs after the bind sites, whose bindings it extends.
func (s *Service) declareAuthorization(b *kernel.Binder) {
	kernel.Declare(b, secretsmanagerapi.OpDescribeSecret, secretNamed(s, func(in *secretsmanagerapi.DescribeSecretInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpUpdateSecret, secretNamed(s, func(in *secretsmanagerapi.UpdateSecretInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpDeleteSecret, secretNamed(s, func(in *secretsmanagerapi.DeleteSecretInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpRestoreSecret, secretNamed(s, func(in *secretsmanagerapi.RestoreSecretInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpPutSecretValue, secretNamed(s, func(in *secretsmanagerapi.PutSecretValueInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpGetSecretValue, secretNamed(s, func(in *secretsmanagerapi.GetSecretValueInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpListSecretVersionIds, secretNamed(s, func(in *secretsmanagerapi.ListSecretVersionIdsInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpUpdateSecretVersionStage, secretNamed(s, func(in *secretsmanagerapi.UpdateSecretVersionStageInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpTagResource, secretNamed(s, func(in *secretsmanagerapi.TagResourceInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpUntagResource, secretNamed(s, func(in *secretsmanagerapi.UntagResourceInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpPutResourcePolicy, secretNamed(s, func(in *secretsmanagerapi.PutResourcePolicyInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpGetResourcePolicy, secretNamed(s, func(in *secretsmanagerapi.GetResourcePolicyInput) string { return in.SecretId })...)
	kernel.Declare(b, secretsmanagerapi.OpDeleteResourcePolicy, secretNamed(s, func(in *secretsmanagerapi.DeleteResourcePolicyInput) string { return in.SecretId })...)
}

// secretNamed authorizes an operation on the secret id names once Secrets
// Manager resolved it: the secret's ARN is read before the handler, and a
// SecretId that names no secret names nothing, so the handler answers it
// before anything is asked.
func secretNamed[In any](s *Service, id func(*In) string) []kernel.BindOption {
	return []kernel.BindOption{
		kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
			r, err := parseSecretID(c, id(in))
			if err != nil {
				return nil
			}
			type found struct {
				sec Secret
				ok  bool
			}
			f := readState(s, r.scope, func(st *State) found {
				sec, ok := r.find(st)
				return found{sec, ok}
			})
			if !f.ok {
				return nil
			}
			return []aws.ARN{f.sec.arn(r.scope)}
		}),
		kernel.AuthorizedAfterResolve(),
	}
}
