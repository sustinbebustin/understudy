package kms

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// defaultWindowDays is ScheduleKeyDeletion's waiting period when none is
// given.
const defaultWindowDays = 30

// day is the unit of KMS waiting periods.
const day = 24 * time.Hour

// bindKeys binds the key lifecycle: create, describe, list, enable,
// disable, describe text and scheduled deletion.
func (s *Service) bindKeys(b kmsapi.Binder) {
	b.CreateKey(s.createKey,
		registry.Partial{Note: "symmetric encryption keys with AWS_KMS origin only: other key specs, key usages, origins, custom key stores and multi-Region keys answer UnsupportedOperationException; the key policy is parsed and stored, the lockout safety check is not run"},
		kernel.WithCheck(checkCreateKey), kernel.WithCheck(func(in *kmsapi.CreateKeyInput) *awserr.Error { return checkTags(in.Tags) }))
	b.DescribeKey(s.describeKey, registry.Full{})
	b.ListKeys(s.listKeys, registry.Full{})
	b.UpdateKeyDescription(s.updateKeyDescription, registry.Full{})
	b.EnableKey(s.enableKey, registry.Full{})
	b.DisableKey(s.disableKey, registry.Full{})
	b.ScheduleKeyDeletion(s.scheduleKeyDeletion, registry.Full{})
	b.CancelKeyDeletion(s.cancelKeyDeletion, registry.Full{})
}

// checkCreateKey refuses the keys understudy cannot back with real
// cryptography rather than create one that would behave differently.
func checkCreateKey(in *kmsapi.CreateKeyInput) *awserr.Error {
	spec := kmsapi.KeySpecSymmetricDefault
	switch {
	case in.KeySpec != nil:
		spec = *in.KeySpec
	case in.CustomerMasterKeySpec != nil:
		spec = kmsapi.KeySpec(*in.CustomerMasterKeySpec)
	}
	// KeyUsage defaults to ENCRYPT_DECRYPT only for a symmetric encryption
	// key; every other spec must name it.
	usage := kmsapi.KeyUsageTypeEncryptDecrypt
	switch {
	case in.KeyUsage != nil:
		usage = *in.KeyUsage
	case spec != kmsapi.KeySpecSymmetricDefault:
		return errKeyUsageRequired()
	}
	// AWS checks the pair before whether it supports the spec, so a
	// mismatch is a ValidationException even for a spec understudy refuses.
	switch {
	case !slices.Contains(keyUsages(spec), usage):
		return kmsapi.ErrValidationException("KeyUsage " + string(usage) + " is not compatible with KeySpec " + string(spec))
	case spec != kmsapi.KeySpecSymmetricDefault:
		return errUnsupported("KeySpec " + string(spec))
	case in.Origin != nil && *in.Origin != kmsapi.OriginTypeAwsKms:
		return errUnsupported("Origin " + string(*in.Origin))
	case in.CustomKeyStoreId != nil || in.XksKeyId != nil:
		return errUnsupported("custom key stores")
	case in.MultiRegion != nil && *in.MultiRegion:
		return errUnsupported("multi-Region keys")
	}
	return nil
}

// keyUsages lists the KeyUsage values AWS accepts with spec (CreateKey's
// KeyUsage documentation): ENCRYPT_DECRYPT, the default, is valid only for
// symmetric, RSA and SM2 keys.
func keyUsages(spec kmsapi.KeySpec) []kmsapi.KeyUsageType {
	switch spec {
	case kmsapi.KeySpecSymmetricDefault:
		return []kmsapi.KeyUsageType{kmsapi.KeyUsageTypeEncryptDecrypt}
	case kmsapi.KeySpecHmac224, kmsapi.KeySpecHmac256, kmsapi.KeySpecHmac384, kmsapi.KeySpecHmac512:
		return []kmsapi.KeyUsageType{kmsapi.KeyUsageTypeGenerateVerifyMac}
	case kmsapi.KeySpecRsa2048, kmsapi.KeySpecRsa3072, kmsapi.KeySpecRsa4096:
		return []kmsapi.KeyUsageType{kmsapi.KeyUsageTypeEncryptDecrypt, kmsapi.KeyUsageTypeSignVerify}
	case kmsapi.KeySpecEccNistP256, kmsapi.KeySpecEccNistP384, kmsapi.KeySpecEccNistP521:
		return []kmsapi.KeyUsageType{kmsapi.KeyUsageTypeSignVerify, kmsapi.KeyUsageTypeKeyAgreement}
	case kmsapi.KeySpecEccSecgP256k1, kmsapi.KeySpecEccNistEdwards25519, kmsapi.KeySpecMlDsa44, kmsapi.KeySpecMlDsa65, kmsapi.KeySpecMlDsa87:
		return []kmsapi.KeyUsageType{kmsapi.KeyUsageTypeSignVerify}
	case kmsapi.KeySpecSm2:
		return []kmsapi.KeyUsageType{kmsapi.KeyUsageTypeEncryptDecrypt, kmsapi.KeyUsageTypeSignVerify, kmsapi.KeyUsageTypeKeyAgreement}
	}
	return nil
}

