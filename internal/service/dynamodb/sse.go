package dynamodb

import (
	"context"
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// SSE is server-side encryption with a KMS key.
type SSE struct {
	KMSKeyARN string `json:"kmsKeyArn"`
}

// enablesSSE reports whether a CreateTable's SSE specification turns on
// encryption with a KMS key.
func enablesSSE(sse *dynamodbapi.SSESpecification) bool {
	return sse != nil && sse.Enabled != nil && *sse.Enabled
}

// sseChange is what an UpdateTable's SSE specification does: nothing,
// switch to the AWS owned key, or encrypt with a KMS key.
type sseChange int

const (
	sseKept sseChange = iota
	sseOff
	sseKMS
)

func sseChangeOf(sse *dynamodbapi.SSESpecification) sseChange {
	switch {
	case sse == nil:
		return sseKept
	case sse.Enabled != nil && !*sse.Enabled:
		return sseOff
	case sse.Enabled != nil || sse.KMSMasterKeyId != nil:
		return sseKMS
	}
	return sseKept
}

// managedKey is the alias of the AWS managed key DynamoDB encrypts with
// when an SSE specification names no key.
const managedKey = "alias/aws/dynamodb"

// encryption is the encryption an SSE specification naming key enables:
// the ARN of the key KMS resolves it to, as AWS's KMSMasterKeyArn names
// the key, never an alias. No key is the AWS managed key, which KMS mints
// behind alias/aws/dynamodb on first use, so the Terraform provider's
// comparison with DescribeKey of that alias matches (tf-storage.md 3.2).
// The DescribeKey runs as forward access charged to the caller. A key KMS
// does not find is refused as DynamoDB refuses it; a server without KMS
// bound describes the key by kmsKeyARN instead.
func (s *Service) encryption(ctx context.Context, c *kernel.Call, key *string) (*SSE, *awserr.Error) {
	id := managedKey
	if key != nil && *key != "" {
		id = *key
	}
	describe := kmsapi.OpDescribeKey
	if _, modeled := s.kernel.Registry().Operation(describe.Descriptor().ID); !modeled {
		return &SSE{KMSKeyARN: kmsKeyARN(c.Scope(), key)}, nil
	}
	out, err := kernel.Invoke(ctx, s.kernel, c, describe, kernel.Target{Scope: c.Scope(), As: kernel.AsCaller{}, Via: ref.KMSForward},
		&kmsapi.DescribeKeyInput{KeyId: id})
	switch {
	case err == nil && out.KeyMetadata != nil && out.KeyMetadata.Arn != nil:
		return &SSE{KMSKeyARN: *out.KeyMetadata.Arn}, nil
	case err == nil:
		return nil, awserr.Internalf(&KeyARNError{Key: id})
	case err.ID.Shape == kmsapi.IdentityNotFoundException().Shape:
		return nil, errKeyNotFound(id)
	case err.ID.Shape == awserr.NotImplemented().Shape:
		return &SSE{KMSKeyARN: kmsKeyARN(c.Scope(), key)}, nil
	}
	return nil, err
}

// KeyARNError is a DescribeKey answer without the key's ARN.
type KeyARNError struct{ Key string }

func (e *KeyARNError) Error() string {
	return fmt.Sprintf("KMS answered DescribeKey of %q without KeyMetadata.Arn, so the table's KMSMasterKeyArn is unknown; nothing was created or changed", e.Key)
}

// errKeyNotFound refuses an SSE specification naming a key KMS does not
// find. The text is UNVERIFIED against AWS.
func errKeyNotFound(key string) *awserr.Error {
	return dynamodbapi.ErrValidationException("Invalid KMSMasterKeyId: KMS key " + key + " was not found")
}

// kmsKeyARN describes an SSE specification's key without KMS, for a server
// that does not bind it: an ARN as given, an alias or key id as its ARN in
// scope, and no key as the AWS managed key's alias.
func kmsKeyARN(scope aws.Scope, key *string) string {
	resource := managedKey
	switch {
	case key == nil || *key == "":
	case strings.HasPrefix(*key, "arn:"):
		return *key
	case strings.HasPrefix(*key, "alias/"):
		resource = *key
	default:
		resource = "key/" + *key
	}
	return aws.ARN{Partition: scope.Partition.String(), Service: "kms", Region: scope.Region.String(), Account: scope.Account.String(), Resource: resource}.String()
}
