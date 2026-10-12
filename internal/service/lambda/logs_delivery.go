package lambda

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// Function logs (ARCHITECTURE.md 14.6): one log stream per execution
// environment, fed by the container's output, with the lines Lambda writes
// itself around each invocation, written to CloudWatch Logs through
// PutLogEventsInternal as the execution role.

// The limits of one PutLogEvents batch (secrets-logs-events-alarms.md 3.3).
const (
	maxLogBatchEvents = 10_000
	maxLogBatchBytes  = 1_048_576
	logEventOverhead  = 26
)

// logGroup is the function's log group: the configured one or
// /aws/lambda/<name>.
func logGroup(name string, l Logging) string {
	if l.Group != "" {
		return l.Group
	}
	return "/aws/lambda/" + name
}

// LogSource follows a function container's output. ContainerLogs adapts a
// *launcher.Launcher.
type LogSource interface {
	Follow(ctx context.Context, containerID string) (Output, error)
}

// Output is one container's output as lines. Lines closes when the
// container stops, the context ends or following fails; Err then says
// which: nil after a clean end. *launcher.LogFollower implements it.
type Output interface {
	Lines() <-chan launcher.Line
	Err() error
}

// ContainerLogs follows containers' output through the launcher.
func ContainerLogs(l *launcher.Launcher) LogSource { return containerLogs{l: l} }

type containerLogs struct{ l *launcher.Launcher }

func (c containerLogs) Follow(ctx context.Context, containerID string) (Output, error) {
	f, err := c.l.Logs(ctx, containerID)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// logStream is one environment's log stream: the messages not yet cut
// into a batch, and the batches waiting to be written, in order.
type logStream struct {
	scope aws.Scope
	group string
	name  string
	role  aws.ARN
	clock *clock.Clock

	mu           sync.Mutex
	pending      []string
	pendingBytes int
	// current is the call the open invocation's output is written on
	// behalf of, from its begin until it ends; nil between invocations.
	current *kernel.Call
	// tail is the end of the open invocation's own log, from START, at
	// most maxTail bytes: what X-Amz-Log-Result carries.
	tail []byte
	// last is the call the stream's last batch was written on behalf of;
	// output arriving between invocations is written on its behalf too.
	last       *kernel.Call
	queue      []logBatch
	delivering bool
	// ready is set once the group and stream are known to exist; only the
	// goroutine delivering, which s.mu hands over, reads or writes it.
	ready bool
}

// logBatch is one cut of messages, the emulator instant it was cut at,
// which stamps every message, and the call they are written on behalf of.
type logBatch struct {
	parent   *kernel.Call
	at       clock.Instant
	messages []string
}

// openStream creates the log stream of a newly started environment, under
// the name the pool gave its container as AWS_LAMBDA_LOG_STREAM_NAME, and
// follows the container's output into it.
func (x *Execution) openStream(env *pool.Env, t *target, group string) *logStream {
	s := &logStream{
		scope: t.ref.scope,
		group: group,
		name:  env.LogStream(),
		role:  t.role,
		clock: x.k.Clock(),
	}
	if x.logSrc == nil {
		return s
	}
	id := env.Container().ID
	x.wg.Go(func() { x.follow(s, id) })
	return s
}

// follow feeds the container's output into s until the container stops,
// then delivers what no invocation cut.
func (x *Execution) follow(s *logStream, id string) {
	out, err := x.logSrc.Follow(x.ctx, id)
	if err != nil {
		x.k.Log().LogAttrs(x.ctx, slog.LevelWarn, "following a Lambda container's output failed; its function output is not delivered to CloudWatch Logs",
			slog.String("container", id), slog.String("log_stream", s.name), slog.String("error", err.Error()))
		return
	}
	for line := range out.Lines() {
		if s.add(lineMessage(line.Text)) {
			x.startDelivery(s)
		}
	}
	if err := out.Err(); err != nil && x.ctx.Err() == nil {
		x.k.Log().LogAttrs(x.ctx, slog.LevelWarn, "following a Lambda container's output stopped early; its later function output is not delivered to CloudWatch Logs",
			slog.String("container", id), slog.String("log_stream", s.name), slog.String("error", err.Error()))
	}
	if s.close() {
		x.startDelivery(s)
	}
}

// lineMessage is a line of output as a log event's message, ending in a
// newline as the line did. A line the launcher cut at MaxLineBytes had
// none, and adding one would make it an event no PutLogEvents request
// can carry.
func lineMessage(text string) string {
	if len(text) >= launcher.MaxLineBytes {
		return text
	}
	return text + "\n"
}

// The cuts below queue their batch under the stream's lock, so batches
// are written in the order they were cut, and report whether the caller
// must start the stream's delivery.

// add appends a line of output. Output that reaches a PutLogEvents
// request's size is cut on its own: during an invocation on its behalf,
// between invocations on behalf of the last one, so a function printing
// in a loop never makes understudy hold more than a request's worth.
func (s *logStream) add(message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, message)
	s.pendingBytes += len(message) + logEventOverhead
	if s.current != nil {
		s.keepTailLocked(message)
	}
	on := s.current
	if on == nil {
		on = s.last
	}
	if on == nil || s.pendingBytes < maxLogBatchBytes {
		return false
	}
	return s.queueLocked(on, s.takeLocked())
}

