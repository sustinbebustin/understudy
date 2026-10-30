package scheduler_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/scheduler"
)

// settle is how long a test waits for the kernel's tracked work and due
// timers, in the bubble's fake time.
const settle = time.Minute

// roleName is the execution role every schedule names; IAM (stubbed)
// resolves it to roleID, the AROA id of the dead-letter example's SenderId
// (aws-doc https://docs.aws.amazon.com/scheduler/latest/UserGuide/configuring-schedule-dlq.html).
const (
	roleName = "sch-role"
	roleID   = "AROA2DZE3W4CTL5ZR7EIN"
)

// sessionName is the role session name form: 32 lower-case hex digits, as
// the CloudTrail example's assumed-role/<role>/70abcd123a123a12345a1aa12aa1bc12
// and the SenderId example show (aws-doc encryption-rest,
// configuring-schedule-dlq; scheduler.md 7.2, 9).
var sessionName = regexp.MustCompile(`^[0-9a-f]{32}$`)

// executionID is the EXECUTION_ID form, 16 lower-case hex digits
// (aws-doc configuring-schedule-dlq; scheduler.md 7.2, 13).
var executionID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// targetCall is one call a stubbed target received.
type targetCall struct {
	op        string // SendMessage, Invoke, PutEvents, Publish, StartExecution
	at        clock.Instant
	principal identity.Principal
	origin    ref.Origin
	sqs       sqsapi.SendMessageInput
	lambda    lambdaapi.InvokeInput
	events    eventbridgeapi.PutEventsInput
	sns       snsapi.PublishInput
	sfn       sfnapi.StartExecutionInput
}

// queue is the queue name a SendMessage addressed.
func (c *targetCall) queue() string {
	return c.sqs.QueueUrl[strings.LastIndexByte(c.sqs.QueueUrl, '/')+1:]
}

// sink records every delivery the kernel hands over, then gives it to the
// outbox.
type sink struct {
	box *outbox.Outbox
	mu  sync.Mutex
	got []kernel.Delivery
}

func (s *sink) Deliver(d kernel.Delivery) error {
	s.mu.Lock()
	s.got = append(s.got, d)
	s.mu.Unlock()
	return s.box.Deliver(d)
}

func (s *sink) all() []kernel.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]kernel.Delivery(nil), s.got...)
}

// harness is a running kernel with Scheduler, the outbox behind a
// recording sink, a stub IAM ResolveRole, and stub targets (SQS
// SendMessage, Lambda Invoke, EventBridge PutEvents, SNS Publish, Step
// Functions StartExecution) that record what they receive. queueAnswer
// decides each SendMessage's error.
type harness struct {
	*kerneltest.K
	svc  *scheduler.Service
	sink *sink

	mu          sync.Mutex
	calls       []*targetCall
	queueAnswer func(queue string, n int) *awserr.Error
	queueCalls  map[string]int
	missingRole bool
}

type harnessOptions struct {
	scheduler  scheduler.Options
	seed       uint64
	authorizer kernel.Authorizer
}

func run(t *testing.T, fn func(h *harness)) {
	t.Helper()
	runWith(t, harnessOptions{}, fn)
}

func runWith(t *testing.T, o harnessOptions, fn func(h *harness)) {
	t.Helper()
	if o.seed == 0 {
		o.seed = 1
	}
	synctest.Test(t, func(t *testing.T) {
		h := build(t, o)
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan error, 1)
		go func() { stopped <- h.Kernel.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-stopped; err != nil {
				t.Errorf("kernel.Run = %v", err)
			}
			if err := h.Kernel.BindErr(); err != nil {
				t.Errorf("bind mistakes: %v", err)
			}
		}()
		fn(h)
	})
}

