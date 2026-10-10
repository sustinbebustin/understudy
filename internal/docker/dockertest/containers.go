package dockertest

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// Program scripts what a container does when it starts.
//
//sumtype:decl
type Program interface{ isProgram() }

// Exit writes Output and then exits with Code; OOMKilled reports the exit as
// an out-of-memory kill (an oom event before die).
type Exit struct {
	Output    []docker.Frame
	Code      int
	OOMKilled bool
}

// Run writes Output and keeps running until it is stopped, killed, removed
// or ended with Daemon.Exit. A container whose image has no program runs
// this with no output.
type Run struct {
	Output []docker.Frame
}

func (Exit) isProgram() {}
func (Run) isProgram()  {}

// SetProgram scripts every container later started from image.
func (d *Daemon) SetProgram(image string, p Program) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.programs[normalizeRef(image)] = p
}

type status string

const (
	statusCreated status = "created"
	statusRunning status = "running"
	statusExited  status = "exited"
)

type logEntry struct {
	at     time.Time
	stream docker.Stream
	data   []byte
}

type container struct {
	id         string
	name       string
	image      docker.Image
	config     docker.Config
	host       docker.HostConfig
	networks   map[string]docker.EndpointSettings
	status     status
	exitCode   int
	oomKilled  bool
	startedAt  time.Time
	finishedAt time.Time
	exits      int
	removed    bool
	logs       []logEntry
	uploads    []Upload
	// waitFailures are the FailWait messages in order; a wait ends with the
	// first one scripted after it was registered.
	waitFailures []string
}

// lookupContainer finds a live container by full ID, name or unique ID
// prefix, in that order. Callers hold d.mu.
func (d *Daemon) lookupContainer(ref string) (*container, bool) {
	return resolve(d.containers, strings.TrimPrefix(ref, "/"), func(c *container) string { return c.name })
}

// Exit makes a running container exit with code, as if its process ended.
// It reports whether the container was running.
func (d *Daemon) Exit(id string, code int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.lookupContainer(id)
	if !ok || c.status != statusRunning {
		return false
	}
	d.exit(c, code, false)
	return true
}

// FailWait ends every wait registered on a container so far with the error
// body the daemon sends when waiting fails: StatusCode -1 and message. The
// container itself is unchanged. It reports whether the container exists.
func (d *Daemon) FailWait(id, message string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.lookupContainer(id)
	if !ok {
		return false
	}
	c.waitFailures = append(c.waitFailures, message)
	d.notify()
	return true
}

// WriteLog appends output to a container's log, as if its process wrote
// it. It reports whether the container exists.
func (d *Daemon) WriteLog(id string, frames ...docker.Frame) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.lookupContainer(id)
	if !ok {
		return false
	}
	d.appendLogs(c, frames)
	return true
}

// Container reports a container the way inspect does.
func (d *Daemon) Container(id string) (docker.Container, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.lookupContainer(id)
	if !ok {
		return docker.Container{}, false
	}
	return d.inspect(c), true
}

// Created returns every container created so far, removed ones included,
// in creation order and as inspect reports them; a removed container is
// shown in its last state. Use it to check the configuration a caller
// submitted for a container it has since removed.
func (d *Daemon) Created() []docker.Container {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]docker.Container, 0, len(d.history))
	for _, c := range d.history {
		out = append(out, d.inspect(c))
	}
	return out
}

// Uploads returns the archives uploaded into a container, in order.
func (d *Daemon) Uploads(id string) []Upload {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.lookupContainer(id)
	if !ok {
		return nil
	}
	return slices.Clone(c.uploads)
}

func (d *Daemon) appendLogs(c *container, frames []docker.Frame) {
	for _, f := range frames {
		c.logs = append(c.logs, logEntry{at: d.now(), stream: f.Stream, data: slices.Clone(f.Payload)})
	}
	d.notify()
}

// emitContainer records a container event; Docker merges the container's
// labels (including those inherited from the image) into the attributes.
func (d *Daemon) emitContainer(c *container, action string, extra map[string]string) {
	attrs := maps.Clone(c.config.Labels)
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["image"] = c.config.Image
	attrs["name"] = c.name
	maps.Copy(attrs, extra)
	d.emit("container", action, c.id, attrs)
}

func (d *Daemon) exit(c *container, code int, oom bool) {
	c.status = statusExited
	c.exitCode = code
	c.oomKilled = oom
	c.finishedAt = d.now()
	c.exits++
	if oom {
		d.emitContainer(c, "oom", nil)
	}
	d.emitContainer(c, "die", map[string]string{"exitCode": strconv.Itoa(code)})
}