// maxPolicyChars is the key policy size quota. The model allows 131072
// characters, so the framework never refuses a longer policy; KMS answers
// LimitExceededException past the quota, as CreateKey and PutKeyPolicy
// document ("If the key policy exceeds the length constraint, AWS KMS
// returns a LimitExceededException";
// https://docs.aws.amazon.com/kms/latest/APIReference/API_CreateKey.html,
// https://docs.aws.amazon.com/kms/latest/APIReference/API_PutKeyPolicy.html).
const maxPolicyChars = 32768

// checkPolicy checks a key policy against the size quota, parses it and
// requires a Principal or NotPrincipal in every statement, as CreateKey and
// PutKeyPolicy document; KMS stores it and phase 1 does not enforce it. A
// statement without Action or Resource is accepted, as AWS accepts it (it
// has no effect).
func checkPolicy(policy string) *awserr.Error {
	if n := utf8.RuneCountInString(policy); n > maxPolicyChars {
		return errPolicyTooLong(n, maxPolicyChars)
	}
	doc, err := policydoc.Parse([]byte(policy))
	if err != nil {
		return kmsapi.ErrMalformedPolicyDocumentException(err.Message())
	}
	for i, st := range doc.Statements {
		if st.Principal == nil {
			return errPolicyPrincipal(i, st.Sid)
		}
	}
	return nil
}

func (s *Service) createKey(_ context.Context, c *kernel.Call, in *kmsapi.CreateKeyInput) (*kmsapi.CreateKeyOutput, *awserr.Error) {
	policy := ""
	if in.Policy != nil {
		if err := checkPolicy(*in.Policy); err != nil {
			return nil, err
		}
		policy = *in.Policy
	}
	scope := c.Scope()
	k, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, _ *kernel.Tx) (Key, *awserr.Error) {
		k, err := s.newKey(scope, now, kmsapi.KeyManagerTypeCustomer, deref(in.Description), policy)
		if err != nil {
			return Key{}, err
		}
		if len(in.Tags) > 0 {
			k.Tags = tagMap(nil, in.Tags)
		}
		st.Keys[k.ID] = k
		return k, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(keyARN(scope, k.ID))
	return &kmsapi.CreateKeyOutput{KeyMetadata: metadata(scope, k)}, nil
}

func (s *Service) describeKey(_ context.Context, c *kernel.Call, in *kmsapi.DescribeKeyInput) (*kmsapi.DescribeKeyOutput, *awserr.Error) {
	k, err := readKey(s, c, in.KeyId, keyOrAlias, anyAccount)
	if err != nil {
		return nil, err
	}
	return &kmsapi.DescribeKeyOutput{KeyMetadata: metadata(k.scope, k.Key)}, nil
}

func (s *Service) listKeys(_ context.Context, c *kernel.Call, in *kmsapi.ListKeysInput) (*kmsapi.ListKeysOutput, *awserr.Error) {
	scope := c.Scope()
	keyIDs := readState(s, scope, func(st *State) []string { return slices.Collect(maps.Keys(st.Keys)) })
	p, err := page(listingKeys, keyIDs, func(id string) string { return id }, in.Limit, limits{def: 100, max: 1000}, in.Marker)
	if err != nil {
		return nil, err
	}
	out := &kmsapi.ListKeysOutput{Keys: make([]kmsapi.KeyListEntry, 0, len(p.Items)), NextMarker: p.Next, Truncated: p.Truncated}
	for _, id := range p.Items {
		out.Keys = append(out.Keys, kmsapi.KeyListEntry{KeyId: new(id), KeyArn: new(keyARN(scope, id).String())})
	}
	return out, nil
}

