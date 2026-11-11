package secretsmanager

import (
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// byName is the coverage note of the operations that take a SecretId. AWS
// denies DescribeSecret of a missing secret before saying it is missing,
// and names the secret in a denial as the SecretId gives it, whether or not
// it exists (iam-enforcement-surfaces.md 3.2, 5.3; IAM-ENF-06, IAM-ENF-14).
// Which ARN AWS evaluates for a missing secret is unrecorded.
const byName = "under iam.enforce, a secret that does not exist is denied before it answers ResourceNotFoundException, evaluated on the ARN its SecretId implies, without a suffix (inferred); " +
	"a denial names the secret as the SecretId gives it (secretsmanager.enforce.missing-resource.by-name)"

// declareAuthorization states what each operation that takes a SecretId is
// authorized on at step 8 (iam-evaluation.md 2.1): the secret it names, by
// its full ARN, which Secrets Manager resolves before the handler, else
// the ARN the SecretId implies (impliedARN), so a missing secret is denied
// first. A denial names the SecretId as given. It runs after the bind
// sites, whose bindings it extends.
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

// secretNamed authorizes an operation on the secret id names: the secret's
// ARN, read before the handler, else impliedARN for one that does not
// exist. A name outside the secret-name character set is refused at step 7,
// before authorization, as AWS validates first (IAM-ENF-13). A SecretId
// that is an ARN out of reach names nothing, so its pair is on any
// resource. A denial names the SecretId as given (IAM-ENF-06, IAM-ENF-14).
func secretNamed[In any](s *Service, id func(*In) string) []kernel.BindOption {
	return []kernel.BindOption{
		kernel.WithCheck(func(in *In) *awserr.Error {
			if !isARN(id(in)) {
				return checkName(id(in))
			}
			return nil
		}),
		kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
			r, err := parseSecretID(c, id(in))
			if err != nil {
				return nil
			}
			if sec, ok := store.View2(s.cells, r.scope, r.find); ok {
				return []aws.ARN{sec.arn(r.scope)}
			}
			return []aws.ARN{r.impliedARN()}
		}),
		kernel.WithDenialResource(func(_ *kernel.Call, in *In) string { return id(in) }),
	}
}

// impliedARN is the ARN a SecretId naming no secret is asked about: the
// ARN it gives, else the secret ARN of its name without a suffix, so a
// caller allowed on secret:* or secret:<name>* goes on to
// ResourceNotFoundException (inferred: which string AWS evaluates is
// unrecorded; iam-enforcement-surfaces.md 5.3).
func (r secretRef) impliedARN() aws.ARN {
	return r.scope.ARN("secretsmanager", "secret:"+r.name)
}
