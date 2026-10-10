package server

import (
	"log/slog"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
)

// wiring is the emulator constructed: every part in the order the
// dependencies demand, with nothing registered in init().
type wiring struct {
	kernel   *kernel.Kernel
	journal  *journal.Journal
	edge     *edge.Handler
	services []string
}

// wire builds the registry, the kernel with its clock, journal, faults,
// access recorder and identity store, the services, and the front door.
// Each service constructor gets kernel.ServiceDeps and its Options; this
// file is the only place that names every service and codec.
func wire(cfg Config, log *slog.Logger, admin *health) (*wiring, error) {
	src := ids.NewRandom()
	if cfg.Seed != nil {
		src = ids.NewSeeded(*cfg.Seed)
	}
	timers := kernel.NewTimers()
	clk, err := clock.New(clockOptions(cfg.ClockStart, timers))
	if err != nil {
		return nil, err
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		return nil, err
	}
	reg, err := registry.New(sqsapi.Service())
	if err != nil {
		return nil, err
	}
	idStore := identity.New(src, aws.DefaultAccount())
	k, err := kernel.New(kernel.Options{
		Registry: reg,
		Clock:    clk,
		Timers:   timers,
		IDs:      src,
		Journal:  j,
		Faults:   fault.New(cfg.Seed, src, kernel.NewCatalogue(reg, throttles())),
		Access:   &access.Recorder{},
		Identity: idStore,
		Log:      log,
	})
	if err != nil {
		return nil, err
	}
	deps := func(svc *registry.Service) kernel.ServiceDeps {
		return kernel.ServiceDeps{Kernel: k, Binder: k.Binder(svc), Clock: clk, IDs: src, Identity: idStore, Log: log}
	}
	sqs.New(deps(sqsapi.Service()), sqs.Options{})
	if err := k.BindErr(); err != nil {
		return nil, err
	}
	front, err := edge.New(edge.Options{
		Kernel:                k,
		Registry:              reg,
		Identity:              idStore,
		Regions:               aws.KnownRegions(),
		Codecs:                map[ref.ProtocolID]edge.Codec{ref.AWSJSON10: JSONCodec(awsjson.V10), ref.AWSJSON11: JSONCodec(awsjson.V11)},
		S3BaseDomains:         []string{"localhost", "s3.localhost"},
		Listener:              ref.ListenerMain,
		Admin:                 admin,
		Unclaimed:             ref.AWSJSON10,
		UnknownOperationError: unknownOperationErrors,
		Log:                   log,
	})
	if err != nil {
		return nil, err
	}
	return &wiring{kernel: k, journal: j, edge: front, services: []string{string(sqsapi.Service().ID)}}, nil
}

// clockOptions maps the start setting onto the clock.
func clockOptions(start ClockStart, timers *kernel.Timers) clock.Options {
	o := clock.Options{Mode: clock.Running{}, Timers: timers.Scheduler()}
	if f, ok := start.(ClockFrozen); ok {
		o.Mode = clock.Frozen{}
		if !f.At.IsZero() {
			o.Start = clock.FromWire(f.At)
		}
	}
	return o
}

// throttles are the services' overlay throttleError identities.
func throttles() map[ref.ServiceID]awserr.Identity {
	return map[ref.ServiceID]awserr.Identity{sqsapi.Service().ID: sqsapi.ThrottleError()}
}

// unknownOperationErrors are the services' overlay unknownOperationError
// identities per protocol family.
func unknownOperationErrors(svc ref.ServiceID, f awserr.Family) (awserr.Identity, bool) {
	if svc == sqsapi.Service().ID {
		return sqsapi.UnknownOperationError(f)
	}
	var none awserr.Identity
	return none, false
}
