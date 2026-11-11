package sqs

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// kmsClient is the client SQS calls KMS through, whose suffix ends the
// KMS message of a KMS failure (IAM-ENF-29).
var kmsClient = awserr.SDKv1("AWSKMS") //nolint:gochecknoglobals // a constant value; Go has no struct constants.

// dataKey asks KMS for a data key under q's KmsMasterKeyId, as SQS does
// before it stores a message on an SSE-KMS queue, and does nothing for a
// queue without one. The call is forward access charged to the caller
// (ARCHITECTURE.md 16), so under iam.enforce=enforce a caller that may not
// kms:GenerateDataKey on the key is refused; KMS resolves an alias and
// evaluates the key's policy itself. The encryption context is the queue's
// ARN under aws:sqs:arn, as the SQS Developer Guide documents for key
// policies. It runs before the send takes its cell, since an internal
// call under a cell lock is a bug (ARCHITECTURE.md 8 rule 1).
//
// Inferred: AWS reuses a data key for KmsDataKeyReusePeriodSeconds and
// calls KMS only once that ends; understudy asks on every send, so a
// grant revoked or a key disabled takes effect at once.
func (s *Service) dataKey(ctx context.Context, c *kernel.Call, q queueRef) *awserr.Error {
	key, err := readQueue(s, q, func(_ *State, queue *Queue) string { return queue.Attributes.KmsMasterKeyID })
	if err != nil || key == "" {
		return err
	}
	_, err = kernel.Invoke(ctx, s.k, c, kmsapi.OpGenerateDataKey,
		kernel.Target{Scope: q.scope, As: kernel.AsCaller{}, Via: ref.KMSForward},
		&kmsapi.GenerateDataKeyInput{
			KeyId:             key,
			EncryptionContext: map[string]string{"aws:sqs:arn": q.arn().String()},
			KeySpec:           new(kmsapi.DataKeySpecAes256),
		})
	if err != nil {
		return kmsFailure(err, string(s.k.IDs().RequestID()))
	}
	return nil
}

// kmsFailure is SQS's answer for a failed KMS call whose request id is
// requestID: KMS's error under SQS's Kms shape for it, carrying KMS's
// message and the KMS client's suffix. The denial is recorded
// (IAM-ENF-29: KmsAccessDenied, "KMS.AccessDeniedException"); a missing,
// disabled or pending-deletion key answering KmsNotFound, KmsDisabled or
// KmsInvalidState in the same frame is inferred. An understudy bug and any
// other KMS error pass through as they are.
func kmsFailure(err *awserr.Error, requestID string) *awserr.Error {
	if _, internal := err.Origin.(awserr.Internal); internal {
		return err
	}
	switch err.ID.Shape {
	case kmsapi.DenialError().Shape:
		return sqsapi.ErrKmsAccessDenied(kmsClient.Message(err, requestID))
	case kmsapi.IdentityNotFoundException().Shape:
		return sqsapi.ErrKmsNotFound(kmsClient.Message(err, requestID))
	case kmsapi.IdentityDisabledException().Shape:
		return sqsapi.ErrKmsDisabled(kmsClient.Message(err, requestID))
	case kmsapi.IdentityKMSInvalidStateException().Shape:
		return sqsapi.ErrKmsInvalidState(kmsClient.Message(err, requestID))
	}
	return err
}

// sendOps are the operations that ask KMS for a data key.
func sendOps() []string { return []string{"sqs.SendMessage", "sqs.SendMessageBatch"} }

// kmsDeniedText is how a send the queue's key refuses answers: KMS's
// denial under KmsAccessDenied with the KMS client's suffix, recorded for
// SendMessage over JSON with and without x-amzn-query-mode
// (iam-enforcement-surfaces.md 3.2; IAM-ENF-29 29-send, 29-raw).
func kmsDeniedText() fidelity.Entry {
	return authz.RecordedSwitch("sqs.sse-kms.denied-text",
		"KmsAccessDenied, KMS.AccessDeniedException;Sender, 400, KMS's message then "+kmsClient.String(),
		"3.2", "IAM-ENF-29",
		"recorded: SendMessage; inferred: SendMessageBatch, refused whole with the same answer; "+authz.SessionUnreplayed, sendOps())
}

// kmsKeyError is how a send answers a key KMS finds missing, disabled or
// pending deletion: inferred in the frame of the recorded denial
// (kmsDeniedText), under the SQS shapes the model lists for SendMessage.
func kmsKeyError() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name: "sqs.sse-kms.key-error", Value: "KmsNotFound, KmsDisabled or KmsInvalidState, KMS's message then the AWSKMS suffix",
		Label: evidence.Inferred, Ref: refAttributes, Ops: sendOps(),
		Settles: fidelity.NoScenario{Reason: "unrecorded: IAM-ENF-29 records the denial only"},
	}
}

// kmsDataKey is when a send asks KMS for a data key: on every send, where
// AWS reuses one for KmsDataKeyReusePeriodSeconds (sqs.md 4), so a revoked
// grant or a disabled key refuses the next send rather than the first one
// after the period.
func kmsDataKey() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name: "sqs.sse-kms.data-key", Value: "generate-on-every-send",
		Label: evidence.Inferred, Ref: refAttributes, Ops: sendOps(),
		Settles: fidelity.NoScenario{Reason: "unrecorded: IAM-ENF-29 records a first send only"},
	}
}