// close cuts what the container printed after the last invocation, once
// its output ended; an invocation still open cuts it itself.
func (s *logStream) close() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil || s.last == nil {
		return false
	}
	return s.queueLocked(s.last, s.takeLocked())
}

// begin opens an invocation written on behalf of parent: INIT_START on a
// cold start, then START, where the invocation's own log begins.
func (s *logStream) begin(cold bool, initStart, start string, parent *kernel.Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current, s.tail = parent, nil
	if cold {
		s.pending = append(s.pending, initStart)
		s.pendingBytes += len(initStart) + logEventOverhead
	}
	s.pending = append(s.pending, start)
	s.pendingBytes += len(start) + logEventOverhead
	s.keepTailLocked(start)
}

// end closes the open invocation with END and REPORT, and cuts every
// pending message into a batch written on its behalf: output that arrived
// between invocations goes ahead of START. tail is the end of the
// invocation's own log, at most maxTail bytes.
func (s *logStream) end(end, report string) (tail []byte, deliver bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, end, report)
	s.keepTailLocked(end)
	s.keepTailLocked(report)
	tail, parent := s.tail, s.current
	s.current, s.tail = nil, nil
	return tail, s.queueLocked(parent, s.takeLocked())
}

// cut closes the open invocation, which ended without a result, and cuts
// every pending message into a batch written on its behalf.
func (s *logStream) cut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.current
	s.current, s.tail = nil, nil
	return s.queueLocked(parent, s.takeLocked())
}

// keepTailLocked adds message to the open invocation's tail, keeping its
// last maxTail bytes. Callers hold s.mu.
func (s *logStream) keepTailLocked(message string) {
	if len(message) >= maxTail {
		s.tail = append(s.tail[:0], message[len(message)-maxTail:]...)
		return
	}
	s.tail = append(s.tail, message...)
	if over := len(s.tail) - maxTail; over > 0 {
		s.tail = append(s.tail[:0], s.tail[over:]...)
	}
}

// takeLocked empties pending. Callers hold s.mu.
func (s *logStream) takeLocked() []string {
	batch := s.pending
	s.pending, s.pendingBytes = nil, 0
	return batch
}

// queueLocked queues messages as a batch stamped with the emulator time
// now, and reports whether no delivery runs to write it. Callers hold
// s.mu.
func (s *logStream) queueLocked(parent *kernel.Call, messages []string) bool {
	if len(messages) == 0 {
		return false
	}
	s.last = parent
	s.queue = append(s.queue, logBatch{parent: parent, at: s.clock.Now(), messages: messages})
	if s.delivering {
		return false
	}
	s.delivering = true
	return true
}

