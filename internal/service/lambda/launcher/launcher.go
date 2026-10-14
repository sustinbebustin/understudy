package launcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// Options configures a Launcher.
type Options struct {
	// Ownership labels every container so sweeps find it.
	Ownership docker.Ownership
	// Test adds the understudy.dev/test=1 label; only test harnesses set it.
	Test bool
	// Topology decides the addresses, extra hosts and network function
	// containers get, and whether code is bind-mounted or copied.
	Topology topology.Topology
	// EndpointPort is the port of understudy's listener reachable from
	// containers; AWS_ENDPOINT_URL is http://<function endpoint host>:<port>.
	EndpointPort uint16
	// Logger receives exit-watch notices; nil discards them.
	Logger *slog.Logger
}

// Container is a started function container.
type Container struct {
	ID   string
	Name string
}

// Launcher creates, starts, watches and removes function containers. It is
// safe for concurrent use.
type Launcher struct {
	client       *docker.Client
	ownership    docker.Ownership
	test         bool
	addrs        topology.Addresses
	endpointPort uint16
	log          *slog.Logger

	pulls flights[imageKey, Image]

	mu sync.Mutex
	// delivery starts as the topology's and turns to tarCopy for good once
	// the daemon refuses a bind mount of the code store.
	delivery delivery
	live     map[string]tracked // created and not yet removed or reported, by container ID
	watcher  *watcher
}

// tracked is a container the exit watch covers.
type tracked struct {
	arch Architecture
	// started: ContainerStart succeeded. Until then the daemon reports the
	// container as not running, so a not-running wait would return at once.
	started bool
	// waiter is the watch whose per-container wait covers the container,
	// or nil while none does.
	waiter *watcher
}

