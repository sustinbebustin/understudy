package secretsmanager

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Recovery windows of DeleteSecret, in days (secrets-logs-events-alarms.md
// 2.5), and the quota of secrets in one account and Region (2.6).
const (
	minWindowDays     = 7
	maxWindowDays     = 30
	defaultWindowDays = 30
	maxSecrets        = 500_000
	day               = 24 * time.Hour
)

// find returns the secret id names, in any state, and touches it.
func (s *Service) find(c *kernel.Call, id string) (secretRef, Secret, *awserr.Error) {
	r, err := parseSecretID(c, id)
	if err != nil {
		return secretRef{}, Secret{}, err
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
		return secretRef{}, Secret{}, errSecretNotFound()
	}
	c.Touch(f.sec.arn(r.scope))
	return r, f.sec, nil
}

// findUsable is find for the operations a secret scheduled for deletion
// refuses.
func (s *Service) findUsable(c *kernel.Call, id string) (secretRef, Secret, *awserr.Error) {
	r, sec, err := s.find(c, id)
	if err != nil {
		return secretRef{}, Secret{}, err
	}
	if _, ok := sec.Lifecycle.(scheduled); ok {
		return secretRef{}, Secret{}, errMarkedForDeletion()
	}
	return r, sec, nil
}

func (s *Service) createSecret(ctx context.Context, c *kernel.Call, in *secretsmanagerapi.CreateSecretInput) (*secretsmanagerapi.CreateSecretOutput, *awserr.Error) {
	if len(in.AddReplicaRegions) > 0 {
		return nil, errUnsupported("replication (AddReplicaRegions)")
	}
	token := *in.ClientRequestToken
	p, hasValue, err := valueOf(in.SecretString, in.SecretBinary)
	if err != nil {
		return nil, err
	}
	tags, err := setTags(nil, in.Tags)
	if err != nil {
		return nil, err
	}
	scope := c.Scope()
	type found struct {
		sec Secret
		ok  bool
	}
	if f := readState(s, scope, func(st *State) found { sec, ok := st.Secrets[in.Name]; return found{sec, ok} }); f.ok {
		return s.replayCreate(ctx, c, scope, f.sec, token, p, hasValue)
	}
	suffix := newSuffix(c.ID())
	arn := secretARN(scope, in.Name, suffix)
	kmsKeyID, box, err := s.encryptFirst(ctx, c, scope, arn.String(), in.KmsKeyId, token, p, hasValue)
	if err != nil {
		return nil, err
	}
	type created struct {
		sec Secret
		// raced is the secret a concurrent request created under the name
		// between the read above and this write, which replayCreate answers
		// as it answers one found by that read.
		raced *Secret
	}
	w, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, tx *kernel.Tx) (created, *awserr.Error) {
		if taken, ok := st.Secrets[in.Name]; ok {
			tx.Unchanged()
			return created{raced: &taken}, nil
		}
		if len(st.Secrets) >= maxSecrets {
			return created{}, errTooManySecrets()
		}
		sec := Secret{
			Name: in.Name, Suffix: suffix, CreateToken: token,
			Description: nonEmpty(in.Description), KMSKeyID: kmsKeyID, Type: in.Type,
			Created: now, Changed: now, Tags: tags, Lifecycle: active{},
		}
		sec.ownMaps()
		if hasValue {
			if err := sec.addVersion(Version{ID: token, Created: now, KMSKeyID: kmsKeyID, Value: box}, nil, now); err != nil {
				return created{}, err
			}
		}
		st.Secrets[in.Name] = sec
		return created{sec: sec}, nil
	})
	switch {
	case err != nil:
		return nil, err
	case w.raced != nil:
		return s.replayCreate(ctx, c, scope, *w.raced, token, p, hasValue)
	}
	sec := w.sec
	c.Touch(arn)
	out := &secretsmanagerapi.CreateSecretOutput{ARN: new(arn.String()), Name: new(sec.Name)}
	if hasValue {
		out.VersionId = new(token)
	}
	return out, nil
}

