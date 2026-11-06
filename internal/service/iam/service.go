package iam

import (
	"log/slog"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is AWS IAM: one state cell per account, since IAM is global.
type Service struct {
	ids       *ids.Source
	identity  *identity.Store
	log       *slog.Logger
	codec     jsonx.Codec
	cells     *store.Partitioned[aws.AccountID, State]
	catalogue policycatalogue.Loader
	// bindings names the operations a denial can answer, for the switches.
	bindings *kernel.Bindings
}

// New binds the IAM operations understudy implements. Every other IAM
// operation stays unbound and answers 501.
func New(d kernel.ServiceDeps, opts Options) *Service {
	codec := jsonx.NewCodec()
	s := &Service{
		ids:       d.IDs,
		identity:  d.Identity,
		log:       d.Log,
		codec:     codec,
		cells:     store.NewPartitioned(d.Kernel.Gate(), store.Codec{Marshal: codec.Marshal, Unmarshal: codec.Unmarshal}, newState),
		catalogue: opts.Catalogue,
		bindings:  d.Kernel.Bindings(),
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
	s.declareAuthorization(d.Binder)
	return s
}

// Switches lists the behaviors of IAM evaluation chosen without
// AWS-recorded evidence: IAM's denial identity (iam-enforcement-surfaces.md
// 12.8) and the evaluation switches (authz.Switches), which IAM reports.
// The rules a policy document is evaluated by govern the operations that
// store one; the grammar and the enforcement order govern every operation
// a denial answers. A missing resource is denied first by the operations
// asked at step 8, and answers not-found first in those that find their
// entity by name (resolvedFirst), which report their own switch.
func (s *Service) Switches() []fidelity.Entry {
	authorized := s.bindings.AuthorizedOps(iamapi.Service())
	// GetRole is AuthorizedInHandler, and asks once its read found the role.
	byName := append(s.bindings.ResolvedFirstOps(iamapi.Service()), iamapi.OpGetRole.Descriptor().ID.String())
	stepEight := slices.DeleteFunc(slices.Clone(authorized), func(op string) bool { return slices.Contains(byName, op) })
	evaluation := authz.SwitchesOf("iam", nil)
	out := make([]fidelity.Entry, 0, 2+len(evaluation))
	out = append(out, authz.RecordedSwitch("iam.denial.identity", "AccessDenied 403 with the IAM namespace", "3.2", "IAM-ENF-10", authz.SessionUnreplayed, authorized))
	for _, e := range evaluation {
		switch {
		case e.Name == "iam.enforce.missing-resource":
			e.Ops = stepEight
		case e.Name == "iam.denial.grammar" || strings.HasPrefix(e.Name, "iam.enforce."):
			e.Ops = authorized
		default:
			e.Ops = documentOps()
		}
		out = append(out, e)
	}
	return append(out, authz.ResolveFirstSwitch("iam", byName))
}

// documentOps are the IAM operations that store a policy document IAM
// evaluation reads: identity policies, trust policies and permissions
// boundaries.
func documentOps() []string {
	return []string{
		"iam.CreatePolicy", "iam.CreatePolicyVersion", "iam.PutUserPolicy", "iam.PutRolePolicy",
		"iam.CreateRole", "iam.UpdateAssumeRolePolicy", "iam.PutUserPermissionsBoundary", "iam.PutRolePermissionsBoundary",
	}
}
