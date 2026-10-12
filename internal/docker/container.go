package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// Config is the container configuration part of a create request
// (containers.md 3.1, 3.2). Tty must stay false so logs are framed.
type Config struct {
	// Platform is sent as the platform query parameter of the create call,
	// not in the body; the zero value means the daemon's own platform.
	Platform   Platform          `json:"-"`
	Hostname   string            `json:"Hostname,omitempty"`
	User       string            `json:"User,omitempty"`
	Env        []string          `json:"Env,omitempty"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Entrypoint []string          `json:"Entrypoint,omitempty"`
	Image      string            `json:"Image"`
	WorkingDir string            `json:"WorkingDir,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
	Tty        bool              `json:"Tty,omitempty"`
}

// HostConfig is the host-side configuration of a container. It has no
// Binds and no AutoRemove: ARCHITECTURE 14.3 mounts code through Mounts
// only and rules out AutoRemove, and no port is published (the Runtime API
// is hosted by understudy, 14.1). Add a field only with the section that
// allows it.
type HostConfig struct {
	// Mounts, not Binds, for code: a missing bind source fails instead of
	// being created as root (containers.md finding 5).
	Mounts         []Mount           `json:"Mounts,omitempty"`
	ExtraHosts     []string          `json:"ExtraHosts,omitempty"`
	LogConfig      LogConfig         `json:"LogConfig,omitzero"`
	NetworkMode    string            `json:"NetworkMode,omitempty"`
	Memory         int64             `json:"Memory,omitempty"`
	MemorySwap     int64             `json:"MemorySwap,omitempty"`
	NanoCPUs       int64             `json:"NanoCpus,omitempty"`
	PidsLimit      int64             `json:"PidsLimit,omitempty"`
	Ulimits        []Ulimit          `json:"Ulimits,omitempty"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs,omitempty"`
	CapDrop        []string          `json:"CapDrop,omitempty"`
	SecurityOpt    []string          `json:"SecurityOpt,omitempty"`
}

// Mount is one entry of HostConfig.Mounts.
type Mount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly,omitempty"`
}

// LogConfig selects the logging driver. Set it explicitly on every
// container: only readable drivers (json-file, local, journald) serve logs.
type LogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config,omitempty"`
}

// Ulimit is one resource limit.
type Ulimit struct {
	Name string `json:"Name"`
	Soft int64  `json:"Soft"`
	Hard int64  `json:"Hard"`
}

// NetworkingConfig attaches a container to networks at create time.
type NetworkingConfig struct {
	EndpointsConfig map[string]EndpointSettings `json:"EndpointsConfig,omitempty"`
}

// EndpointSettings is a container's endpoint on one network, as submitted
// on create or connect and as reported by inspect.
type EndpointSettings struct {
	Aliases    []string `json:"Aliases,omitempty"`
	NetworkID  string   `json:"NetworkID,omitempty"`
	IPAddress  string   `json:"IPAddress,omitempty"`
	Gateway    string   `json:"Gateway,omitempty"`
	MacAddress string   `json:"MacAddress,omitempty"`
	DNSNames   []string `json:"DNSNames,omitempty"`
}

// Created is the answer to a create call. Warnings carries platform and
// swap-limit notices worth logging.
type Created struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

type createBody struct {
	Config
	HostConfig       HostConfig       `json:"HostConfig"`
	NetworkingConfig NetworkingConfig `json:"NetworkingConfig"`
}

// ContainerCreate creates (does not start) a container named name. Podman's
// compatibility layer accepts endpoint settings for one network only, so
// more are refused up front there (containers.md 10.1).
func (c *Client) ContainerCreate(ctx context.Context, name string, cfg Config, host HostConfig, net NetworkingConfig) (Created, error) {
	p, err := c.session(ctx)
	if err != nil {
		return Created{}, err
	}
	if _, ok := p.Engine.(Podman); ok && len(net.EndpointsConfig) > 1 {
		return Created{}, &EngineLimitError{Op: "ContainerCreate", Reason: fmt.Sprintf("Podman accepts endpoint settings for one network per container, got %d; create on one network and use NetworkConnect for the rest", len(net.EndpointsConfig))}
	}
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	if pl := cfg.Platform.String(); pl != "" {
		q.Set("platform", pl)
	}
	var out Created
	err = c.callJSON(ctx, "ContainerCreate", http.MethodPost, "/containers/create", q,
		createBody{Config: cfg, HostConfig: host, NetworkingConfig: net}, &out)
	return out, err
}