// encryptFirst is the KMS work of a CreateSecret of the secret arn, before
// its cell is taken: the key the secret stores for keyID (resolveKey) and,
// with a value, that value sealed under the key as version token; without
// one, a customer key's access is checked instead.
func (s *Service) encryptFirst(ctx context.Context, c *kernel.Call, scope aws.Scope, arn string, keyID *string, token string, p plain, hasValue bool) (*string, sealed, *awserr.Error) {
	kmsKeyID, err := s.resolveKey(ctx, c, scope, keyID)
	if err != nil {
		return nil, sealed{}, err
	}
	switch {
	case hasValue:
		box, err := s.seal(ctx, c, scope, arn, token, kmsKeyID, p)
		return kmsKeyID, box, err
	case kmsKeyID != nil:
		return kmsKeyID, sealed{}, s.checkKeyAccess(ctx, c, scope, arn, kmsKeyID)
	}
	return kmsKeyID, sealed{}, nil
}

// replayCreate answers a CreateSecret for a name that exists: a retry with
// the token and value that created it succeeds again; anything else is the
// conflict AWS reports.
func (s *Service) replayCreate(ctx context.Context, c *kernel.Call, scope aws.Scope, sec Secret, token string, p plain, hasValue bool) (*secretsmanagerapi.CreateSecretOutput, *awserr.Error) {
	if _, ok := sec.Lifecycle.(scheduled); ok {
		return nil, errNameScheduled()
	}
	v, hadValue := sec.Versions[token]
	if sec.CreateToken != token || hasValue != hadValue {
		return nil, errSecretExists(sec.Name)
	}
	arn := sec.arn(scope)
	c.Touch(arn)
	out := &secretsmanagerapi.CreateSecretOutput{ARN: new(arn.String()), Name: new(sec.Name)}
	if !hasValue {
		return out, nil
	}
	if err := s.sameValue(ctx, c, scope, sec, v, p); err != nil {
		return nil, err
	}
	out.VersionId = new(token)
	return out, nil
}

// sameValue answers nil when version v of sec holds p, and the token
// conflict otherwise. Comparing needs the stored value, so it decrypts
// through KMS, as AWS does for an idempotent retry.
func (s *Service) sameValue(ctx context.Context, c *kernel.Call, scope aws.Scope, sec Secret, v Version, p plain) *awserr.Error {
	arn := sec.arn(scope).String()
	stored, err := s.open(ctx, c, scope, arn, v)
	if err != nil {
		return err
	}
	if !stored.equal(p) {
		return errTokenInUse(v.ID, arn)
	}
	return nil
}

func (s *Service) describeSecret(_ context.Context, c *kernel.Call, in *secretsmanagerapi.DescribeSecretInput) (*secretsmanagerapi.DescribeSecretOutput, *awserr.Error) {
	r, sec, err := s.find(c, in.SecretId)
	if err != nil {
		return nil, err
	}
	return &secretsmanagerapi.DescribeSecretOutput{
		ARN:                new(sec.arn(r.scope).String()),
		Name:               new(sec.Name),
		Description:        sec.Description,
		KmsKeyId:           sec.KMSKeyID,
		Type:               sec.Type,
		CreatedDate:        new(sec.Created.Wire()),
		LastChangedDate:    new(sec.Changed.Wire()),
		LastAccessedDate:   wireOf(sec.Accessed),
		DeletedDate:        deletedDate(&sec),
		Tags:               renderTags(sec.Tags),
		VersionIdsToStages: sec.versionsToStages(),
	}, nil
}

