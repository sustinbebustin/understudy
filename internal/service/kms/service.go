package kms

import (
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerKeyDeletion fires when a key's waiting period ends; the key and its
// aliases are then deleted. The key's ID is the key ARN.
const TimerKeyDeletion kernel.TimerKind = "kms.key-deletion"

// Service is AWS KMS: one state cell per account and region.
type Service struct {
	k     *kernel.Kernel
	ids   *ids.Source
	cells *store.Partitioned[aws.Scope, State]
	// deletion is TimerKeyDeletion as registered with the kernel.
	deletion kernel.HandledKind
}

// New binds the KMS operations understudy implements and registers the
// deletion timer. Every other KMS operation stays unbound and answers 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	s := &Service{
		k:     d.Kernel,
		ids:   d.IDs,
		cells: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState),
	}
	s.deletion = d.Kernel.Timers().Handle(TimerKeyDeletion, s.onDeletion)
	b := kmsapi.NewBinder(d.Binder)
	s.bindKeys(b)
	s.bindPolicies(b)
	s.bindRotation(b)
	s.bindAliases(b)
	s.bindTags(b)
	s.bindCrypto(b)
	return s
}

// Switches lists the behaviors chosen without AWS-recorded evidence.
func (*Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{pendingCryptoError().Entry(), crossAccountManagementError().Entry()}
}

// crossAccountManagementError is the error a key management operation
// (one AWS documents with "Cross-account use: No") gets for another
// account's key ARN. AWS's answer is unrecorded; understudy answers the
// NotFoundException of a key the caller's account does not have. Each key
// management operation's API reference page carries the "Cross-account
// use: No" line; Ref names one of them.
func crossAccountManagementError() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "kms.cross-account.management-error",
		Value: "NotFoundException",
		Label: evidence.Inferred,
		Ref:   "https://docs.aws.amazon.com/kms/latest/APIReference/API_GetKeyPolicy.html",
	}
}

// pendingCryptoError is the error a cryptographic operation gets from a key
// pending deletion: AWS's key-state page documents both
// KMSInvalidStateException and DisabledException with the pending text;
// understudy answers the first.
func pendingCryptoError() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "kms.pending-deletion.crypto-error",
		Value: "KMSInvalidStateException",
		Label: evidence.AWSDoc,
		Ref:   "docs/research/iam-sts-kms.md#56-key-states-deletion-and-the-state-matrix",
	}
}
