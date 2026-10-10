package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// Line is one line of a function container's output.
type Line struct {
	Stream docker.Stream
	// At is when the daemon received the line's first byte (the Docker log
	// timestamp; containers.md 7).
	At   time.Time
	Text string
}

// LogFollower follows a container's output until it stops.
type LogFollower struct {
	lines chan Line
	err   error
}

// Lines yields every line in order per stream and closes when the
// container stops, the context ends or the stream fails; then Err says
// which.
func (f *LogFollower) Lines() <-chan Line { return f.lines }

// Err is nil after a clean end of output and the failure otherwise. Call
// it after Lines is closed.
func (f *LogFollower) Err() error { return f.err }

// Logs follows the container's stdout and stderr from its start,
// demultiplexed, timestamped and reassembled into lines: the daemon splits
// lines longer than 16 KiB over several frames.
func (l *Launcher) Logs(ctx context.Context, id string) (*LogFollower, error) {
	stream, err := l.client.ContainerLogs(ctx, id, docker.LogsOptions{Follow: true, Timestamps: true})
	if err != nil {
		return nil, err
	}
	f := &LogFollower{lines: make(chan Line, 64)}
	go func() {
		defer close(f.lines)
		defer stream.Close()
		f.err = follow(ctx, stream, f.lines)
	}()
	return f, nil
}

func follow(ctx context.Context, stream *docker.LogStream, out chan<- Line) error {
	var lines assembler
	for {
		frame, err := stream.Next()
		if err != nil {
			if !send(ctx, out, lines.flush()) {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if !send(ctx, out, lines.add(frame)) {
			return ctx.Err()
		}
	}
}

func send(ctx context.Context, out chan<- Line, lines []Line) bool {
	for _, line := range lines {
		select {
		case out <- line:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

// MaxLineBytes is the longest Line: the largest CloudWatch Logs event
// message, 1,048,576 bytes less the 26 bytes each event counts for
// (secrets-logs-events-alarms.md 3.3). Longer output without a newline is
// split into lines of this size, less up to three bytes so that a cut
// never splits a UTF-8 character, so a function that never writes a
// newline cannot grow understudy's memory without bound.
const MaxLineBytes = 1<<20 - 26

// assembler joins frames into lines per stream. A line is stamped with the
// timestamp of the frame that carried its first byte.
type assembler struct {
	partial [3]partialLine // indexed by docker.Stream
}

// partialLine is the start of a line whose newline has not arrived.
type partialLine struct {
	at   time.Time
	text []byte
}

// add takes one frame and returns the lines it completes, cutting a line
// at MaxLineBytes, moved back to a character start. Between calls a partial
// line is at most that long, so every cut falls inside this frame or at
// most three bytes before it (the start of a character the previous frame
// split), and what follows the cut is stamped with the frame's time.
func (a *assembler) add(frame docker.Frame) []Line {
	at, text := splitTimestamp(frame.Payload)
	p := &a.partial[frame.Stream]
	if len(p.text) == 0 {
		p.at = at
	}
	p.text = append(p.text, text...)
	var lines []Line
	for {
		end, skip := bytes.IndexByte(p.text, '\n'), 1
		if end < 0 || end > MaxLineBytes {
			if len(p.text) <= MaxLineBytes {
				return lines
			}
			end, skip = runeStartBefore(p.text, MaxLineBytes), 0
		}
		lines = append(lines, Line{Stream: frame.Stream, At: p.at, Text: string(p.text[:end])})
		p.text = p.text[end+skip:]
		p.at = at
	}
}

// runeStartBefore moves a cut at n in text back to the start of the UTF-8
// character it falls in, so neither side of the cut holds a partial
// character. It moves back at most utf8.UTFMax bytes, which is enough for
// valid UTF-8; output that is not valid is cut there anyway.
func runeStartBefore(text []byte, n int) int {
	end := n
	for end > n-utf8.UTFMax && end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return end
}

// flush returns the unterminated lines left at the end of output.
func (a *assembler) flush() []Line {
	var lines []Line
	for _, s := range []docker.Stream{docker.Stdout, docker.Stderr} {
		if p := &a.partial[s]; len(p.text) > 0 {
			lines = append(lines, Line{Stream: s, At: p.at, Text: string(p.text)})
			p.text = nil
		}
	}
	return lines
}

// splitTimestamp separates the RFC 3339 timestamp the daemon prefixes to
// every frame when timestamps are requested. A frame without one keeps its
// bytes and gets the zero time.
func splitTimestamp(payload []byte) (at time.Time, rest []byte) {
	stamp, rest, ok := bytes.Cut(payload, []byte(" "))
	if !ok {
		return time.Time{}, payload
	}
	at, err := time.Parse(time.RFC3339Nano, string(stamp))
	if err != nil {
		return time.Time{}, payload
	}
	return at, rest
}
