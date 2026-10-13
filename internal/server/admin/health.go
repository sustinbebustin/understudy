package admin

import (
	"errors"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// noOperations is the reason a registered service with nothing bound is
// degraded: every operation answers 501.
const noOperations = "no operations implemented in this build"

// health is the readiness probe of ARCHITECTURE.md 17: 503 with what it is
// waiting for until the listener accepts, then 200 with the version,
// uptime, services, model pin, topology and listener. A service is enabled
// once it binds an operation, and degraded before.
func (h *Handler) health(w http.ResponseWriter, r *http.Request, _ []string) {
	listener := h.listener.Load()
	if listener == nil {
		h.reply(w, r, http.StatusServiceUnavailable, admin.Health{Waiting: []string{"listener"}})
		return
	}
	bindings := h.o.Kernel.Bindings()
	services := map[string]admin.ServiceStatus{}
	for _, svc := range h.o.Kernel.Registry().Services() {
		status := admin.ServiceStatus{Status: admin.Degraded, Reason: noOperations}
		if slices.ContainsFunc(svc.Operations, func(op *registry.Operation) bool { return isBound(bindings.Status(op)) }) {
			status = admin.ServiceStatus{Status: admin.Enabled}
		}
		services[string(svc.ID)] = status
	}
	out := admin.Health{
		Ready:    true,
		Version:  h.o.Version,
		Uptime:   admin.Duration(h.o.Uptime().Round(time.Millisecond)),
		Services: services,
		Journal:  journalStatus(h.o.Kernel.Journal()),
		Model:    h.o.ModelPin,
		Listener: listener.String(),
		Clock:    h.state(),
	}
	if h.o.Components != nil {
		c := h.o.Components.Components(r.Context())
		out.Docker, out.DynamoDBLocal, out.Topology = c.Docker, c.DynamoDBLocal, c.Topology
		for id, status := range c.Services {
			services[string(id)] = status
		}
	}
	h.ok(w, r, out)
}

// journalStatus is the journal's health: zero while it records normally,
// degraded once every event id is issued, which stops recording until the
// emulator restarts, or once its file failed, which stops the file but
// not memory, queries or streams. The kernel logs each refused event; this
// reports the state once.
func journalStatus(j *journal.Journal) admin.ServiceStatus {
	var reasons []string
	if j.NextID() == math.MaxUint64 {
		reasons = append(reasons, "every journal event id has been issued, so no event is recorded until the emulator restarts")
	}
	switch err := j.FileErr(); {
	case err == nil:
	case errors.Is(err, os.ErrClosed):
		reasons = append(reasons, "events recorded after shutdown were not written to the journal file: "+err.Error())
	default:
		reasons = append(reasons, "the journal file stopped at its first failure, while memory, queries and streams go on: "+err.Error())
	}
	if len(reasons) == 0 {
		return admin.ServiceStatus{}
	}
	return admin.ServiceStatus{Status: admin.Degraded, Reason: strings.Join(reasons, "; ")}
}

func isBound(s kernel.Status) bool {
	_, ok := s.(kernel.Bound)
	return ok
}
