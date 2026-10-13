package kernel

import (
	"log/slog"

	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// ServiceDeps is what wire.go hands every service constructor. Settings of
// one service are its own Options struct, with usable zero values. Blob is
// always set; Docker is nil when containers are disabled.
type ServiceDeps struct {
	Kernel   *Kernel
	Binder   *Binder
	Clock    *clock.Clock
	IDs      *ids.Source
	Blob     *blob.Store
	Identity *identity.Store
	Docker   *docker.Client
	Log      *slog.Logger
}
