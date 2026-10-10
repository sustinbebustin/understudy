package kernel

import (
	"context"
	"log/slog"

	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// ServiceDeps is what wire.go hands every service constructor. Settings of
// one service are its own Options struct, with usable zero values. Blob,
// Docker and Topology are nil until the services that need them are wired.
type ServiceDeps struct {
	Kernel   *Kernel
	Binder   *Binder
	Clock    *clock.Clock
	IDs      *ids.Source
	Blob     *blob.Store
	Identity *identity.Store
	Docker   *docker.Client
	Topology func(ctx context.Context) (topology.Topology, error)
	Log      *slog.Logger
}