// ContainerStart starts a container. Starting a running container is not
// an error (the daemon answers 304).
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	return c.callJSON(ctx, "ContainerStart", http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
}

// ContainerStop sends the stop signal and kills the container after timeout
// (whole seconds, rounded up). Stopping a stopped container is not an
// error. Function containers are removed with force instead: the Python
// runtime as PID 1 ignores SIGTERM (containers.md finding 7).
func (c *Client) ContainerStop(ctx context.Context, id string, timeout time.Duration) error {
	secs := int64((timeout + time.Second - 1) / time.Second)
	q := url.Values{"t": {strconv.FormatInt(max(secs, 0), 10)}}
	return c.callJSON(ctx, "ContainerStop", http.MethodPost, "/containers/"+id+"/stop", q, nil, nil)
}

// ContainerKill sends signal (SIGKILL when empty). Killing a container that
// is not running is a conflict.
func (c *Client) ContainerKill(ctx context.Context, id, signal string) error {
	q := url.Values{}
	if signal != "" {
		q.Set("signal", signal)
	}
	return c.callJSON(ctx, "ContainerKill", http.MethodPost, "/containers/"+id+"/kill", q, nil, nil)
}

// RemoveOptions controls ContainerRemove.
type RemoveOptions struct {
	// Force kills a running container first.
	Force bool
	// Volumes removes the container's anonymous volumes.
	Volumes bool
}

// ContainerRemove deletes a container. Without Force a running container is
// a conflict.
func (c *Client) ContainerRemove(ctx context.Context, id string, opts RemoveOptions) error {
	q := url.Values{}
	if opts.Force {
		q.Set("force", "1")
	}
	if opts.Volumes {
		q.Set("v", "1")
	}
	return c.callJSON(ctx, "ContainerRemove", http.MethodDelete, "/containers/"+id, q, nil, nil)
}

// WaitCondition selects what ContainerWait waits for.
type WaitCondition string

// The wait conditions.
const (
	// WaitNotRunning returns at once for a container that is not running.
	WaitNotRunning WaitCondition = "not-running"
	// WaitNextExit waits for the next exit; register it before start.
	WaitNextExit WaitCondition = "next-exit"
	// WaitRemoved waits until the container is removed.
	WaitRemoved WaitCondition = "removed"
)

// Wait is a registered wait. The daemon sends the response headers once the
// waiter is registered, so a WaitNextExit obtained before ContainerStart
// cannot miss the exit.
type Wait struct {
	op   string
	body io.ReadCloser
}

// ContainerWait registers a wait and returns once the daemon has accepted
// it; Result blocks for the outcome.
func (c *Client) ContainerWait(ctx context.Context, id string, cond WaitCondition) (*Wait, error) {
	q := url.Values{"condition": {string(cond)}}
	resp, err := c.call(ctx, "ContainerWait", http.MethodPost, "/containers/"+id+"/wait", q, nil, "") //nolint:bodyclose // the returned Wait owns the body; Result closes it
	if err != nil {
		return nil, err
	}
	return &Wait{op: "ContainerWait " + id, body: resp.Body}, nil
}

// Close abandons the wait and releases its connection. It is safe to call
// after Result, so `defer w.Close()` covers every path.
func (w *Wait) Close() error { return w.body.Close() }

