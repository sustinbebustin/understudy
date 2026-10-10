package journal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// sink appends events to an NDJSON file, rotating it by size. Recording only
// queues an event, under the journal lock; a writer goroutine encodes and
// writes it with no lock held, so a slow or stalled disk never delays
// recording. The queue keeps recording order and holds at most limit
// estimated bytes not yet written. The first failure, a full queue among
// them, stops the file, so it always holds a prefix of recording order with
// no holes.
type sink struct {
	path   string
	rotate int64
	limit  int // most estimated bytes not yet written, unless one event alone exceeds it
	open   opener
	wake   chan struct{} // holds a token while the queue may be non-empty or close was asked
	done   chan struct{} // closed once the writer has exited

	mu      sync.Mutex
	queue   []queued
	pending int   // estimated bytes of the events queued or taken by the writer and not yet written
	closed  bool  // close was called; the writer exits once the queue is empty
	err     error // the first failure; nothing is queued once it is set

	// Owned by the writer goroutine once it starts.
	f    file // nil once a failure stopped the sink, or after close
	size int64
}

// queued is an event waiting for the writer and its Estimate.
type queued struct {
	event Event
	size  int
}

// file is the open journal file: an *os.File, or a double in tests.
type file interface {
	Write(p []byte) (int, error)
	Close() error
}

// opener opens the file at path for appending and returns it with its size.
type opener func(path string) (file, int64, error)

// FileError reports a failure of the journal file. Events in memory are
// unaffected.
type FileError struct {
	Path string
	Op   FileOp
	Err  error
}

func (e *FileError) Error() string {
	return fmt.Sprintf("journal: %s %s: %v", e.Op, e.Path, e.Err)
}

func (e *FileError) Unwrap() error { return e.Err }

// FileOp is the step of the journal file that failed.
type FileOp uint8

// FileOp values.
const (
	// FileOpen: opening the file, or ending a torn last record left by a
	// crash so the next record starts on its own line.
	FileOpen FileOp = iota + 1
	// FileEncode: encoding an event, which Append and Begin should have
	// refused.
	FileEncode
	// FileWrite: appending a record.
	FileWrite
	// FileRotate: renaming the full file to Path + ".1".
	FileRotate
	// FileClose: closing the file.
	FileClose
	// FileQueue: queueing an event while the disk had fallen MaxBytes
	// estimated bytes behind recording.
	FileQueue
)

// String returns the step's name, such as "write".
func (o FileOp) String() string {
	switch o {
	case FileOpen:
		return "open"
	case FileEncode:
		return "encode"
	case FileWrite:
		return "write"
	case FileRotate:
		return "rotate"
	case FileClose:
		return "close"
	case FileQueue:
		return "queue"
	default:
		return fmt.Sprintf("FileOp(%d)", uint8(o))
	}
}

// QueueFullError is the cause of a FileQueue failure: the disk fell so far
// behind that queueing Event would take the estimated bytes waiting to be
// written past Limit (Options.MaxBytes). Sizes are Estimate values.
type QueueFullError struct {
	Event   ref.EventID
	Size    int // the refused event's estimated bytes
	Waiting int // estimated bytes queued or being written when it was refused
	Limit   int
}

func (e *QueueFullError) Error() string {
	return fmt.Sprintf("the disk fell behind: event %d (%d estimated bytes) would take the %d estimated bytes still waiting to be written past the limit of %d (Options.MaxBytes), "+
		"so it and every later event were not written; the file holds every event recorded before it, and memory, queries and streams hold them all; "+
		"check that the disk holding the file is not stalled or full; the file stays stopped until the journal is reopened",
		e.Event, e.Size, e.Waiting, e.Limit)
}

// openSink opens the file o names and starts its writer, which queues at
// most limit estimated bytes.
func openSink(o FileOptions, limit int, open opener) (*sink, error) {
	if o.Path == "" {
		return nil, &OptionsError{Field: "File.Path", Reason: "is empty; name the NDJSON file to append events to"}
	}
	rotate, err := positive("File.RotateBytes", o.RotateBytes, DefaultRotateBytes)
	if err != nil {
		return nil, err
	}
	s := &sink{
		path:   filepath.Clean(o.Path),
		rotate: rotate,
		limit:  limit,
		open:   open,
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	if err := s.openFile(); err != nil {
		return nil, err
	}
	go s.run()
	return s, nil
}

// openOSFile opens path for appending. A file that does not end in a newline
// holds a record torn by a crash mid-write; openOSFile ends that line,
// keeping its bytes, so the torn record stays one unreadable line and every
// record after it decodes.
func openOSFile(path string) (file, int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, 0, err
	}
	size, err := endLastLine(f)
	if err != nil {
		return nil, 0, errors.Join(err, f.Close())
	}
	return f, size, nil
}

