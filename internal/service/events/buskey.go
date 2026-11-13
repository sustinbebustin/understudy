package events

import (
	"context"
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// servicePrincipal is EventBridge's service principal, which a bus's KMS
// key policy must allow.
const servicePrincipal = "events.amazonaws.com"

// kmsClient is the client EventBridge calls KMS through, whose SDK v2
// suffix ends the KMS message EventBridge relays for a key KMS does not
// find (testdata/parity/events/EBB/05-missing-key.json).
var kmsClient = awserr.SDKv2("Kms") //nolint:gochecknoglobals // a constant value; Go has no struct constants.

// generateDataKey is what a bus's key policy must allow EventBridge
// (eventbridge-buses.md 3.1, EBB-05).
func generateDataKey() registry.IAMAction {
	return registry.IAMAction{Prefix: "kms", Name: "GenerateDataKey"}
}

// checkBusKey checks the KmsKeyIdentifier key that CreateEventBus or
// UpdateEventBus sends for the bus, and returns the identifier the bus
// stores and echoes (busKeyIdentifier). It runs before the write takes its
// cell, since an internal call under a cell lock is a bug (ARCHITECTURE.md
// 8 rule 1).
//
// KMS is asked to describe the key as EventBridge's platform: the lookup is
// not an authorization, so it runs under every iam.enforce mode, and a key
// or alias KMS does not find is refused (errBusKeyMissing). Then the key
// policy is evaluated for events.amazonaws.com's kms:GenerateDataKey on
// the key the identifier resolves to: refused under iam.enforce=enforce
// (errBusKeyDenied), journaled under advise, not asked under off
// (kernel.AuthorizeAs). The evaluation carries aws:SourceArn the bus and
// aws:SourceAccount its account, inferred from the confused-deputy keys
// other service principals send. A server without KMS bound stores the
// identifier unchecked, since nothing could resolve it.
func (s *Service) checkBusKey(ctx context.Context, c *kernel.Call, bus aws.ARN, key string) (string, *awserr.Error) {
	stored := busKeyIdentifier(c.Scope(), key)
	describe := kmsapi.OpDescribeKey
	if _, modeled := s.k.Registry().Operation(describe.Descriptor().ID); !modeled {
		return stored, nil
	}
	out, err := kernel.Invoke(ctx, s.k, c, describe, kernel.Target{Scope: c.Scope(), As: kernel.AsPlatform{Name: servicePrincipal}, Via: ref.KMSForward},
		&kmsapi.DescribeKeyInput{KeyId: key})
	switch {
	case err == nil:
	case err.ID.Shape == kmsapi.IdentityNotFoundException().Shape, err.ID.Shape == kmsapi.IdentityInvalidArnException().Shape:
		return "", errBusKeyMissing(kmsClient.Message(err, string(s.ids.RequestID())))
	case err.ID.Shape == awserr.NotImplemented().Shape:
		return stored, nil
	default:
		return "", err
	}
	if out.KeyMetadata == nil || out.KeyMetadata.Arn == nil {
		return "", awserr.Internalf(&BusKeyError{Key: key, Reason: "KMS answered DescribeKey without KeyMetadata.Arn"})
	}
	resolved, perr := aws.ParseARN(*out.KeyMetadata.Arn)
	if perr != nil {
		return "", awserr.Internalf(&BusKeyError{Key: key, Reason: fmt.Sprintf("KMS answered DescribeKey with KeyMetadata.Arn %q, which is not an ARN: %v", *out.KeyMetadata.Arn, perr)})
	}
	as := kernel.AsService{Name: servicePrincipal, Source: bus, SourceAccount: c.Scope().Account}
	q := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: generateDataKey(), Resource: kernel.ResourceARN{ARN: resolved}}}}
	if err := kernel.AuthorizeAs(ctx, c, as, q); err != nil {
		if _, denied := err.Origin.(awserr.Denial); denied {
			return "", errBusKeyDenied(resolved.String())
		}
		return "", err
	}
	return stored, nil
}

// busKeyIdentifier is a KmsKeyIdentifier as a bus of scope stores and
// echoes it: a key or alias ARN as sent, an alias name as its alias ARN
// (not resolved to the key) and a key id as its key ARN
// (testdata/parity/events/EBB/05-key-id.json, 05-alias.json,
// 05-alias-arn.json).
func busKeyIdentifier(scope aws.Scope, key string) string {
	if strings.HasPrefix(key, "arn:") {
		return key
	}
	resource := "key/" + key
	if strings.HasPrefix(key, "alias/") {
		resource = key
	}
	return aws.ARN{Partition: scope.Partition.String(), Service: "kms", Region: scope.Region.String(), Account: scope.Account.String(), Resource: resource}.String()
}

// BusKeyError is a KMS answer about a bus's key that understudy's KMS never
// gives, so the key was neither checked nor stored.
type BusKeyError struct {
	Key    string // the KmsKeyIdentifier sent
	Reason string
}

func (e *BusKeyError) Error() string {
	return "events: checking the event bus KmsKeyIdentifier " + e.Key + ": " + e.Reason + "; the bus was not created or changed"
}