func build(t *testing.T, o harnessOptions) *harness {
	t.Helper()
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(schedulerapi.Service(), sqsapi.Service(), lambdaapi.Service(), eventbridgeapi.Service(),
		snsapi.Service(), sfnapi.Service(), iamapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	seed := o.seed
	src := ids.NewSeeded(seed)
	faults := fault.New(&seed, src, kernel.NewCatalogue(reg, nil))
	rec := &access.Recorder{}
	idStore := identity.New(src, aws.DefaultAccount())
	sk := &sink{box: outbox.New()}
	k, err := kernel.New(kernel.Options{
		Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j,
		Faults: faults, Access: rec, Identity: idStore, Authorizer: o.authorizer, Deliver: sk, Strict: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sk.box.Bind(k, outbox.Options{Faults: faults}); err != nil {
		t.Fatal(err)
	}
	kt := &kerneltest.K{
		T: t, Kernel: k, Clock: clk, Journal: j, Faults: faults, Access: rec, Identity: idStore,
		Scope:    aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()},
		Endpoint: kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
	}
	h := &harness{K: kt, sink: sk, queueCalls: map[string]int{}}
	h.queueAnswer = func(string, int) *awserr.Error { return nil }
	h.stubTargets()
	h.svc = scheduler.New(kt.Deps(schedulerapi.Service()), o.scheduler)
	return h
}

func (h *harness) record(c *kernel.Call, tc targetCall) {
	tc.at = c.Admitted()
	tc.principal = c.Principal()
	tc.origin = c.Origin()
	h.mu.Lock()
	h.calls = append(h.calls, &tc)
	h.mu.Unlock()
}

func (h *harness) stubTargets() {
	h.Stub(iamapi.OpResolveRole, func(_ context.Context, _ *kernel.Call, in *iamapi.ResolveRoleInput) (*iamapi.ResolveRoleOutput, *awserr.Error) {
		h.mu.Lock()
		missing := h.missingRole
		h.mu.Unlock()
		if missing || !strings.HasSuffix(in.RoleArn, ":role/"+roleName) {
			return nil, iamapi.ErrNoSuchEntityException("The role with name " + in.RoleArn + " cannot be found.")
		}
		return &iamapi.ResolveRoleOutput{Role: iamapi.Role{Path: "/", RoleName: roleName, RoleId: roleID, Arn: in.RoleArn}}, nil
	})
	h.Stub(sqsapi.OpSendMessage, func(_ context.Context, c *kernel.Call, in *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
		tc := targetCall{op: "SendMessage", sqs: *in}
		h.record(c, tc)
		h.mu.Lock()
		n := h.queueCalls[tc.queue()]
		h.queueCalls[tc.queue()]++
		answer := h.queueAnswer
		h.mu.Unlock()
		if err := answer(tc.queue(), n); err != nil {
			return nil, err
		}
		return &sqsapi.SendMessageOutput{MessageId: new("m")}, nil
	})
	h.Stub(lambdaapi.OpInvoke, func(_ context.Context, c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
		h.record(c, targetCall{op: "Invoke", lambda: *in})
		return &lambdaapi.InvokeOutput{StatusCode: 202}, nil
	})
	h.Stub(eventbridgeapi.OpPutEvents, func(_ context.Context, c *kernel.Call, in *eventbridgeapi.PutEventsInput) (*eventbridgeapi.PutEventsOutput, *awserr.Error) {
		h.record(c, targetCall{op: "PutEvents", events: *in})
		out := &eventbridgeapi.PutEventsOutput{}
		for range in.Entries {
			out.Entries = append(out.Entries, eventbridgeapi.PutEventsResultEntry{EventId: new("e")})
		}
		return out, nil
	})
	h.Stub(snsapi.OpPublish, func(_ context.Context, c *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
		h.record(c, targetCall{op: "Publish", sns: *in})
		return &snsapi.PublishOutput{MessageId: new("m")}, nil
	})
	h.Stub(sfnapi.OpStartExecution, func(_ context.Context, c *kernel.Call, in *sfnapi.StartExecutionInput) (*sfnapi.StartExecutionOutput, *awserr.Error) {
		h.record(c, targetCall{op: "StartExecution", sfn: *in})
		return &sfnapi.StartExecutionOutput{ExecutionArn: in.StateMachineArn + ":x", StartDate: c.Admitted().Wire()}, nil
	})
}

// answerQueues sets how SendMessage answers.
func (h *harness) answerQueues(fn func(queue string, n int) *awserr.Error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queueAnswer = fn
}

// targetCalls returns what the stubbed targets received, in order.
func (h *harness) targetCalls() []*targetCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*targetCall(nil), h.calls...)
}

// sentTo returns the SendMessage calls to queue.
func (h *harness) sentTo(queue string) []*targetCall {
	var out []*targetCall
	for _, c := range h.targetCalls() {
		if c.op == "SendMessage" && c.queue() == queue {
			out = append(out, c)
		}
	}
	return out
}

// callsOf returns the calls of one target operation.
func (h *harness) callsOf(op string) []*targetCall {
	var out []*targetCall
	for _, c := range h.targetCalls() {
		if c.op == op {
			out = append(out, c)
		}
	}
	return out
}

func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return h.Invoke(h.Context(), op, in)
}

func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s (%d): %s", op.Descriptor().ID.Name, err.ID.Shape.Name(), err.ID.Status, err.Message)
	}
	return out
}

func errOf[Out any](_ *Out, err *awserr.Error) *awserr.Error { return err }

// wantError fails unless err is the shape (by name) with status, and with
// message when message is not empty.
func wantError(tb testing.TB, err *awserr.Error, shape string, status int, message string) {
	tb.Helper()
	switch {
	case err == nil:
		tb.Fatalf("no error, want %s %d %q", shape, status, message)
	case err.ID.Shape.Name() != shape || err.ID.Status != status || (message != "" && err.Message != message):
		tb.Fatalf("error = %s %d %q\nwant  %s %d %q", err.ID.Shape.Name(), err.ID.Status, err.Message, shape, status, message)
	}
}

func (h *harness) settled() {
	h.Helper()
	if p, err := h.Kernel.Settle(h.Context(), settle); err != nil {
		h.Fatalf("Settle = %v, %v", p, err)
	}
}