// settleLogs is the settle barrier: it waits for a runtime's last output
// lines to reach the stream before the invocation is cut, through a
// context deadline because only internal/clock reads the wall clock.
// Without a LogSource no output is followed, so there is nothing to wait
// for; Close ends the wait.
func (x *Execution) settleLogs() {
	if x.logSrc == nil {
		return
	}
	wait, cancel := context.WithTimeout(x.ctx, x.settle)
	defer cancel()
	<-wait.Done()
}

// startDelivery writes the stream's queued batches in order, as tracked
// work, until the queue is empty.
func (x *Execution) startDelivery(s *logStream) {
	x.k.Go(kernel.DeliveryAttempt, func(ctx context.Context) {
		for {
			s.mu.Lock()
			if len(s.queue) == 0 {
				s.delivering = false
				s.mu.Unlock()
				return
			}
			b := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			x.write(ctx, s, b)
		}
	})
}

// write puts a batch's messages in requests within PutLogEvents' limits.
// A stream or group deleted since it was created is created again once.
func (x *Execution) write(ctx context.Context, s *logStream, b logBatch) {
	for _, events := range logRequests(b.messages, b.at.Wire().UnixMilli()) {
		err := x.put(ctx, s, b.parent, events)
		if err != nil && err.ID.Shape == logsapi.IdentityResourceNotFoundException().Shape {
			s.ready = false
			x.groups.forget(s.scope, s.group)
			err = x.put(ctx, s, b.parent, events)
		}
		if err != nil {
			x.k.Log().LogAttrs(ctx, slog.LevelWarn, "delivering Lambda function logs to CloudWatch Logs failed; these lines are lost",
				slog.String("log_group", s.group), slog.String("log_stream", s.name), slog.Int("events", len(events)),
				slog.String("error", err.ID.Shape.Name()+": "+err.Message))
		}
	}
}

// put writes one request, creating the group and the stream first unless
// they are known to exist; an existing one is not an error.
func (x *Execution) put(ctx context.Context, s *logStream, parent *kernel.Call, events []logsapi.InputLogEvent) *awserr.Error {
	target := kernel.Target{Scope: s.scope, As: kernel.AsExecutionRole{Role: s.role}, Via: ref.LambdaLogs}
	if !s.ready {
		if !x.groups.known(s.scope, s.group) {
			if _, err := kernel.Invoke(ctx, x.k, parent, logsapi.OpCreateLogGroup, target, &logsapi.CreateLogGroupInput{LogGroupName: s.group}); err != nil && !alreadyExists(err) {
				return err
			}
			x.groups.add(s.scope, s.group)
		}
		if _, err := kernel.Invoke(ctx, x.k, parent, logsapi.OpCreateLogStream, target, &logsapi.CreateLogStreamInput{LogGroupName: s.group, LogStreamName: s.name}); err != nil && !alreadyExists(err) {
			return err
		}
		s.ready = true
	}
	_, err := kernel.Invoke(ctx, x.k, parent, logsapi.OpPutLogEventsInternal, target, &logsapi.PutLogEventsInternalInput{LogGroupName: s.group, LogStreamName: s.name, LogEvents: events})
	return err
}

// logGroups are the log groups delivery created or found, so a cold start
// creates only its stream.
type logGroups struct {
	mu sync.Mutex
	m  map[groupKey]bool
}

type groupKey struct {
	scope aws.Scope
	name  string
}

func (g *logGroups) known(scope aws.Scope, name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.m[groupKey{scope, name}]
}

func (g *logGroups) add(scope aws.Scope, name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[groupKey]bool{}
	}
	g.m[groupKey{scope, name}] = true
}

func (g *logGroups) forget(scope aws.Scope, name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.m, groupKey{scope, name})
}

func alreadyExists(err *awserr.Error) bool {
	return err.ID.Shape == logsapi.IdentityResourceAlreadyExistsException().Shape
}

