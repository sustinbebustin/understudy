package secretsmanager

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// newVersion is a value to store as a new version. Nil labels give it
// AWSCURRENT; otherwise it takes exactly the labels named.
type newVersion struct {
	token  string
	value  plain
	labels []string
	reuse  tokenReuse
}

// tokenReuse is what an operation answers for a ClientRequestToken that
// already names a version of the secret (secrets-logs-events-alarms.md 2.3).
type tokenReuse uint8

const (
	// sameValueReplays: the same value is an idempotent retry that changes
	// nothing, another value the token conflict (PutSecretValue).
	sameValueReplays tokenReuse = iota + 1
	// alwaysConflicts: any reuse is the token conflict, whatever the value
	// (UpdateSecret: "results in an error").
	alwaysConflicts
)

// write applies meta (nil for none) to the usable secret id names and, when
// nv is not nil, adds nv as a version sealed under the secret's key after
// meta. A version that already has nv's token is answered by nv.reuse.
// Without nv, a meta that moves the secret to another customer key first
// checks the caller's access to that key. It returns the stored secret and
// the reference that found it, whose scope the secret's ARN is in.
//
// A version is sealed before the write takes the cell, under the key the
// secret had when read. When another write moved the secret to another key
// in between, the sealed value would sit under the old key while the
// secret's labeled versions moved on, so the write reads the secret again
// and seals anew. Each retry follows a write that committed, so the loop
// ends once the secret's key stops changing.
func (s *Service) write(ctx context.Context, c *kernel.Call, id string, meta func(sec *Secret), nv *newVersion) (secretRef, Secret, *awserr.Error) {
	if meta == nil {
		meta = func(*Secret) {}
	}
	for {
		r, sec, err := s.findUsable(c, id)
		if err != nil {
			return secretRef{}, Secret{}, err
		}
		if nv == nil {
			out, err := s.writeMeta(ctx, c, r, sec, meta)
			return r, out, err
		}
		out, rekeyedMeanwhile, err := s.writeVersion(ctx, c, r, sec, meta, nv)
		if !rekeyedMeanwhile {
			return r, out, err
		}
	}
}

// writeMeta is write without a value: sec, as read, decides whether meta
// moves it to another key, and then the labeled versions move with it.
func (s *Service) writeMeta(ctx context.Context, c *kernel.Call, r secretRef, sec Secret, meta func(sec *Secret)) (Secret, *awserr.Error) {
	next := sec
	meta(&next)
	if rekeyed(sec.KMSKeyID, next.KMSKeyID) {
		if err := s.checkKeyAccess(ctx, c, r.scope, sec.arn(r.scope).String(), next.KMSKeyID); err != nil {
			return Secret{}, err
		}
	}
	moved, err := s.moveKey(ctx, c, r.scope, sec, next)
	if err != nil {
		return Secret{}, err
	}
	return s.modify(c, r, func(cur *Secret) *awserr.Error {
		meta(cur)
		applyRewrap(cur, moved)
		return nil
	})
}

// moveKey re-encrypts the labeled versions of sec under next's key when
// next has another key than sec (rewrap); nil when the key is the same.
func (s *Service) moveKey(ctx context.Context, c *kernel.Call, scope aws.Scope, sec, next Secret) (map[string]rewrapped, *awserr.Error) {
	if keyRef(sec.KMSKeyID) == keyRef(next.KMSKeyID) {
		return nil, nil
	}
	return s.rewrap(ctx, c, scope, sec, next.KMSKeyID)
}

// writeVersion is write with a value: sec, as read, gives the key to seal
// nv under once meta is applied, and the labeled versions move to that key
// with it. rekeyedMeanwhile reports that the secret's key changed after sec
// was read; nothing was written, and write reads it again.
func (s *Service) writeVersion(ctx context.Context, c *kernel.Call, r secretRef, sec Secret, meta func(sec *Secret), nv *newVersion) (out Secret, rekeyedMeanwhile bool, err *awserr.Error) {
	if v, ok := sec.Versions[nv.token]; ok {
		return sec, false, s.reused(ctx, c, r.scope, sec, v, nv)
	}
	next := sec
	meta(&next)
	box, err := s.seal(ctx, c, r.scope, sec.arn(r.scope).String(), nv.token, next.KMSKeyID, nv.value)
	if err != nil {
		return Secret{}, false, err
	}
	moved, err := s.moveKey(ctx, c, r.scope, sec, next)
	if err != nil {
		return Secret{}, false, err
	}
	type written struct {
		sec Secret
		// raced is the version a concurrent request with the same token
		// stored between the read above and this write.
		raced *Version
		// rekeyed is set when the secret's key after meta is no longer the
		// one box is sealed under.
		rekeyed bool
	}
	w, err := kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (written, *awserr.Error) {
		cur, ok := r.find(st)
		if !ok || cur.Suffix != sec.Suffix {
			return written{}, errSecretNotFound()
		}
		if _, gone := cur.Lifecycle.(scheduled); gone {
			return written{}, errMarkedForDeletion()
		}
		if v, dup := cur.Versions[nv.token]; dup {
			tx.Unchanged()
			return written{sec: cur, raced: &v}, nil
		}
		after := cur
		meta(&after)
		if keyRef(after.KMSKeyID) != keyRef(next.KMSKeyID) {
			tx.Unchanged()
			return written{rekeyed: true}, nil
		}
		cur.ownMaps()
		meta(&cur)
		applyRewrap(&cur, moved)
		if err := cur.addVersion(Version{ID: nv.token, Created: now, KMSKeyID: cur.KMSKeyID, Value: box}, nv.labels, now); err != nil {
			return written{}, err
		}
		cur.Changed = now
		st.Secrets[cur.Name] = cur
		return written{sec: cur}, nil
	})
	switch {
	case err != nil:
		return Secret{}, false, err
	case w.rekeyed:
		return Secret{}, true, nil
	case w.raced != nil:
		return w.sec, false, s.reused(ctx, c, r.scope, w.sec, *w.raced, nv)
	}
	return w.sec, false, nil
}