// Result blocks until the condition holds and returns the container's exit
// code. It releases the connection; call it at most once.
func (w *Wait) Result() (int, error) {
	defer w.body.Close()
	var out struct {
		StatusCode int `json:"StatusCode"`
		Error      *struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := json.NewDecoder(w.body).Decode(&out); err != nil {
		return 0, fmt.Errorf("docker %s: read result: %w", w.op, err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return out.StatusCode, &StreamError{Op: w.op, Message: out.Error.Message}
	}
	return out.StatusCode, nil
}

// Container is the subset of GET /containers/{id}/json that the runtime
// reads.
type Container struct {
	ID              string          `json:"Id"`
	Name            string          `json:"Name"`
	Image           string          `json:"Image"`
	Config          Config          `json:"Config"`
	HostConfig      HostConfig      `json:"HostConfig"`
	State           ContainerState  `json:"State"`
	NetworkSettings NetworkSettings `json:"NetworkSettings"`
	Mounts          []MountPoint    `json:"Mounts"`
}

// ContainerState is a container's lifecycle state.
type ContainerState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	OOMKilled  bool   `json:"OOMKilled"`
	ExitCode   int    `json:"ExitCode"`
	Error      string `json:"Error"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
}

// NetworkSettings carries a container's endpoints, keyed by network name.
// Since API 1.52 the top-level address fields are gone, so only Networks is
// read (containers.md 2.5).
type NetworkSettings struct {
	Networks map[string]EndpointSettings `json:"Networks"`
}

// MountPoint is a mount as inspect reports it; Source is the host-side path.
type MountPoint struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

// ContainerInspect reads a container by ID, ID prefix or name.
func (c *Client) ContainerInspect(ctx context.Context, id string) (Container, error) {
	var out Container
	err := c.callJSON(ctx, "ContainerInspect", http.MethodGet, "/containers/"+id+"/json", nil, nil, &out)
	return out, err
}

// Memory is the memory_stats part of GET /containers/{id}/stats.
type Memory struct {
	// Usage is the cgroup's charged memory, page cache included.
	Usage uint64 `json:"usage"`
	// MaxUsage is the peak of Usage under cgroup v1; cgroup v2 daemons
	// report none.
	MaxUsage uint64 `json:"max_usage"`
	Stats    struct {
		// InactiveFile is reclaimable page cache under cgroup v2;
		// TotalInactiveFile is its cgroup v1 name.
		InactiveFile      uint64 `json:"inactive_file"`
		TotalInactiveFile uint64 `json:"total_inactive_file"`
	} `json:"stats"`
}

// Used is the memory the container's processes hold now, as docker stats
// reports it: Usage without the reclaimable page cache.
func (m Memory) Used() uint64 {
	cache := max(m.Stats.InactiveFile, m.Stats.TotalInactiveFile)
	if cache > m.Usage {
		return 0
	}
	return m.Usage - cache
}

// ContainerMemory reads a running container's memory statistics once,
// without the second CPU sample a plain stats read waits a second for.
func (c *Client) ContainerMemory(ctx context.Context, id string) (Memory, error) {
	var out struct {
		Memory Memory `json:"memory_stats"`
	}
	q := url.Values{"stream": {"false"}, "one-shot": {"true"}}
	err := c.callJSON(ctx, "ContainerStats", http.MethodGet, "/containers/"+id+"/stats", q, nil, &out)
	return out.Memory, err
}

// ContainerSummary is one row of GET /containers/json.
type ContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
}

// ContainerList lists every container, running or not, that carries all of
// labels. Image labels are inherited by containers, so select on
// understudy's own keys only.
func (c *Client) ContainerList(ctx context.Context, labels map[string]string) ([]ContainerSummary, error) {
	q, err := labelQuery(labels)
	if err != nil {
		return nil, err
	}
	q.Set("all", "1")
	var out []ContainerSummary
	err = c.callJSON(ctx, "ContainerList", http.MethodGet, "/containers/json", q, nil, &out)
	return out, err
}

// ContainerArchiveUpload extracts the tar stream into the container at the
// directory path. The daemon refuses it for a read-only root filesystem.
func (c *Client) ContainerArchiveUpload(ctx context.Context, id, path string, tar io.Reader) error {
	q := url.Values{"path": {path}}
	resp, err := c.call(ctx, "ContainerArchiveUpload", http.MethodPut, "/containers/"+id+"/archive", q, tar, "application/x-tar")
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

// Filters is the filters query of list and events calls: each key maps to
// values that must all match (labels) or any match (other keys), as the
// daemon defines per key.
type Filters map[string][]string

func (f Filters) query() (url.Values, error) {
	q := url.Values{}
	if len(f) == 0 {
		return q, nil
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("docker: encode filters: %w", err)
	}
	q.Set("filters", string(raw))
	return q, nil
}

// LabelFilter builds the label filter values "key=value" in key order.
func LabelFilter(labels map[string]string) []string {
	out := make([]string, 0, len(labels))
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		out = append(out, k+"="+labels[k])
	}
	return out
}

func labelQuery(labels map[string]string) (url.Values, error) {
	if len(labels) == 0 {
		return url.Values{}, nil
	}
	return Filters{"label": LabelFilter(labels)}.query()
}
