package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// modelPin is the vendored Smithy model commit the generated packages come
// from (third_party/api-models-aws/UPSTREAM.json; a test keeps them equal).
const modelPin = "api-models-aws@1cfdfb4bb9ea858e65623e66d73af04bec353201"

// health serves GET /_understudy/v1/health with the readiness contract of
// ARCHITECTURE.md 17: 503 with what it is waiting for until the listener
// accepts, then 200 with the version, uptime, services and model pin. It is
// the interim admin handler until the admin API package takes the prefix
// over (T22).
type health struct {
	version  string
	services []string
	uptime   func() time.Duration
	ready    atomic.Bool
	log      *slog.Logger
}

// healthPath is the readiness endpoint.
const healthPath = "/_understudy/v1/health"

type waiting struct {
	Ready   bool     `json:"ready"`
	Waiting []string `json:"waiting"`
}

type serviceStatus struct {
	Status string `json:"status"`
}

type ready struct {
	Ready    bool                     `json:"ready"`
	Version  string                   `json:"version"`
	Uptime   string                   `json:"uptime"`
	Services map[string]serviceStatus `json:"services"`
	Model    string                   `json:"model"`
}

type adminError struct {
	Error adminErrorBody `json:"error"`
}

type adminErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// ServeHTTP matches the escaped path, as the edge routes and the container
// listener filters on it (go-toolchain.md 5): a percent-encoded spelling of
// the health path is not the health path.
func (h *health) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if path := r.URL.EscapedPath(); path != healthPath {
		h.reply(w, r, http.StatusNotFound, adminError{Error: adminErrorBody{
			Code:    "not_found",
			Message: path + " is not an admin endpoint of this build",
			Hint:    "GET " + healthPath + " is served; the rest of the admin API is not wired yet",
		}})
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.reply(w, r, http.StatusMethodNotAllowed, adminError{Error: adminErrorBody{
			Code:    "invalid_request",
			Message: r.Method + " " + healthPath + " is not supported",
			Hint:    "use GET " + healthPath,
		}})
		return
	}
	if !h.ready.Load() {
		h.reply(w, r, http.StatusServiceUnavailable, waiting{Waiting: []string{"listener"}})
		return
	}
	services := make(map[string]serviceStatus, len(h.services))
	for _, s := range h.services {
		services[s] = serviceStatus{Status: "enabled"}
	}
	h.reply(w, r, http.StatusOK, ready{
		Ready:    true,
		Version:  h.version,
		Uptime:   h.uptime().Round(time.Millisecond).String(),
		Services: services,
		Model:    modelPin,
	})
}

func (h *health) reply(w http.ResponseWriter, r *http.Request, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		h.log.LogAttrs(r.Context(), slog.LevelError, "health response did not encode", slog.String("error", err.Error()))
		http.Error(w, "health response did not encode", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(data, '\n')); err != nil {
		h.log.LogAttrs(r.Context(), slog.LevelDebug, "health response not written; the client went away", slog.String("error", err.Error()))
	}
}