func (s *Service) updateKeyDescription(_ context.Context, c *kernel.Call, in *kmsapi.UpdateKeyDescriptionInput) (*kmsapi.UpdateKeyDescriptionOutput, *awserr.Error) {
	_, err := updateKey(s, c, in.KeyId, notPending, func(k Key, _ string, _ clock.Instant, _ *kernel.Tx) (Key, *awserr.Error) {
		k.Description = in.Description
		return k, nil
	})
	if err != nil {
		return nil, err
	}
	return &kmsapi.UpdateKeyDescriptionOutput{}, nil
}

func (s *Service) enableKey(_ context.Context, c *kernel.Call, in *kmsapi.EnableKeyInput) (*kmsapi.EnableKeyOutput, *awserr.Error) {
	if err := s.setLifecycle(c, in.KeyId, enabled{}); err != nil {
		return nil, err
	}
	return &kmsapi.EnableKeyOutput{}, nil
}

func (s *Service) disableKey(_ context.Context, c *kernel.Call, in *kmsapi.DisableKeyInput) (*kmsapi.DisableKeyOutput, *awserr.Error) {
	if err := s.setLifecycle(c, in.KeyId, disabled{}); err != nil {
		return nil, err
	}
	return &kmsapi.DisableKeyOutput{}, nil
}

// setLifecycle enables or disables a key that is not pending deletion.
func (s *Service) setLifecycle(c *kernel.Call, keyID string, l lifecycle) *awserr.Error {
	_, err := updateKey(s, c, keyID, notPending, func(k Key, _ string, _ clock.Instant, _ *kernel.Tx) (Key, *awserr.Error) {
		k.Lifecycle = l
		return k, nil
	})
	return err
}

// scheduleKeyDeletion starts the waiting period on the emulator clock; the
// deletion timer deletes the key and its aliases when it ends.
func (s *Service) scheduleKeyDeletion(_ context.Context, c *kernel.Call, in *kmsapi.ScheduleKeyDeletionInput) (*kmsapi.ScheduleKeyDeletionOutput, *awserr.Error) {
	window := int32(defaultWindowDays)
	if in.PendingWindowInDays != nil {
		window = *in.PendingWindowInDays
	}
	k, err := updateKey(s, c, in.KeyId, notPending, func(k Key, arn string, now clock.Instant, tx *kernel.Tx) (Key, *awserr.Error) {
		p := pendingDeletion{DeletionDate: now.Add(time.Duration(window) * day), WindowDays: window}
		k.Lifecycle = p
		tx.Arm(s.deletion, arn, p.DeletionDate)
		return k, nil
	})
	if err != nil {
		return nil, err
	}
	out := &kmsapi.ScheduleKeyDeletionOutput{
		KeyId:               new(k.arn),
		KeyState:            new(kmsapi.KeyStatePendingDeletion),
		PendingWindowInDays: new(window),
	}
	if p, ok := k.Lifecycle.(pendingDeletion); ok {
		out.DeletionDate = new(p.DeletionDate.Wire())
	}
	return out, nil
}

// cancelKeyDeletion ends the waiting period; the key comes back disabled.
func (s *Service) cancelKeyDeletion(_ context.Context, c *kernel.Call, in *kmsapi.CancelKeyDeletionInput) (*kmsapi.CancelKeyDeletionOutput, *awserr.Error) {
	k, err := updateKey(s, c, in.KeyId, onlyPending, func(k Key, arn string, _ clock.Instant, tx *kernel.Tx) (Key, *awserr.Error) {
		k.Lifecycle = disabled{}
		tx.Disarm(s.deletion, arn)
		return k, nil
	})
	if err != nil {
		return nil, err
	}
	return &kmsapi.CancelKeyDeletionOutput{KeyId: new(k.arn)}, nil
}

// onDeletion deletes a key whose waiting period ended, with every alias
// that names it. A key whose deletion was cancelled is left alone.
func (s *Service) onDeletion(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	scope, id, ok := parseKeyARN(key.ID)
	if !ok {
		return
	}
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		p, ok := st.Keys[id].Lifecycle.(pendingDeletion)
		if !ok || due.Before(p.DeletionDate) {
			tx.Unchanged()
			return struct{}{}, nil
		}
		delete(st.Keys, id)
		maps.DeleteFunc(st.Aliases, func(_ string, a Alias) bool { return a.KeyID == id })
		return struct{}{}, nil
	})
	if err != nil {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "kms key deletion failed; the key stays pending deletion until a snapshot load re-arms its timer",
			slog.String("key", key.ID), slog.String("error", err.Message))
	}
}