func validName(name string) bool {
	name = strings.TrimPrefix(name, "/")
	if len(name) < 2 {
		return false
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			i > 0 && (r == '_' || r == '.' || r == '-')
		if !ok {
			return false
		}
	}
	return true
}

type createRequest struct {
	docker.Config
	HostConfig       docker.HostConfig       `json:"HostConfig"`
	NetworkingConfig docker.NetworkingConfig `json:"NetworkingConfig"`
}

func (d *Daemon) containerCreate(w http.ResponseWriter, r *http.Request) {
	var in createRequest
	if err := decodeBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := r.URL.Query().Get("name")
	if name != "" && !validName(name) {
		writeError(w, http.StatusBadRequest, "Invalid container name ("+name+"), only [a-zA-Z0-9][a-zA-Z0-9_.-] are allowed")
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	img, ok := d.lookupImage(in.Image)
	if !ok {
		writeError(w, http.StatusNotFound, "No such image: "+in.Image)
		return
	}
	d.created++
	sum := sha256.Sum256([]byte("container/" + strconv.Itoa(d.created)))
	id := hex.EncodeToString(sum[:])
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		name = "dockertest_" + strconv.Itoa(d.created)
	}
	if other, taken := d.lookupContainer(name); taken && other.name == name {
		writeError(w, http.StatusConflict, `Conflict. The container name "/`+name+`" is already in use by container "`+other.id+`". You have to remove (or rename) that container to be able to reuse that name.`)
		return
	}
	networks, missing := d.endpoints(in.HostConfig.NetworkMode, in.NetworkingConfig)
	if missing != "" {
		writeError(w, http.StatusNotFound, "network "+missing+" not found")
		return
	}
	c := &container{id: id, name: name, image: img, config: in.Config, host: in.HostConfig, networks: networks, status: statusCreated}
	c.config.Labels = maps.Clone(img.Config.Labels)
	if c.config.Labels == nil {
		c.config.Labels = map[string]string{}
	}
	maps.Copy(c.config.Labels, in.Labels)
	if c.config.Hostname == "" {
		c.config.Hostname = id[:12]
	}
	d.containers[id] = c
	d.history = append(d.history, c)
	d.emitContainer(c, "create", nil)
	writeJSON(w, http.StatusCreated, docker.Created{ID: id, Warnings: []string{}})
}

// endpoints resolves the networks a new container joins, returning the name
// of the first one that does not exist. Callers hold d.mu.
func (d *Daemon) endpoints(mode string, cfg docker.NetworkingConfig) (joined map[string]docker.EndpointSettings, missing string) {
	out := map[string]docker.EndpointSettings{}
	switch mode {
	case "", "default", "bridge", d.opts.DefaultNetwork:
		out[d.opts.DefaultNetwork] = docker.EndpointSettings{}
	case "host", "none":
		out[mode] = docker.EndpointSettings{}
	default:
		if _, ok := d.lookupNetwork(mode); !ok {
			return nil, mode
		}
		out[mode] = docker.EndpointSettings{}
	}
	for name, ep := range cfg.EndpointsConfig {
		if _, ok := d.lookupNetwork(name); !ok {
			return nil, name
		}
		out[name] = ep
	}
	return out, ""
}

// assignAddresses gives every bridge endpoint without an address one from
// its network, as the daemon does on start. Callers hold d.mu.
func (c *container) assignAddresses(d *Daemon) {
	for name, ep := range c.networks {
		n, ok := d.lookupNetwork(name)
		if !ok || ep.IPAddress != "" {
			continue
		}
		ep.NetworkID = n.id
		ep.IPAddress = n.allocate().String()
		ep.Gateway = n.gateway.String()
		ep.DNSNames = append([]string{c.name}, ep.Aliases...)
		c.networks[name] = ep
	}
}

func (d *Daemon) withContainer(w http.ResponseWriter, r *http.Request, f func(c *container)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.lookupContainer(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "No such container: "+r.PathValue("id"))
		return
	}
	f(c)
}

