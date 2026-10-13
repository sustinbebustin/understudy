package logs

import (
	"context"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// checkKey refuses a CreateLogGroup kmsKeyId that is not the key ARN of an
// enabled symmetric encryption key, as AWS refuses it with
// InvalidParameterException: the model documents kmsKeyId as the key's ARN
// and CloudWatch Logs as supporting only symmetric keys, so a bare key ID or
// an alias is refused before KMS is asked. The DescribeKey runs as forward
// access charged to the caller, before any cell is held. A server without
// KMS bound accepts a key ARN as given, since nothing could resolve it.
func (s *Service) checkKey(ctx context.Context, c *kernel.Call, key string) *awserr.Error {
	if !isKeyARN(key) {
		return errKMSKey()
	}
	describe := kmsapi.OpDescribeKey
	if _, modeled := s.k.Registry().Operation(describe.Descriptor().ID); !modeled {
		return nil
	}
	out, err := kernel.Invoke(ctx, s.k, c, describe, kernel.Target{Scope: c.Scope(), As: kernel.AsCaller{}, Via: ref.KMSForward},
		&kmsapi.DescribeKeyInput{KeyId: key})
	switch {
	case err == nil && usableKey(out.KeyMetadata):
		return nil
	case err == nil:
		return errKMSKey()
	case err.ID.Shape == kmsapi.IdentityNotFoundException().Shape, err.ID.Shape == kmsapi.IdentityInvalidArnException().Shape:
		return errKMSKey()
	case err.ID.Shape == awserr.NotImplemented().Shape:
		return nil
	}
	return err
}

// isKeyARN reports whether key has the form
// arn:<partition>:kms:<region>:<account>:key/<key-id>.
func isKeyARN(key string) bool {
	a, err := aws.ParseARN(key)
	if err != nil || a.Service != "kms" || a.Region == "" || a.Account == "" {
		return false
	}
	id, ok := strings.CutPrefix(a.Resource, "key/")
	return ok && id != ""
}

// usableKey reports whether a key can encrypt a log group: enabled, of spec
// SYMMETRIC_DEFAULT and usage ENCRYPT_DECRYPT.
func usableKey(m *kmsapi.KeyMetadata) bool {
	return m != nil &&
		m.KeyState != nil && *m.KeyState == kmsapi.KeyStateEnabled &&
		m.KeySpec != nil && *m.KeySpec == kmsapi.KeySpecSymmetricDefault &&
		m.KeyUsage != nil && *m.KeyUsage == kmsapi.KeyUsageTypeEncryptDecrypt
}