// located is a key with the scope it lives in and its ARN.
type located struct {
	Key
	scope aws.Scope
	arn   string
}

func locate(scope aws.Scope, k Key) located {
	return located{Key: k, scope: scope, arn: keyARN(scope, k.ID).String()}
}

// readKey returns the key keyID names, in any state, and touches it.
func readKey(s *Service, c *kernel.Call, keyID string, forms refForms, rc reach) (located, *awserr.Error) {
	r, err := parseKeyRef(c, keyID, forms)
	if err != nil {
		return located{}, err
	}
	if err := rc.check(c, r); err != nil {
		return located{}, err
	}
	k, err := s.keyFor(c, r, anyState)
	if err != nil {
		return located{}, err
	}
	c.Touch(keyARN(r.scope(), k.ID))
	return locate(r.scope(), k), nil
}

// updateKey replaces the caller's customer managed key keyID (a key id or
// key ARN) names with what fn returns, once the operation's state rule
// accepts it, and returns the new key. fn gets the key's ARN for timer ids.
func updateKey(s *Service, c *kernel.Call, keyID string, rule stateRule, fn func(k Key, arn string, now clock.Instant, tx *kernel.Tx) (Key, *awserr.Error)) (located, *awserr.Error) {
	r, err := parseKeyID(c, keyID)
	if err != nil {
		return located{}, err
	}
	if err := ownAccount.check(c, r); err != nil {
		return located{}, err
	}
	k, err := withKey(s, c, r, rule, func(st *State, k Key, now clock.Instant, tx *kernel.Tx) (Key, *awserr.Error) {
		arn := keyARN(r.scope(), k.ID).String()
		if err := customerOnly(k, arn); err != nil {
			return Key{}, err
		}
		k, err := fn(k, arn, now, tx)
		if err != nil {
			return Key{}, err
		}
		st.Keys[k.ID] = k
		return k, nil
	})
	if err != nil {
		return located{}, err
	}
	c.Touch(keyARN(r.scope(), k.ID))
	return locate(r.scope(), k), nil
}

// readState runs fn on the state of scope without creating its cell; a
// scope with no cell reads as empty.
func readState[R any](s *Service, scope aws.Scope, fn func(st *State) R) R {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		st := newState(scope)
		return fn(&st)
	}
	return store.Read(cell, fn)
}

// metadata renders a key as KeyMetadata: DescribeKey's fields for a
// symmetric AWS_KMS key, DeletionDate only while pending deletion.
func metadata(scope aws.Scope, k Key) *kmsapi.KeyMetadata {
	m := &kmsapi.KeyMetadata{
		AWSAccountId:          new(scope.Account.String()),
		KeyId:                 k.ID,
		Arn:                   new(keyARN(scope, k.ID).String()),
		CreationDate:          new(k.Created.Wire()),
		Enabled:               k.Lifecycle.keyState() == kmsapi.KeyStateEnabled,
		Description:           new(k.Description),
		KeyUsage:              new(kmsapi.KeyUsageTypeEncryptDecrypt),
		KeyState:              new(k.Lifecycle.keyState()),
		Origin:                new(kmsapi.OriginTypeAwsKms),
		KeyManager:            new(k.Manager),
		CustomerMasterKeySpec: new(kmsapi.CustomerMasterKeySpecSymmetricDefault),
		KeySpec:               new(kmsapi.KeySpecSymmetricDefault),
		EncryptionAlgorithms:  []kmsapi.EncryptionAlgorithmSpec{kmsapi.EncryptionAlgorithmSpecSymmetricDefault},
		MultiRegion:           new(false),
		CurrentKeyMaterialId:  new(k.MaterialID),
	}
	if p, ok := k.Lifecycle.(pendingDeletion); ok {
		m.DeletionDate = new(p.DeletionDate.Wire())
	}
	return m
}

// parseKeyARN reverses keyARN for a deletion timer's ID.
func parseKeyARN(s string) (aws.Scope, string, bool) {
	a, err := aws.ParseARN(s)
	if err != nil {
		return aws.Scope{}, "", false
	}
	id, ok := strings.CutPrefix(a.Resource, "key/")
	if !ok {
		return aws.Scope{}, "", false
	}
	scope, err := store.ScopeKeys().Decode(a.Account + "/" + a.Region)
	if err != nil {
		return aws.Scope{}, "", false
	}
	return scope, id, true
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
