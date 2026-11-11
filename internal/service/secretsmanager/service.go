package secretsmanager

import (
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/authz"
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
		}),
		// secretsmanager:TagResource only for a secret created with tags,
		// secretsmanager:ReplicateSecretToRegions only with replica regions:
		// each applies when its member is sent (iam-enforcement-surfaces.md
		// 10.2, 10.4).
		kernel.WithActions(func(_ *kernel.Call, in *secretsmanagerapi.CreateSecretInput) []registry.IAMAction {
			var out []registry.IAMAction
			if len(in.Tags) > 0 {
				out = append(out, registry.IAMAction{Prefix: "secretsmanager", Name: "TagResource"})
			}
			if len(in.AddReplicaRegions) > 0 {
				out = append(out, registry.IAMAction{Prefix: "secretsmanager", Name: "ReplicateSecretToRegions"})
			}
			return out
		}),
		// Authorized at step 8 on the secret the call creates (the
		// Service Authorization Reference lists the Secret resource type
		// for CreateSecret and TagResource): its suffix is drawn from the
		// request id (newSuffix), so the ARN is known before the handler;
		// a name that exists names that secret.
		kernel.WithResources(func(c *kernel.Call, in *secretsmanagerapi.CreateSecretInput) []aws.ARN {
			return []aws.ARN{s.createTarget(c, in.Name)}
		}))
	named := registry.Partial{Note: byName}
	b.DescribeSecret(s.describeSecret, named)
	b.ListSecrets(s.listSecrets, registry.Full{})
	b.UpdateSecret(s.updateSecret, named,
		kernel.WithCheck(func(in *secretsmanagerapi.UpdateSecretInput) *awserr.Error {
			return firstError(requireToken(in.ClientRequestToken), checkValue(in.SecretString, in.SecretBinary))
		}))
	b.DeleteSecret(s.deleteSecret, named, kernel.WithCheck(checkDelete))
	b.RestoreSecret(s.restoreSecret, named)

	b.PutSecretValue(s.putSecretValue, registry.Partial{Note: "RotationToken is accepted and ignored: rotation is not implemented; " + byName},
		kernel.WithCheck(func(in *secretsmanagerapi.PutSecretValueInput) *awserr.Error {
			return firstError(requireToken(in.ClientRequestToken), checkValue(in.SecretString, in.SecretBinary))
		}))
	b.GetSecretValue(s.getSecretValue, named)
	b.ListSecretVersionIds(s.listSecretVersionIDs, named)
	b.UpdateSecretVersionStage(s.updateSecretVersionStage, named)

	b.TagResource(s.tagResource, named)
	b.UntagResource(s.untagResource, named)
	b.PutResourcePolicy(s.putResourcePolicy, registry.Partial{Note: "the policy is parsed and stored verbatim, evaluated under iam.enforce=advise, which journals each denial, and enforced under iam.enforce=enforce; BlockPublicPolicy is not evaluated under any mode, so a broad policy sent with BlockPublicPolicy true is stored where AWS answers PublicPolicyException, and each put sending it true journals a check-not-run advisory; " + byName})
	b.GetResourcePolicy(s.getResourcePolicy, named)
	b.DeleteResourcePolicy(s.deleteResourcePolicy, named)
	s.declareAuthorization(d.Binder)
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

// Switches lists the behaviors chosen without AWS-recorded evidence, with
// the status of a denial (iam-enforcement-surfaces.md 12.8;
// third-party-recorded for a denial that is not IAM's) and the operations
// that take a SecretId, which deny a missing secret first (byName).
func (s *Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		authz.RecordedSwitch("secretsmanager.denial.status", "AccessDeniedException 400, body key Message", "3.2", "IAM-ENF-06", authz.SessionUnreplayed,
			s.k.Bindings().AuthorizedOps(secretsmanagerapi.Service())),
		authz.RecordedDenyFirstSwitch("secretsmanager.enforce.missing-resource.by-name",
			"DescribeSecret of a missing secret, and of an existing one (IAM-ENF-06), naming the SecretId as given",
			"the other operations that take a SecretId, and the ARN a missing secret is evaluated on, without a suffix (impliedARN)",
			s.k.Bindings().NamedDenialOps(secretsmanagerapi.Service())),
		fidelity.Switch[string]{
			Name:    "secretsmanager.list-secrets.filter-combination",
			Value:   "and-across-filters,or-across-values",
			Label:   evidence.Inferred,
			Ref:     "docs/research/secrets-logs-events-alarms.md#24-operations-request-and-response-facts",
			Ops:     []string{"secretsmanager.ListSecrets"},
			Settles: fidelity.NoScenario{Reason: "unrecorded: no recording calls ListSecrets"},
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "secretsmanager.list-secrets.all-filter-match",
			Value:   "any-word,word-prefix",
			Label:   evidence.Inferred,
			Ref:     "https://docs.aws.amazon.com/secretsmanager/latest/userguide/manage_search-secret.html",
			Ops:     []string{"secretsmanager.ListSecrets"},
			Settles: fidelity.NoScenario{Reason: "unrecorded: no recording calls ListSecrets"},
		}.Entry(),
		fidelity.Switch[string]{
			// GetSecretValue sets the date; the others report it.
			Name:  "secretsmanager.last-accessed-date.granularity",
			Value: "day",
			Label: evidence.AWSDoc,
			Ref:   "docs/research/secrets-logs-events-alarms.md#24-operations-request-and-response-facts",
			Ops: []string{
				"secretsmanager.GetSecretValue", "secretsmanager.DescribeSecret", "secretsmanager.ListSecrets",
				"secretsmanager.ListSecretVersionIds",
			},
			Settles: fidelity.NoScenario{Reason: "unrecorded: no recording reads a LastAccessedDate"},
		}.Entry(),
		fidelity.Switch[string]{
			Name:    "secretsmanager.restore.not-scheduled",
			Value:   "no-op",
			Label:   evidence.Inferred,
			Ref:     "docs/research/secrets-logs-events-alarms.md#25-deletion-window-force-delete-restore-clock-driven",
			Ops:     []string{"secretsmanager.RestoreSecret"},
			Settles: fidelity.NoScenario{Reason: "secretsmanager/R15/05-restore-live.json is recorded; no scenario replays secretsmanager/R15 yet"},
		}.Entry(),
	}
}