// reused answers a write of nv whose token already names version v of sec,
// as nv.reuse says.
func (s *Service) reused(ctx context.Context, c *kernel.Call, scope aws.Scope, sec Secret, v Version, nv *newVersion) *awserr.Error {
	switch nv.reuse {
	case sameValueReplays:
		return s.sameValue(ctx, c, scope, sec, v, nv.value)
	case alwaysConflicts:
	}
	return errTokenInUse(v.ID, sec.arn(scope).String())
}

// rekeyed reports a change of a secret's KMS key to a customer key named by
// next; a move to the default key needs no access check, since every caller
// can use the AWS managed key.
func rekeyed(cur, next *string) bool {
	return next != nil && (cur == nil || *cur != *next)
}

// modify replaces the usable secret r names with what fn makes of a copy
// of it, which owns its version and stage maps, and returns the stored
// secret. It advances LastChangedDate, which AWS documents as the last
// time the secret "was modified in any way".
func (s *Service) modify(c *kernel.Call, r secretRef, fn func(sec *Secret) *awserr.Error) (Secret, *awserr.Error) {
	return kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, _ *kernel.Tx) (Secret, *awserr.Error) {
		cur, ok := r.find(st)
		if !ok {
			return Secret{}, errSecretNotFound()
		}
		if _, gone := cur.Lifecycle.(scheduled); gone {
			return Secret{}, errMarkedForDeletion()
		}
		cur.ownMaps()
		if err := fn(&cur); err != nil {
			return Secret{}, err
		}
		cur.Changed = now
		st.Secrets[cur.Name] = cur
		return cur, nil
	})
}

// update is modify of the usable secret id names; it also returns the
// reference that found it, whose scope the secret's ARN is in.
func (s *Service) update(c *kernel.Call, id string, fn func(sec *Secret) *awserr.Error) (secretRef, Secret, *awserr.Error) {
	r, _, err := s.findUsable(c, id)
	if err != nil {
		return secretRef{}, Secret{}, err
	}
	sec, err := s.modify(c, r, fn)
	return r, sec, err
}

func (s *Service) putSecretValue(ctx context.Context, c *kernel.Call, in *secretsmanagerapi.PutSecretValueInput) (*secretsmanagerapi.PutSecretValueOutput, *awserr.Error) {
	p, hasValue, err := valueOf(in.SecretString, in.SecretBinary)
	if err != nil {
		return nil, err
	}
	if !hasValue {
		return nil, errNoValue()
	}
	token := *in.ClientRequestToken
	r, sec, err := s.write(ctx, c, in.SecretId, nil, &newVersion{token: token, value: p, labels: in.VersionStages, reuse: sameValueReplays})
	if err != nil {
		return nil, err
	}
	return &secretsmanagerapi.PutSecretValueOutput{
		ARN: new(sec.arn(r.scope).String()), Name: new(sec.Name), VersionId: new(token), VersionStages: sec.labels(token),
	}, nil
}

// getSecretValue decrypts the version VersionId or VersionStage names,
// AWSCURRENT when neither does, and records the day it was read.
func (s *Service) getSecretValue(ctx context.Context, c *kernel.Call, in *secretsmanagerapi.GetSecretValueInput) (*secretsmanagerapi.GetSecretValueOutput, *awserr.Error) {
	r, sec, err := s.findUsable(c, in.SecretId)
	if err != nil {
		return nil, err
	}
	v, err := selectVersion(sec, in.VersionId, in.VersionStage)
	if err != nil {
		return nil, err
	}
	arn := sec.arn(r.scope).String()
	p, err := s.open(ctx, c, r.scope, arn, v)
	if err != nil {
		return nil, err
	}
	if _, err := kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		recordAccess(st, sec.Suffix, r, v.ID, now, tx)
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	out := &secretsmanagerapi.GetSecretValueOutput{
		ARN: new(arn), Name: new(sec.Name), VersionId: new(v.ID),
		VersionStages: sec.labels(v.ID), CreatedDate: new(v.Created.Wire()),
	}
	switch p.kind {
	case kindString:
		out.SecretString = new(string(p.bytes))
	case kindBinary:
		out.SecretBinary = p.bytes
	}
	return out, nil
}