// updateSecret changes the description, KMS key or type and, with a value,
// adds a version that takes AWSCURRENT.
func (s *Service) updateSecret(ctx context.Context, c *kernel.Call, in *secretsmanagerapi.UpdateSecretInput) (*secretsmanagerapi.UpdateSecretOutput, *awserr.Error) {
	p, hasValue, err := valueOf(in.SecretString, in.SecretBinary)
	if err != nil {
		return nil, err
	}
	var kmsKeyID *string
	if in.KmsKeyId != nil {
		r, _, err := s.findUsable(c, in.SecretId)
		if err != nil {
			return nil, err
		}
		if kmsKeyID, err = s.resolveKey(ctx, c, r.scope, in.KmsKeyId); err != nil {
			return nil, err
		}
	}
	meta := func(sec *Secret) {
		if in.Description != nil {
			sec.Description = nonEmpty(in.Description)
		}
		if in.KmsKeyId != nil {
			sec.KMSKeyID = kmsKeyID
		}
		if in.Type != nil {
			sec.Type = in.Type
		}
	}
	var nv *newVersion
	if hasValue {
		nv = &newVersion{token: *in.ClientRequestToken, value: p, reuse: alwaysConflicts}
	}
	r, sec, err := s.write(ctx, c, in.SecretId, meta, nv)
	if err != nil {
		return nil, err
	}
	out := &secretsmanagerapi.UpdateSecretOutput{ARN: new(sec.arn(r.scope).String()), Name: new(sec.Name)}
	if hasValue {
		out.VersionId = new(nv.token)
	}
	return out, nil
}

// deleteSecret schedules the secret's deletion at the end of its recovery
// window on the emulator clock, or deletes it at once when forced.
func (s *Service) deleteSecret(_ context.Context, c *kernel.Call, in *secretsmanagerapi.DeleteSecretInput) (*secretsmanagerapi.DeleteSecretOutput, *awserr.Error) {
	force := in.ForceDeleteWithoutRecovery != nil && *in.ForceDeleteWithoutRecovery
	window := int64(defaultWindowDays)
	if in.RecoveryWindowInDays != nil {
		window = *in.RecoveryWindowInDays
	}
	r, err := parseSecretID(c, in.SecretId)
	switch {
	case err != nil && force && isARN(in.SecretId):
		// An ARN out of reach names no secret, and forcing the deletion of
		// a secret that does not exist is not an error.
		arn, name := unreachableIdentity(in.SecretId)
		return &secretsmanagerapi.DeleteSecretOutput{ARN: new(arn), Name: new(name), DeletionDate: new(c.Admitted().Wire())}, nil
	case err != nil:
		return nil, err
	}
	type deleted struct {
		arn, name string
		date      clock.Instant
		// secret is the ARN of the secret deleted, for the journal; nil when
		// the secret did not exist, since arn then names no resource.
		secret *aws.ARN
	}
	d, err := kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (deleted, *awserr.Error) {
		sec, ok := r.find(st)
		switch {
		case !ok && force:
			// Forcing the deletion of a secret that does not exist is not
			// an error (secrets-logs-events-alarms.md 2.5), and AWS still
			// answers an ARN and a name.
			tx.Unchanged()
			arn, name := r.missingIdentity(c)
			return deleted{arn: arn, name: name, date: now}, nil
		case !ok:
			return deleted{}, errSecretNotFound()
		}
		arn := sec.arn(r.scope)
		if force {
			delete(st.Secrets, sec.Name)
			tx.Disarm(s.deletion, arn.String())
			return deleted{arn: arn.String(), name: sec.Name, date: now, secret: &arn}, nil
		}
		if _, gone := sec.Lifecycle.(scheduled); gone {
			return deleted{}, errMarkedForDeletion()
		}
		date := now.Add(time.Duration(window) * day)
		sec.Lifecycle = scheduled{DeletionDate: date}
		st.Secrets[sec.Name] = sec
		tx.Arm(s.deletion, arn.String(), date)
		return deleted{arn: arn.String(), name: sec.Name, date: date, secret: &arn}, nil
	})
	if err != nil {
		return nil, err
	}
	if d.secret != nil {
		c.Touch(*d.secret)
	}
	return &secretsmanagerapi.DeleteSecretOutput{ARN: new(d.arn), Name: new(d.name), DeletionDate: new(d.date.Wire())}, nil
}

// missingIdentity is the ARN and name a forced DeleteSecret answers for a
// secret r names that does not exist: a full ARN as given, with the name it
// carries, or for a name or a partial ARN the ARN a secret created under
// that name by this call would have.
func (r secretRef) missingIdentity(c *kernel.Call) (arn, name string) {
	if r.arn != "" {
		if name, _, ok := cutSuffix(r.name); ok {
			return r.arn, name
		}
	}
	return secretARN(r.scope, r.name, newSuffix(c.ID())).String(), r.name
}