// advance moves the emulator clock and settles what came due.
func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
	h.settled()
}

// advanceBy moves the clock in steps of step, settling after each.
func (h *harness) advanceBy(total, step time.Duration) {
	h.Helper()
	for d := time.Duration(0); d < total; d += step {
		h.advance(step)
	}
}

func (h *harness) now() time.Time { return h.Clock.Now().Wire() }

func (h *harness) roleArn() string {
	return fmt.Sprintf("arn:aws:iam::%s:role/%s", h.Scope.Account, roleName)
}

func (h *harness) queueArn(name string) string {
	return fmt.Sprintf("arn:aws:sqs:%s:%s:%s", h.Scope.Region, h.Scope.Account, name)
}

func (h *harness) functionArn(name string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", h.Scope.Region, h.Scope.Account, name)
}

func (h *harness) topicArn(name string) string {
	return fmt.Sprintf("arn:aws:sns:%s:%s:%s", h.Scope.Region, h.Scope.Account, name)
}

func (h *harness) busArn(name string) string {
	return fmt.Sprintf("arn:aws:events:%s:%s:event-bus/%s", h.Scope.Region, h.Scope.Account, name)
}

func (h *harness) machineArn(name string) string {
	return fmt.Sprintf("arn:aws:states:%s:%s:stateMachine:%s", h.Scope.Region, h.Scope.Account, name)
}

func (h *harness) keyArn() string {
	return fmt.Sprintf("arn:aws:kms:%s:%s:key/1234abcd-12ab-34cd-56ef-1234567890ab", h.Scope.Region, h.Scope.Account)
}

func (h *harness) scheduleArn(group, name string) string {
	return fmt.Sprintf("arn:aws:scheduler:%s:%s:schedule/%s/%s", h.Scope.Region, h.Scope.Account, group, name)
}

func (h *harness) groupArn(name string) string {
	return fmt.Sprintf("arn:aws:scheduler:%s:%s:schedule-group/%s", h.Scope.Region, h.Scope.Account, name)
}

// queueTarget is an SQS target with the harness role.
func (h *harness) queueTarget(queue string, input *string) schedulerapi.Target {
	return schedulerapi.Target{Arn: h.queueArn(queue), RoleArn: h.roleArn(), Input: input}
}

// schedule is a CreateScheduleInput as Terraform sends it for the basic
// configuration: name, expression, window OFF, target, and the provider's
// defaults ScheduleExpressionTimezone UTC and State ENABLED (tf
// schedule.go:L136-L152; scheduler.md 4.2).
func schedule(name, expr string, target schedulerapi.Target) *schedulerapi.CreateScheduleInput {
	return &schedulerapi.CreateScheduleInput{
		Name:                       name,
		ScheduleExpression:         expr,
		ScheduleExpressionTimezone: new("UTC"),
		State:                      new(schedulerapi.ScheduleStateEnabled),
		Target:                     target,
		FlexibleTimeWindow:         schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeOff},
	}
}

// create creates the schedule and returns its ARN.
func (h *harness) create(in *schedulerapi.CreateScheduleInput) string {
	h.Helper()
	return must(h, schedulerapi.OpCreateSchedule, in).ScheduleArn
}

func (h *harness) get(group, name string) *schedulerapi.GetScheduleOutput {
	h.Helper()
	in := &schedulerapi.GetScheduleInput{Name: name}
	if group != "" {
		in.GroupName = new(group)
	}
	return must(h, schedulerapi.OpGetSchedule, in)
}

// update builds an UpdateScheduleInput with every member of in.
func update(in *schedulerapi.CreateScheduleInput) *schedulerapi.UpdateScheduleInput {
	return &schedulerapi.UpdateScheduleInput{
		Name: in.Name, GroupName: in.GroupName, ScheduleExpression: in.ScheduleExpression,
		StartDate: in.StartDate, EndDate: in.EndDate, Description: in.Description,
		ScheduleExpressionTimezone: in.ScheduleExpressionTimezone, State: in.State, KmsKeyArn: in.KmsKeyArn,
		Target: in.Target, FlexibleTimeWindow: in.FlexibleTimeWindow, ActionAfterCompletion: in.ActionAfterCompletion,
	}
}

// wantRoleSession fails unless p is a session of the schedule's execution
// role: Scheduler assumes the role to call the target (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/setting-up.html;
// scheduler.md 1 item 1, 9, 14.4.4).
func wantRoleSession(tb testing.TB, what string, p identity.Principal) identity.AssumedRole {
	tb.Helper()
	r, ok := p.(identity.AssumedRole)
	if !ok || r.Account != aws.DefaultAccount() || r.Role.Name != roleName || r.Role.ID != roleID || !sessionName.MatchString(r.Session) {
		tb.Errorf("%s ran as %#v, want a 32-hex session of role %s (%s)", what, p, roleName, roleID)
	}
	return r
}

var schedulerOrigin ref.Origin = ref.Internal{Via: ref.SchedulerTarget}

func val(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
