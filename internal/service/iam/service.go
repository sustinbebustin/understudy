package iam

import (
	"log/slog"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is AWS IAM: one state cell per account, since IAM is global.
type Service struct {
	ids      *ids.Source
	identity *identity.Store
	log      *slog.Logger
	codec    jsonx.Codec
	cells    *store.Partitioned[aws.AccountID, State]
}

// New binds the IAM operations understudy implements. Every other IAM
// operation stays unbound and answers 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	codec := jsonx.NewCodec()
	s := &Service{
		ids:      d.IDs,
		identity: d.Identity,
		log:      d.Log,
		codec:    codec,
		cells:    store.NewPartitioned(d.Kernel.Gate(), store.Codec{Marshal: codec.Marshal, Unmarshal: codec.Unmarshal}, newState),
	}
	b := iamapi.NewBinder(d.Binder)
	s.bindRoles(b)
	s.bindInline(b)
	s.bindAttachments(b)
	s.bindPolicies(b)
	s.bindUsers(b)
	s.bindAccessKeys(b)
	s.bindInstanceProfiles(b)
	s.bindTags(b)
	return s
}

// Switches lists no behaviors: every IAM choice without AWS evidence is a
// documented decision of the package, none a switchable behavior.
func (*Service) Switches() []fidelity.Entry { return nil }
