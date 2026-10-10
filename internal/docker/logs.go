package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// LogsOptions selects what ContainerLogs returns. Both stdout and stderr
// are always requested.
type LogsOptions struct {
	// Follow keeps the stream open until the container stops.
	Follow bool
	// Timestamps prefixes each payload with an RFC 3339 nano timestamp and
	// one space.
	Timestamps bool
	// Since skips output older than this instant; zero means from the start.
	Since time.Time
}

// rawStreamType is the content type of a container with Tty true, whose
// output is not framed (from API 1.42; see ContainerLogs).
const rawStreamType = "application/vnd.docker.raw-stream"

// rawChunk is how much unframed output one raw-stream frame carries.
const rawChunk = 32 << 10

// LogStream is an open logs response. Close it when done.
type LogStream struct {
	body  io.ReadCloser
	op    string
	demux *Demux
}

// ContainerLogs opens the container's output. Only readable logging drivers
// serve logs; a container with LogConfig "none" answers 501.
func (c *Client) ContainerLogs(ctx context.Context, id string, opts LogsOptions) (*LogStream, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}}
	if opts.Follow {
		q.Set("follow", "1")
	}
	if opts.Timestamps {
		q.Set("timestamps", "1")
	}
	if !opts.Since.IsZero() {
		q.Set("since", unixTimestamp(opts.Since))
	}
	p, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	// Below API 1.42 moby labels every logs response raw-stream, framed or
	// not (container_routes.go), and Podman's compat handler sends no
	// Content-Type at all (compat/containers_logs.go), so in both cases the
	// container's Tty setting decides.
	_, moby := p.Engine.(Moby)
	headerTrusted := moby && p.Negotiated.Compare(logsContentTypeVersion()) >= 0
	tty := false
	if !headerTrusted {
		inspected, err := c.ContainerInspect(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("docker ContainerLogs %s: read the Tty setting, which decides framing on Podman and below API %s: %w", id, logsContentTypeVersion(), err)
		}
		tty = inspected.Config.Tty
	}
	resp, err := c.call(ctx, "ContainerLogs", http.MethodGet, "/containers/"+id+"/logs", q, nil, "") //nolint:bodyclose // the returned LogStream owns the body; Close closes it
	if err != nil {
		return nil, err
	}
	if headerTrusted {
		tty = resp.Header.Get("Content-Type") == rawStreamType
	}
	s := &LogStream{body: resp.Body, op: "ContainerLogs " + id}
	if !tty {
		s.demux = NewDemux(resp.Body, s.op)
	}
	return s, nil
}

// logsContentTypeVersion is the first API version whose logs Content-Type
// tells framed output from raw on Docker Engine.
func logsContentTypeVersion() APIVersion { return APIVersion{Major: 1, Minor: 42} }

// Next returns the next frame, or io.EOF when the stream ends. Output of a
// Tty container is unframed and returned as Stdout chunks.
func (s *LogStream) Next() (Frame, error) {
	if s.demux != nil {
		return s.demux.Next()
	}
	buf := make([]byte, rawChunk)
	for {
		n, err := s.body.Read(buf)
		if n > 0 {
			return Frame{Stream: Stdout, Payload: buf[:n]}, nil
		}
		if err != nil {
			return Frame{}, streamReadError(s.op, err)
		}
	}
}

// Close releases the connection.
func (s *LogStream) Close() error { return s.body.Close() }

// unixTimestamp formats t as the "seconds.nanoseconds" form the since and
// until parameters accept.
func unixTimestamp(t time.Time) string {
	return fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())
}