// endLastLine appends a newline to f unless it is empty or already ends in
// one, and returns f's size.
func endLastLine(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	if size == 0 {
		return 0, nil
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, size-1); err != nil {
		return 0, err
	}
	if last[0] == '\n' {
		return size, nil
	}
	n, err := f.Write([]byte{'\n'})
	return size + int64(n), err
}

// openFile opens the file for the writer.
func (s *sink) openFile() error {
	f, size, err := s.open(s.path)
	if err != nil {
		return &FileError{Path: s.path, Op: FileOpen, Err: err}
	}
	s.f, s.size = f, size
	return nil
}

// enqueue hands e, whose Estimate is size, to the writer, never blocking. It
// records a failure instead when the file was closed, or when the queue
// would pass limit; once a failure is recorded it drops e, since the file
// has stopped and the failure already reports it. Called with the journal
// lock held, so the queue follows recording order.
func (s *sink) enqueue(e Event, size int) {
	s.mu.Lock()
	switch {
	case s.err != nil:
	case s.closed:
		s.err = &FileError{Path: s.path, Op: FileWrite, Err: fmt.Errorf("event %d recorded after the journal was closed: %w", e.ID, os.ErrClosed)}
	case s.pending > 0 && s.pending+size > s.limit:
		s.err = &FileError{Path: s.path, Op: FileQueue, Err: &QueueFullError{Event: e.ID, Size: size, Waiting: s.pending, Limit: s.limit}}
	default:
		s.queue = append(s.queue, queued{event: e, size: size})
		s.pending += size
	}
	s.mu.Unlock()
	s.signal()
}

func (s *sink) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// run is the writer: it writes queued events in order until close was
// called and the queue is empty, then closes the file.
func (s *sink) run() {
	defer close(s.done)
	for {
		batch, ok := s.take()
		if !ok {
			s.closeFile()
			return
		}
		for i := range batch {
			s.write(batch[i].event)
			s.written(batch[i].size)
			batch[i] = queued{} // release the event as soon as it is written
		}
	}
}

// written stops counting an event of the given size as pending once the
// writer is done with it, so the queue bound counts only what the disk has
// not taken yet: a Write that hangs keeps counting its own event and every
// event after it.
func (s *sink) written(size int) {
	s.mu.Lock()
	s.pending -= size
	s.mu.Unlock()
}

// take waits for queued events and returns them all, or reports false once
// close was called and the queue is empty.
func (s *sink) take() ([]queued, bool) {
	for {
		s.mu.Lock()
		batch, closed := s.queue, s.closed
		s.queue = nil
		s.mu.Unlock()
		if len(batch) > 0 {
			return batch, true
		}
		if closed {
			return nil, false
		}
		<-s.wake
	}
}

// write appends one line unless a failure stopped the file. An encoding,
// write or rotation failure stops it. Append and Begin refuse events that
// cannot be encoded, so an encoding failure here is a journal bug.
func (s *sink) write(e Event) {
	if s.f == nil {
		return
	}
	line, err := e.MarshalJSON()
	if err != nil {
		s.stop(&FileError{Path: s.path, Op: FileEncode, Err: fmt.Errorf("event %d: %w", e.ID, err)})
		return
	}
	line = append(line, '\n')
	if s.size > 0 && s.size+int64(len(line)) > s.rotate {
		if err := s.rotateFile(); err != nil {
			s.stop(err)
			return
		}
	}
	n, err := s.f.Write(line)
	s.size += int64(n)
	if err != nil {
		s.stop(&FileError{Path: s.path, Op: FileWrite, Err: fmt.Errorf("event %d: %w", e.ID, err)})
	}
}

// rotateFile renames the full file to path.1 and starts an empty one.
func (s *sink) rotateFile() error {
	if err := s.f.Close(); err != nil {
		s.f = nil
		return &FileError{Path: s.path, Op: FileRotate, Err: err}
	}
	s.f = nil
	if err := os.Rename(s.path, s.path+".1"); err != nil {
		return &FileError{Path: s.path, Op: FileRotate, Err: err}
	}
	return s.openFile()
}

// fail keeps err unless an earlier failure was kept.
func (s *sink) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

// stop keeps err and closes the file.
func (s *sink) stop(err error) {
	s.fail(err)
	s.closeFile()
}

// closeFile closes the file, if still open.
func (s *sink) closeFile() {
	if s.f == nil {
		return
	}
	if err := s.f.Close(); err != nil {
		s.fail(&FileError{Path: s.path, Op: FileClose, Err: err})
	}
	s.f = nil
}

// failure returns the first failure, or nil.
func (s *sink) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// close waits for the writer to write every queued event and close the
// file, then returns the first failure. Calling it again returns the same.
func (s *sink) close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.signal()
	<-s.done
	return s.failure()
}