// logRequests splits messages into requests of at most 10,000 events and
// 1 MiB, counting 26 bytes per event as CloudWatch Logs does.
func logRequests(messages []string, at int64) [][]logsapi.InputLogEvent {
	var out [][]logsapi.InputLogEvent
	start, size := 0, 0
	for i, m := range messages {
		n := len(m) + logEventOverhead
		if i-start == maxLogBatchEvents || (i > start && size+n > maxLogBatchBytes) {
			out = append(out, logEvents(messages[start:i], at))
			start, size = i, 0
		}
		size += n
	}
	if start < len(messages) {
		out = append(out, logEvents(messages[start:], at))
	}
	return out
}

func logEvents(messages []string, at int64) []logsapi.InputLogEvent {
	events := make([]logsapi.InputLogEvent, len(messages))
	for i, m := range messages {
		events[i] = logsapi.InputLogEvent{Timestamp: at, Message: m}
	}
	return events
}

// The lines Lambda writes itself (lambda-api.md 9.7, text format). Each
// message ends in a newline, as Lambda's do.

// initStartLine opens a cold start. A managed runtime is named by its
// pinned image, python:3.13 or provided:al2023, with the runtime version
// ARN GetFunctionConfiguration reports; a container image function by its
// image URI.
func initStartLine(t *target) string {
	cfg := t.version.Config
	if code, ok := t.version.Code.(ImageCode); ok {
		return "INIT_START Runtime Version: " + code.URI + "\n"
	}
	version := cfg.Runtime
	if img, ok := launcher.ImageForRuntime(cfg.Runtime); ok {
		version = path.Base(img.Repository) + ":" + img.Tag
	}
	return "INIT_START Runtime Version: " + version + "\tRuntime Version ARN: " + runtimeVersionARN(t.ref.scope, cfg.Runtime) + "\n"
}

func startLine(request ids.LambdaRequestID, version string) string {
	return "START RequestId: " + string(request) + " Version: " + version + "\n"
}

func endLine(request ids.LambdaRequestID) string {
	return "END RequestId: " + string(request) + "\n"
}

// reportLine is REPORT with wall-measured durations, Max Memory Used, Init
// Duration on a cold start and the new-style Status of a failed runtime.
// Max Memory Used is the memory the container held when the invocation
// ended, rounded up to the MB, which is at most the peak AWS measures;
// used is nil when it could not be read, and the field is then left out
// rather than invented.
func reportLine(request ids.LambdaRequestID, duration time.Duration, memoryMB int32, used *uint64, cold bool, initDur time.Duration, o runtimeapi.Outcome) string {
	var b strings.Builder
	fmt.Fprintf(&b, "REPORT RequestId: %s\tDuration: %.2f ms\tBilled Duration: %d ms\tMemory Size: %d MB\t",
		request, millis(duration), billed(duration), memoryMB)
	if used != nil {
		fmt.Fprintf(&b, "Max Memory Used: %d MB\t", max(1, (*used+1<<20-1)>>20))
	}
	if cold {
		fmt.Fprintf(&b, "Init Duration: %.2f ms\t", millis(initDur))
	}
	switch o := o.(type) {
	case runtimeapi.Timeout:
		b.WriteString("Status: timeout\t")
	case runtimeapi.ExitError:
		b.WriteString("Status: error\tError Type: Runtime.ExitError\t")
	case runtimeapi.OOM:
		b.WriteString("Status: error\tError Type: Runtime.OutOfMemory\t")
	case runtimeapi.InitError:
		b.WriteString("Status: error\tError Type: " + o.Type + "\t")
	case runtimeapi.ArchMismatch:
		b.WriteString("Status: error\tError Type: Runtime.InvalidEntrypoint\t")
	case runtimeapi.Success, runtimeapi.FunctionError:
	}
	b.WriteString("\n")
	return b.String()
}

func millis(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// billed is Billed Duration: the duration rounded up to the millisecond,
// at least 1 ms.
func billed(d time.Duration) int64 {
	return max(1, int64(math.Ceil(millis(d))))
}
