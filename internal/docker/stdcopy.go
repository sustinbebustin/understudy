package docker

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Stream identifies the output stream a log frame came from.
type Stream uint8

// The output streams. Their values are the frame types of the multiplexed
// format.
const (
	Stdout Stream = 1
	Stderr Stream = 2
)

const (
	frameHeaderLen = 8
	// frameStdin frames are delivered as Stdout; frameSystemErr is not a
	// Stream but an error.
	frameStdin     = 0
	frameSystemErr = 3
	// maxFramePayload bounds one frame. The daemon writes at most its 32 KiB
	// copy buffer per frame; the bound only stops a corrupt length from
	// allocating gigabytes.
	maxFramePayload = 16 << 20
)

// Frame is one payload of the multiplexed stream. A log line longer than
// 16 KiB arrives split over several frames; the last carries the newline
// (containers.md 2.6).
type Frame struct {
	Stream  Stream
	Payload []byte
}

// Demux reads the multiplexed stream format of logs and attach for
// containers created with Tty false: an 8-byte header {stream, 0, 0, 0,
// size uint32 big endian} then size bytes of payload.
type Demux struct {
	r      io.Reader
	op     string
	header [frameHeaderLen]byte
}

// NewDemux reads frames from r; op names the call in errors.
func NewDemux(r io.Reader, op string) *Demux {
	return &Demux{r: r, op: op}
}

// Next returns the next frame, io.EOF at a clean end of stream, a
// *StreamError when the daemon sends a systemerr frame, and
// io.ErrUnexpectedEOF when the stream stops inside a frame.
func (d *Demux) Next() (Frame, error) {
	if _, err := io.ReadFull(d.r, d.header[:]); err != nil {
		return Frame{}, streamReadError(d.op, err)
	}
	size := binary.BigEndian.Uint32(d.header[4:])
	if size > maxFramePayload {
		return Frame{}, fmt.Errorf("docker %s: frame of %d bytes exceeds the %d byte limit; the stream is corrupt or not multiplexed (Tty true?)", d.op, size, maxFramePayload)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(d.r, payload); err != nil {
		if errors.Is(err, io.EOF) {
			return Frame{}, io.ErrUnexpectedEOF
		}
		return Frame{}, streamReadError(d.op, err)
	}
	switch kind := d.header[0]; kind {
	case frameStdin, byte(Stdout):
		return Frame{Stream: Stdout, Payload: payload}, nil
	case byte(Stderr):
		return Frame{Stream: Stderr, Payload: payload}, nil
	case frameSystemErr:
		return Frame{}, &StreamError{Op: d.op, Message: string(payload)}
	default:
		return Frame{}, fmt.Errorf("docker %s: unknown stream type %d in frame header", d.op, kind)
	}
}

// streamReadError names op on a failed body read, such as a cancelled
// context or a reset connection. io.EOF and io.ErrUnexpectedEOF pass
// through unwrapped, since callers compare against them.
func streamReadError(op string, err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	return fmt.Errorf("docker %s: read stream: %w", op, err)
}
