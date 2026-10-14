package secretsmanager

import (
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerSecretDeletion fires when a secret's recovery window ends; the
// secret is then deleted for good. The timer's ID is the secret ARN.
const TimerSecretDeletion kernel.TimerKind = "secretsmanager.deletion"

// Service is AWS Secrets Manager: one state cell per account and region.
// Secret values are encrypted through KMS, which must be bound in the same
// kernel.
type Service struct {
	k     *kernel.Kernel
	cells *store.Partitioned[aws.Scope, State]
	// deletion is TimerSecretDeletion as registered with the kernel.
	deletion kernel.HandledKind
}

// New binds the Secrets Manager operations understudy implements and
// registers the deletion timer. Every other operation (rotation,
// replication, BatchGetSecretValue, GetRandomPassword,
// ValidateResourcePolicy) stays unbound and answers 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	s := &Service{
		k:     d.Kernel,
		cells: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState),
	}
	s.deletion = d.Kernel.Timers().Handle(TimerSecretDeletion, s.onDeletion)
	b := secretsmanagerapi.NewBinder(d.Binder)

	b.CreateSecret(s.createSecret, registry.Partial{Note: "AddReplicaRegions (replication) answers NotImplemented"},
		kernel.WithCheck(func(in *secretsmanagerapi.CreateSecretInput) *awserr.Error {
			return firstError(requireToken(in.ClientRequestToken), checkName(in.Name), checkValue(in.SecretString, in.SecretBinary))
		}))
	b.DescribeSecret(s.describeSecret, registry.Full{})
	b.ListSecrets(s.listSecrets, registry.Full{})
	b.UpdateSecret(s.updateSecret, registry.Full{},
		kernel.WithCheck(func(in *secretsmanagerapi.UpdateSecretInput) *awserr.Error {
			return firstError(requireToken(in.ClientRequestToken), checkValue(in.SecretString, in.SecretBinary))
		}))
	b.DeleteSecret(s.deleteSecret, registry.Full{}, kernel.WithCheck(checkDelete))
	b.RestoreSecret(s.restoreSecret, registry.Full{})

	b.PutSecretValue(s.putSecretValue, registry.Partial{Note: "RotationToken is accepted and ignored: rotation is not implemented"},
		kernel.WithCheck(func(in *secretsmanagerapi.PutSecretValueInput) *awserr.Error {
			return firstError(requireToken(in.ClientRequestToken), checkValue(in.SecretString, in.SecretBinary))
		}))
	b.GetSecretValue(s.getSecretValue, registry.Full{})
	b.ListSecretVersionIds(s.listSecretVersionIDs, registry.Full{})
	b.UpdateSecretVersionStage(s.updateSecretVersionStage, registry.Full{})

	b.TagResource(s.tagResource, registry.Full{})
	b.UntagResource(s.untagResource, registry.Full{})
	b.PutResourcePolicy(s.putResourcePolicy, registry.StoredOnly{Note: "the policy is parsed and stored verbatim, not enforced; BlockPublicPolicy is not evaluated"})
	b.GetResourcePolicy(s.getResourcePolicy, registry.Full{})
	b.DeleteResourcePolicy(s.deleteResourcePolicy, registry.Full{})
	return s
}

// requireToken refuses a write without a ClientRequestToken, which only a
// hand-rolled client sends: the SDKs and the CLI always generate one.
func requireToken(token *string) *awserr.Error {
	if token == nil {
		return errTokenRequired()
	}
	return nil
}

func checkName(name string) *awserr.Error {
	if !validName(name) {
		return errInvalidName()
	}
	return nil
}

func checkValue(s *string, b []byte) *awserr.Error {
	_, _, err := valueOf(s, b)
	return err
}

// checkDelete refuses a recovery window outside 7 to 30 days and a window
// given together with ForceDeleteWithoutRecovery.
func checkDelete(in *secretsmanagerapi.DeleteSecretInput) *awserr.Error {
	if in.RecoveryWindowInDays == nil {
		return nil
	}
	if in.ForceDeleteWithoutRecovery != nil && *in.ForceDeleteWithoutRecovery {
		return errForceWithWindow()
	}
	if w := *in.RecoveryWindowInDays; w < minWindowDays || w > maxWindowDays {
		return errRecoveryWindow()
	}
	return nil
}

func firstError(errs ...*awserr.Error) *awserr.Error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Switches lists the behaviors chosen without AWS-recorded evidence.
func (*Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		fidelity.Switch[string]{
			Name:  "secretsmanager.list-secrets.filter-combination",
			Value: "and-across-filters,or-across-values",
			Label: evidence.Inferred,
			Ref:   "docs/research/secrets-logs-events-alarms.md#24-operations-request-and-response-facts",
		}.Entry(),
		fidelity.Switch[string]{
			Name:  "secretsmanager.list-secrets.all-filter-match",
			Value: "any-word,word-prefix",
			Label: evidence.Inferred,
			Ref:   "https://docs.aws.amazon.com/secretsmanager/latest/userguide/manage_search-secret.html",
		}.Entry(),
		fidelity.Switch[string]{
			Name:  "secretsmanager.last-accessed-date.granularity",
			Value: "day",
			Label: evidence.AWSDoc,
			Ref:   "docs/research/secrets-logs-events-alarms.md#24-operations-request-and-response-facts",
		}.Entry(),
		fidelity.Switch[string]{
			Name:  "secretsmanager.restore.not-scheduled",
			Value: "no-op",
			Label: evidence.Inferred,
			Ref:   "docs/research/secrets-logs-events-alarms.md#25-deletion-window-force-delete-restore-clock-driven",
		}.Entry(),
	}
}