func (d *Daemon) containerStart(w http.ResponseWriter, r *http.Request) {
	d.withContainer(w, r, func(c *container) {
		if c.status == statusRunning {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		c.status = statusRunning
		c.startedAt = d.now()
		c.oomKilled = false
		c.assignAddresses(d)
		d.emitContainer(c, "start", nil)
		program, ok := d.programs[normalizeRef(c.config.Image)]
		if !ok {
			program = Run{}
		}
		switch p := program.(type) {
		case Exit:
			d.appendLogs(c, p.Output)
			d.exit(c, p.Code, p.OOMKilled)
		case Run:
			d.appendLogs(c, p.Output)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// containerStop ends a running container with 137, as when the grace period
// runs out on a process that ignores SIGTERM (containers.md finding 7).
func (d *Daemon) containerStop(w http.ResponseWriter, r *http.Request) {
	d.withContainer(w, r, func(c *container) {
		if c.status != statusRunning {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		d.emitContainer(c, "kill", map[string]string{"signal": "15"})
		d.exit(c, 137, false)
		d.emitContainer(c, "stop", nil)
		w.WriteHeader(http.StatusNoContent)
	})
}

// signalExit maps the signals a fake process dies of to their exit codes;
// any other signal is treated as handled and the container keeps running.
func signalExit(signal string) (number string, code int, dies bool) {
	switch strings.TrimPrefix(strings.ToUpper(signal), "SIG") {
	case "", "KILL", "9":
		return "9", 137, true
	case "TERM", "15":
		return "15", 143, true
	default:
		return signal, 0, false
	}
}

func (d *Daemon) containerKill(w http.ResponseWriter, r *http.Request) {
	d.withContainer(w, r, func(c *container) {
		if c.status != statusRunning {
			writeError(w, http.StatusConflict, "cannot kill container: "+c.name+": container "+c.id+" is not running")
			return
		}
		num, code, dies := signalExit(r.URL.Query().Get("signal"))
		d.emitContainer(c, "kill", map[string]string{"signal": num})
		if dies {
			d.exit(c, code, false)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func (d *Daemon) containerRemove(w http.ResponseWriter, r *http.Request) {
	d.withContainer(w, r, func(c *container) {
		force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
		if c.status == statusRunning {
			if !force {
				writeError(w, http.StatusConflict, `cannot remove container "/`+c.name+`": container is running: stop the container before removing or force remove`)
				return
			}
			d.emitContainer(c, "kill", map[string]string{"signal": "9"})
			d.exit(c, 137, false)
		}
		c.removed = true
		delete(d.containers, c.id)
		d.emitContainer(c, "destroy", nil)
		w.WriteHeader(http.StatusNoContent)
	})
}

func (d *Daemon) containerInspect(w http.ResponseWriter, r *http.Request) {
	d.withContainer(w, r, func(c *container) {
		writeJSON(w, http.StatusOK, d.inspect(c))
	})
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "0001-01-01T00:00:00Z"
	}
	return t.Format(time.RFC3339Nano)
}

func (d *Daemon) inspect(c *container) docker.Container {
	mounts := make([]docker.MountPoint, 0, len(c.host.Mounts))
	for _, m := range c.host.Mounts {
		mounts = append(mounts, docker.MountPoint{Type: m.Type, Source: m.Source, Destination: m.Target, RW: !m.ReadOnly})
	}
	return docker.Container{
		ID:         c.id,
		Name:       "/" + c.name,
		Image:      c.image.ID,
		Config:     c.config,
		HostConfig: c.host,
		State: docker.ContainerState{
			Status:     string(c.status),
			Running:    c.status == statusRunning,
			OOMKilled:  c.oomKilled,
			ExitCode:   c.exitCode,
			StartedAt:  formatTime(c.startedAt),
			FinishedAt: formatTime(c.finishedAt),
		},
		NetworkSettings: docker.NetworkSettings{Networks: maps.Clone(c.networks)},
		Mounts:          mounts,
	}
}

func (d *Daemon) containerList(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilters(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	all := r.URL.Query().Get("all") == "1" || r.URL.Query().Get("all") == "true"
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []docker.ContainerSummary{}
	for _, c := range sortedContainers(d.containers) {
		if !all && c.status != statusRunning || !labelsMatch(f["label"], c.config.Labels) {
			continue
		}
		out = append(out, docker.ContainerSummary{
			ID:     c.id,
			Names:  []string{"/" + c.name},
			Image:  c.config.Image,
			State:  string(c.status),
			Status: string(c.status),
			Labels: maps.Clone(c.config.Labels),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Daemon) containerArchive(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read archive: "+err.Error())
		return
	}
	d.withContainer(w, r, func(c *container) {
		switch {
		case path == "":
			writeError(w, http.StatusBadRequest, "path must not be empty")
		case c.host.ReadonlyRootfs:
			writeError(w, http.StatusBadRequest, "container rootfs is marked read-only")
		default:
			c.uploads = append(c.uploads, Upload{Path: path, Data: data})
			w.WriteHeader(http.StatusOK)
		}
	})
}