// unreachableIdentity is missingIdentity for an ARN parseSecretID refuses
// (another partition or Region, or not a secret's): the ARN as given and
// the name it carries, or the whole id when it carries none.
func unreachableIdentity(id string) (arn, name string) {
	a, err := aws.ParseARN(id)
	if err != nil {
		return id, id
	}
	rest, ok := strings.CutPrefix(a.Resource, "secret:")
	if !ok || rest == "" {
		return id, id
	}
	if name, _, ok := cutSuffix(rest); ok {
		return id, name
	}
	return id, rest
}

// restoreSecret cancels a scheduled deletion; a secret that is not
// scheduled for deletion is left as it is.
func (s *Service) restoreSecret(_ context.Context, c *kernel.Call, in *secretsmanagerapi.RestoreSecretInput) (*secretsmanagerapi.RestoreSecretOutput, *awserr.Error) {
	r, _, err := s.find(c, in.SecretId)
	if err != nil {
		return nil, err
	}
	sec, err := kernel.Write(c, s.cells, r.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (Secret, *awserr.Error) {
		sec, ok := r.find(st)
		if !ok {
			return Secret{}, errSecretNotFound()
		}
		if _, gone := sec.Lifecycle.(scheduled); !gone {
			tx.Unchanged()
			return sec, nil
		}
		sec.Lifecycle = active{}
		st.Secrets[sec.Name] = sec
		tx.Disarm(s.deletion, sec.arn(r.scope).String())
		return sec, nil
	})
	if err != nil {
		return nil, err
	}
	return &secretsmanagerapi.RestoreSecretOutput{ARN: new(sec.arn(r.scope).String()), Name: new(sec.Name)}, nil
}

// onDeletion deletes a secret whose recovery window ended. A secret that
// was restored, or deleted and recreated under the same name, is left
// alone.
func (s *Service) onDeletion(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	scope, name, suffix, ok := parseSecretARN(key.ID)
	if !ok {
		return
	}
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		sec, ok := st.Secrets[name]
		l, gone := sec.Lifecycle.(scheduled)
		if !ok || sec.Suffix != suffix || !gone || due.Before(l.DeletionDate) {
			tx.Unchanged()
			return struct{}{}, nil
		}
		delete(st.Secrets, name)
		return struct{}{}, nil
	})
	if err != nil {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "secret deletion failed; the secret stays scheduled for deletion until a snapshot load re-arms its timer",
			slog.String("secret", key.ID), slog.String("error", err.Message))
	}
}

// parseSecretARN reads the scope, name and suffix of a full secret ARN, for
// a deletion timer's ID.
func parseSecretARN(arn string) (scope aws.Scope, name, suffix string, ok bool) {
	a, err := aws.ParseARN(arn)
	if err != nil {
		return aws.Scope{}, "", "", false
	}
	rest, isSecret := strings.CutPrefix(a.Resource, "secret:")
	name, suffix, hasSuffix := cutSuffix(rest)
	if !isSecret || !hasSuffix {
		return aws.Scope{}, "", "", false
	}
	scope, err = store.ScopeKeys().Decode(a.Account + "/" + a.Region)
	if err != nil {
		return aws.Scope{}, "", "", false
	}
	return scope, name, suffix, true
}

// deletedDate is DescribeSecret's DeletedDate: the deletion date of a
// secret scheduled for deletion, absent otherwise.
func deletedDate(sec *Secret) *time.Time {
	if l, ok := sec.Lifecycle.(scheduled); ok {
		return new(l.DeletionDate.Wire())
	}
	return nil
}

// nonEmpty is nil for an absent or empty string: an empty Description is
// none, and an empty KmsKeyId is the default key.
func nonEmpty(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}

func wireOf(i *clock.Instant) *time.Time {
	if i == nil {
		return nil
	}
	return new(i.Wire())
}
