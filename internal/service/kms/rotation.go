package kms

import (
	"context"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// defaultRotationDays is the rotation period when EnableKeyRotation gives
// none, and the period of AWS managed keys.
const defaultRotationDays = 365

// bindRotation binds the automatic rotation status. Rotation itself (new
// key material each period) is not performed; the status, period and next
// date are stored and reported.
func (s *Service) bindRotation(b kmsapi.Binder) {
	stored := registry.StoredOnly{Note: "the rotation status and period are stored and reported; key material is never rotated"}
	b.EnableKeyRotation(s.enableKeyRotation, stored)
	b.DisableKeyRotation(s.disableKeyRotation, stored)
	b.GetKeyRotationStatus(s.getKeyRotationStatus, registry.Full{})
}

func (s *Service) enableKeyRotation(_ context.Context, c *kernel.Call, in *kmsapi.EnableKeyRotationInput) (*kmsapi.EnableKeyRotationOutput, *awserr.Error) {
	period := int32(defaultRotationDays)
	if in.RotationPeriodInDays != nil {
		period = *in.RotationPeriodInDays
	}
	_, err := updateKey(s, c, in.KeyId, usable, func(k Key, _ string, now clock.Instant, _ *kernel.Tx) (Key, *awserr.Error) {
		since := now
		if k.Rotation != nil {
			// Rotation already on dates its next rotation from when it was
			// turned on; a repeated enable changes only the period.
			since = k.Rotation.Since
		}
		k.Rotation = &Rotation{PeriodDays: period, Since: since}
		return k, nil
	})
	if err != nil {
		return nil, err
	}
	return &kmsapi.EnableKeyRotationOutput{}, nil
}

func (s *Service) disableKeyRotation(_ context.Context, c *kernel.Call, in *kmsapi.DisableKeyRotationInput) (*kmsapi.DisableKeyRotationOutput, *awserr.Error) {
	_, err := updateKey(s, c, in.KeyId, usable, func(k Key, _ string, _ clock.Instant, _ *kernel.Tx) (Key, *awserr.Error) {
		k.Rotation = nil
		return k, nil
	})
	if err != nil {
		return nil, err
	}
	return &kmsapi.DisableKeyRotationOutput{}, nil
}

// getKeyRotationStatus reports false while the key is pending deletion,
// keeping the stored status for a cancelled deletion, and true with the
// default period for AWS managed keys (iam-sts-kms.md 5.5). The period is
// always returned when rotation is on, since Terraform diffs on it
// (tf-storage.md 4.3).
func (s *Service) getKeyRotationStatus(_ context.Context, c *kernel.Call, in *kmsapi.GetKeyRotationStatusInput) (*kmsapi.GetKeyRotationStatusOutput, *awserr.Error) {
	k, err := readKey(s, c, in.KeyId, keyOnly, anyAccount)
	if err != nil {
		return nil, err
	}
	out := &kmsapi.GetKeyRotationStatusOutput{KeyId: new(k.arn)}
	rot := k.Rotation
	if k.Manager == kmsapi.KeyManagerTypeAws {
		rot = &Rotation{PeriodDays: defaultRotationDays, Since: k.Created}
	}
	if _, pending := k.Lifecycle.(pendingDeletion); pending || rot == nil {
		return out, nil
	}
	out.KeyRotationEnabled = true
	out.RotationPeriodInDays = new(rot.PeriodDays)
	out.NextRotationDate = new(rot.next(c.Admitted()).Wire())
	return out, nil
}

// next is the first rotation date after now: Since plus the smallest whole
// number of periods, at least one, that ends after now. Rotation is never
// performed, so the date moves on by periods as the clock passes each one
// rather than staying in the past.
func (r Rotation) next(now clock.Instant) clock.Instant {
	period := time.Duration(r.PeriodDays) * day
	n := int64(max(now.Sub(r.Since)/period, 0)) + 1
	return r.Since.Add(time.Duration(n) * period)
}