// recordAccess sets the last-accessed day of the secret and the version
// read. AWS reports the date, not the time (DescribeSecret's
// LastAccessedDate), so a second read on the same day changes nothing. A
// secret r now finds with another suffix than the one read is a recreated
// secret, which keeps its own dates.
func recordAccess(st *State, suffix string, r secretRef, versionID string, now clock.Instant, tx *kernel.Tx) {
	today := clock.FromWire(now.Wire().UTC().Truncate(day))
	cur, ok := r.find(st)
	v, vok := cur.Versions[versionID]
	if !ok || !vok || cur.Suffix != suffix || sameDay(cur.Accessed, today) && sameDay(v.Accessed, today) {
		tx.Unchanged()
		return
	}
	cur.ownMaps()
	cur.Accessed, v.Accessed = &today, &today
	cur.Versions[versionID] = v
	st.Secrets[cur.Name] = cur
}

func sameDay(i *clock.Instant, today clock.Instant) bool {
	return i != nil && i.Compare(today) == 0
}

// selectVersion picks the version GetSecretValue reads: by id, by stage, or
// AWSCURRENT; an id and a stage must name the same version.
func selectVersion(sec Secret, id, stage *string) (Version, *awserr.Error) {
	if id != nil {
		v, ok := sec.Versions[*id]
		if !ok {
			return Version{}, errVersionValueNotFound(*id)
		}
		if stage != nil && sec.Stages[*stage] != *id {
			return Version{}, errStageVersionMismatch()
		}
		return v, nil
	}
	label := stageCurrent
	if stage != nil {
		label = *stage
	}
	vid, ok := sec.Stages[label]
	if !ok {
		return Version{}, errStageValueNotFound(label)
	}
	return sec.Versions[vid], nil
}

// defaultKeyName is how ListSecretVersionIds names the AWS managed key.
const defaultKeyName = "DefaultEncryptionKey"

// listSecretVersionIDs lists the secret's versions oldest first; versions
// without labels only with IncludeDeprecated.
func (s *Service) listSecretVersionIDs(_ context.Context, c *kernel.Call, in *secretsmanagerapi.ListSecretVersionIdsInput) (*secretsmanagerapi.ListSecretVersionIdsOutput, *awserr.Error) {
	r, sec, err := s.find(c, in.SecretId)
	if err != nil {
		return nil, err
	}
	arn := sec.arn(r.scope).String()
	all := in.IncludeDeprecated != nil && *in.IncludeDeprecated
	var ids []string
	for _, id := range sec.versionsByAge() {
		if all || sec.labels(id) != nil {
			ids = append(ids, id)
		}
	}
	p, err := page(ids, func(id string) string { return instantKey(sec.Versions[id].Created) + "\x00" + id }, ascending, "versions/"+arn, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	out := &secretsmanagerapi.ListSecretVersionIdsOutput{
		ARN: new(arn), Name: new(sec.Name), NextToken: p.next,
		Versions: make([]secretsmanagerapi.SecretVersionsListEntry, 0, len(p.items)),
	}
	for _, id := range p.items {
		v := sec.Versions[id]
		key := defaultKeyName
		if v.KMSKeyID != nil {
			key = *v.KMSKeyID
		}
		out.Versions = append(out.Versions, secretsmanagerapi.SecretVersionsListEntry{
			VersionId: new(v.ID), VersionStages: sec.labels(v.ID), CreatedDate: new(v.Created.Wire()),
			LastAccessedDate: wireOf(v.Accessed), KmsKeyIds: []string{key},
		})
	}
	return out, nil
}

// updateSecretVersionStage moves a staging label onto a version, or takes
// it off one.
func (s *Service) updateSecretVersionStage(_ context.Context, c *kernel.Call, in *secretsmanagerapi.UpdateSecretVersionStageInput) (*secretsmanagerapi.UpdateSecretVersionStageOutput, *awserr.Error) {
	var m stageMove
	switch {
	case in.MoveToVersionId != nil:
		m = moveLabel{To: *in.MoveToVersionId, From: in.RemoveFromVersionId}
	case in.RemoveFromVersionId != nil:
		m = removeLabel{From: *in.RemoveFromVersionId}
	default:
		return nil, errNoStageTarget()
	}
	r, sec, err := s.update(c, in.SecretId, func(sec *Secret) *awserr.Error { return sec.moveStage(in.VersionStage, m) })
	if err != nil {
		return nil, err
	}
	return &secretsmanagerapi.UpdateSecretVersionStageOutput{ARN: new(sec.arn(r.scope).String()), Name: new(sec.Name)}, nil
}
