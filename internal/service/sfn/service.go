package sfn

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Options are the Step Functions settings. There are none yet; the zero
// value is AWS's behavior.
type Options struct{}

// Service is AWS Step Functions: one state cell per account and region.
type Service struct {
	cells *store.Partitioned[aws.Scope, State]
}

// New returns the service. It binds no operation yet, so every Step
// Functions operation answers 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	return &Service{cells: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState)}
}

// AcceptedFeatures is the set of definition features whose runtime has
// landed; Create, Update and Validate parse with it, so any other construct
// is refused with code UNSUPPORTED_BY_UNDERSTUDY. None has landed yet.
func AcceptedFeatures() asl.Features { return asl.NewFeatures() }