// New validates opts. A topology the launcher cannot serve returns its
// *topology.UnsupportedError; an incomplete ownership its
// *docker.OwnershipError.
func New(client *docker.Client, opts Options) (*Launcher, error) {
	if client == nil {
		return nil, errors.New("launcher: Docker client is nil")
	}
	if err := opts.Ownership.Validate(); err != nil {
		return nil, err
	}
	if opts.Topology == nil {
		return nil, errors.New("launcher: Options.Topology is nil; detect the container topology first")
	}
	addrs, err := opts.Topology.Addresses()
	if err != nil {
		return nil, err
	}
	if opts.EndpointPort == 0 {
		return nil, errors.New("launcher: Options.EndpointPort is 0; pass the port of the listener containers reach")
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Launcher{
		client:       client,
		ownership:    opts.Ownership,
		test:         opts.Test,
		addrs:        addrs,
		delivery:     deliveryFor(opts.Topology),
		endpointPort: opts.EndpointPort,
		log:          log,
		live:         map[string]tracked{},
	}, nil
}

// Start creates and starts the container of one environment. The image
// must be present (EnsureImage). On any failure after the create, the
// container is removed again. When the daemon refuses to bind-mount the
// code because its path is not shared with it (Docker Desktop's "Mounts
// denied"), Start retries with the code copied in, and so does every later
// Start.
func (l *Launcher) Start(ctx context.Context, spec Spec, slot Slot) (Container, error) {
	if err := spec.validate(); err != nil {
		return Container{}, err
	}
	if err := slot.validate(); err != nil {
		return Container{}, err
	}
	labels, err := docker.Labels{
		Ownership: l.ownership,
		Function: &docker.FunctionLabels{
			Function:   spec.Function.Name,
			Account:    spec.Function.Account.String(),
			Region:     spec.Function.Region.String(),
			Generation: spec.Generation,
		},
		Test: l.test,
	}.Map()
	if err != nil {
		return Container{}, err
	}
	name := containerName(l.ownership.Instance, spec, slot)
	d := l.codeDelivery()
	c, err := l.launch(ctx, name, spec, slot, labels, d)
	// docker.IsMountNotShared may answer the create or the start; launch
	// covers both.
	if _, bind := d.(bindMount); !bind || !docker.IsMountNotShared(err) {
		return c, err
	}
	l.fallBackToCopy(ctx, spec, err)
	return l.launch(ctx, name, spec, slot, labels, tarCopy{})
}

// Remove deletes the container and its anonymous volumes, killing it if it
// runs; function runtimes as PID 1 ignore SIGTERM (containers.md finding
// 7), so there is no grace period. The container is untracked first, so
// understudy's own kill is never reported as an exit. Removing a removed
// container is not an error.
func (l *Launcher) Remove(ctx context.Context, id string) error {
	l.mu.Lock()
	delete(l.live, id)
	l.mu.Unlock()
	err := l.client.ContainerRemove(ctx, id, docker.RemoveOptions{Force: true, Volumes: true})
	if err == nil || docker.IsNotFound(err) {
		return nil
	}
	return err
}

// MemoryUsed reads the bytes of memory the container's processes hold
// now, as docker stats reports them (without reclaimable page cache).
func (l *Launcher) MemoryUsed(ctx context.Context, id string) (uint64, error) {
	m, err := l.client.ContainerMemory(ctx, id)
	if err != nil {
		return 0, err
	}
	return m.Used(), nil
}

// Sweep removes the containers and networks earlier instances of this
// launcher's owner left behind, after a crash (containers.md 9.3). The
// caller must hold the owner's data-directory lock.
func (l *Launcher) Sweep(ctx context.Context) (docker.Swept, error) {
	return l.client.Sweep(ctx, l.ownership)
}

// launch creates and starts one container with code delivered by d,
// removing it again on any failure after the create.
func (l *Launcher) launch(ctx context.Context, name string, spec Spec, slot Slot, labels map[string]string, d delivery) (Container, error) {
	cfg, host := containerSpec(spec, slot, labels, l.addrs, l.endpointPort, d)
	created, err := l.client.ContainerCreate(ctx, name, cfg, host, docker.NetworkingConfig{})
	if err != nil {
		return Container{}, fmt.Errorf("launch %s: %w", name, err)
	}
	c := Container{ID: created.ID, Name: name}
	if err := l.prepare(ctx, c, spec, d); err != nil {
		return Container{}, errors.Join(err, l.Remove(context.WithoutCancel(ctx), c.ID))
	}
	return c, nil
}

func (l *Launcher) codeDelivery() delivery {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.delivery
}

// fallBackToCopy switches every later Start to tar copy, logging the
// switch once.
func (l *Launcher) fallBackToCopy(ctx context.Context, spec Spec, refused error) {
	l.mu.Lock()
	_, already := l.delivery.(tarCopy)
	l.delivery = tarCopy{}
	l.mu.Unlock()
	if already {
		return
	}
	dir := ""
	if code, ok := spec.Code.(ZipCode); ok {
		dir = code.Dir
	}
	l.log.WarnContext(ctx, "the Docker daemon cannot bind-mount the Lambda code store because the path is not shared with it; function code is copied into each container instead. Add the directory to the shared paths (Docker Desktop: Settings > Resources > File sharing) to use read-only bind mounts again",
		slog.String("dir", dir), slog.String("error", refused.Error()))
}

// prepare copies code when bind mounts are unavailable, registers the
// container for exit watching and starts it.
func (l *Launcher) prepare(ctx context.Context, c Container, spec Spec, d delivery) error {
	if code, ok := spec.Code.(ZipCode); ok {
		if _, copyCode := d.(tarCopy); copyCode {
			if err := l.copyCode(ctx, c.ID, code.Dir); err != nil {
				return fmt.Errorf("launch %s: copy code: %w", c.Name, err)
			}
		}
	}
	if err := l.track(c.ID, spec.Architecture); err != nil {
		return fmt.Errorf("launch %s: watch for exit: %w", c.Name, err)
	}
	if err := l.client.ContainerStart(ctx, c.ID); err != nil {
		return fmt.Errorf("launch %s: %w", c.Name, err)
	}
	if err := l.markStarted(c.ID); err != nil {
		return fmt.Errorf("launch %s: watch for exit: %w", c.Name, err)
	}
	return nil
}
