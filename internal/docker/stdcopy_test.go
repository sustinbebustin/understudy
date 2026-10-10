package docker_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
)

type sentFrame struct {
	stream  byte
	payload []byte
}

func drawFrames(t *rapid.T) []sentFrame {
	return rapid.SliceOfN(rapid.Custom(func(t *rapid.T) sentFrame {
		return sentFrame{
			stream:  rapid.SampledFrom([]byte{0, 1, 2}).Draw(t, "stream"),
			payload: rapid.SliceOfN(rapid.Byte(), 0, 70_000).Draw(t, "payload"),
		}
	}), 0, 12).Draw(t, "frames")
}

func TestDemuxRecoversInterleavedFrames(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		frames := drawFrames(t)
		var wire bytes.Buffer
		for _, f := range frames {
			wire.Write(dockertest.MuxFrame(f.stream, f.payload))
		}
		d := docker.NewDemux(&wire, "test")
		for i, want := range frames {
			got, err := d.Next()
			if err != nil {
				t.Fatalf("frame %d: %v", i, err)
			}
			wantStream := docker.Stream(want.stream)
			if want.stream == 0 {
				wantStream = docker.Stdout // the daemon delivers stdin frames as stdout
			}
			if got.Stream != wantStream || !bytes.Equal(got.Payload, want.payload) {
				t.Fatalf("frame %d = stream %d, %d bytes; want stream %d, %d bytes", i, got.Stream, len(got.Payload), wantStream, len(want.payload))
			}
		}
		if _, err := d.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("after the last frame: %v, want io.EOF", err)
		}
	})
}

func TestDemuxReportsATruncatedFrame(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		frame := dockertest.MuxFrame(1, rapid.SliceOfN(rapid.Byte(), 1, 100).Draw(t, "payload"))
		cut := rapid.IntRange(1, len(frame)-1).Draw(t, "cut")
		_, err := docker.NewDemux(bytes.NewReader(frame[:cut]), "test").Next()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("frame cut at %d of %d: %v, want io.ErrUnexpectedEOF", cut, len(frame), err)
		}
	})
}

func TestDemuxTurnsASystemErrFrameIntoAnError(t *testing.T) {
	wire := append(dockertest.MuxFrame(1, []byte("out")), dockertest.MuxFrame(3, []byte("logger failed"))...)
	d := docker.NewDemux(bytes.NewReader(wire), "ContainerLogs fn")
	if f, err := d.Next(); err != nil || string(f.Payload) != "out" {
		t.Fatalf("first frame = %q, %v", f.Payload, err)
	}
	_, err := d.Next()
	var serr *docker.StreamError
	if !errors.As(err, &serr) || serr.Message != "logger failed" || serr.Op != "ContainerLogs fn" {
		t.Errorf("systemerr frame = %v, want *StreamError carrying the daemon message", err)
	}
}

func TestDemuxRejectsUnknownStreamTypes(t *testing.T) {
	if _, err := docker.NewDemux(bytes.NewReader(dockertest.MuxFrame(9, []byte("x"))), "test").Next(); err == nil {
		t.Error("stream type 9 was accepted")
	}
}
