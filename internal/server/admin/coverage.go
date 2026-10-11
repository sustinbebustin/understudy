package admin

import (
	"log/slog"
	"net/http"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/coverage"
)

// The forms GET /coverage answers in, JSON when the request names none,
// and the query parameter that picks one.
const (
	coverageJSON     = "json"
	coverageMarkdown = "markdown"
	paramFormat      = "format"
)

// coverage answers the coverage report of this server (ARCHITECTURE.md
// 3.4, 17): protocol compliance from the baseline built into the binary,
// operation coverage from the kernel's bindings, and the switches of the
// wired services. The binary carries no overlay files, so the report
// points to docs/coverage.md for overlay evidence.
func (h *Handler) coverage(w http.ResponseWriter, r *http.Request, _ []string) {
	format := r.URL.Query().Get(paramFormat)
	switch format {
	case "":
		format = coverageJSON
	case coverageJSON, coverageMarkdown:
	default:
		h.fail(w, r, &admin.Error{
			Code: admin.InvalidRequest, Field: paramFormat, Known: []string{coverageJSON, coverageMarkdown},
			Message: "format " + format + " is not a coverage report format",
		})
		return
	}
	var sources []coverage.SwitchSource
	for _, s := range h.o.Services {
		if src, ok := s.(coverage.SwitchSource); ok {
			sources = append(sources, src)
		}
	}
	in, err := coverage.Running(h.o.Kernel, sources...)
	if err != nil {
		h.fail(w, r, internal("reading the protocol-test baseline built into this binary", err))
		return
	}
	report := coverage.Build(in)
	body, contentType := report.Markdown(), "text/markdown; charset=utf-8"
	if format == coverageJSON {
		if body, err = report.JSON(); err != nil {
			h.fail(w, r, internal("encoding the coverage report", err))
			return
		}
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	if _, err := w.Write(body); err != nil {
		h.log.LogAttrs(r.Context(), slog.LevelDebug, "coverage report not written; the client went away", slog.String("error", err.Error()))
	}
}
