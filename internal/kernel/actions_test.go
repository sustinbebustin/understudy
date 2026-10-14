package kernel_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// deployInput is the input of the test service's operations: Tagged and
// Role stand for members that need a secondary action and name a resource
// of another service.
type deployInput struct {
	Tagged bool
	Role   string
}

type deployOutput struct{}

// deployService is a service whose Deploy lists a primary action, a
// same-service secondary, an action of another service that acts on IAM
// roles (iam:PassRole) and one of a service no call touches
// (s3-object-lambda); WriteLogs is internal and lists none.
func deployService() (svc *registry.Service, deploy, writeLogs kernel.Op[deployInput, deployOutput]) {
	svc = &registry.Service{ID: "deploy", SigningName: "deploy", IAMPrefix: "deploy"}
	d := &registry.Operation{Service: svc, ID: deployOpID("Deploy"), Ordinal: 0, Kind: registry.Modeled, IAM: []registry.IAMAction{
		{Prefix: "deploy", Name: "Deploy"}, {Prefix: "deploy", Name: "TagResource"}, {Prefix: "iam", Name: "PassRole"}, {Prefix: "s3-object-lambda", Name: "Deploy"},
	}}
	w := &registry.Operation{Service: svc, ID: deployOpID("WriteLogs"), Ordinal: 1, Kind: registry.Internal}
	svc.Operations = []*registry.Operation{d, w}
	return svc, kernel.NewOp[deployInput, deployOutput](d), kernel.NewOp[deployInput, deployOutput](w)
}

func deployOpID(name string) ref.OperationID {
	return ref.OperationID{Service: "deploy", Name: name}
}

var (
	deployTag  = registry.IAMAction{Prefix: "deploy", Name: "TagResource"}
	deployRole = "arn:aws:iam::000000000000:role/runner"
)

// deployed answers every call.
func deployed(context.Context, *kernel.Call, *deployInput) (*deployOutput, *awserr.Error) {
	return &deployOutput{}, nil
}

// passedRole touches the role an input passes.
func passedRole(_ *kernel.Call, in *deployInput) []aws.ARN {
	if in.Role == "" {
		return nil
	}
	a, err := aws.ParseARN(in.Role)
	if err != nil {
		return nil
	}
	return []aws.ARN{a}
}

// tagging declares deploy:TagResource for a tagged input.
func tagging(_ *kernel.Call, in *deployInput) []registry.IAMAction {
	if in.Tagged {
		return []registry.IAMAction{deployTag}
	}
	return nil
}

// usedActions returns every recorded action, with the resource text it is
// recorded on.
func usedActions(k *kerneltest.K) []string {
	uses := k.Access.Usage(access.Filter{})
	out := make([]string, 0, len(uses))
	for i := range uses {
		on := "*"
		if r, ok := uses[i].Resource.(access.OnResource); ok {
			on = r.ARN.String()
		}
		out = append(out, uses[i].Action.String()+" on "+on)
	}
	return out
}

// syncBuffer is a log destination safe for the kernel's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func runDeploy(t *testing.T, fn func(k *kerneltest.K, deploy, writeLogs kernel.Op[deployInput, deployOutput]), opts ...kerneltest.Option) {
	t.Helper()
	svc, deploy, writeLogs := deployService()
	kerneltest.Run(t, func(k *kerneltest.K) { fn(k, deploy, writeLogs) }, append(opts, kerneltest.Services(svc))...)
}

func invokeDeploy(k *kerneltest.K, op kernel.Op[deployInput, deployOutput], in deployInput) {
	k.Helper()
	if _, err := k.Invoke(k.Context(), op, &in); err != nil {
		k.Fatalf("%s: %s", op.Descriptor().ID, err.Message)
	}
}

// A binding that declares nothing about its actions records every action
// of the generated list, an over-grant rather than an under-grant; an
// action of another service is still recorded only on a touched resource
// of that service.
func TestAccessRecordsTheWholeListForAnUndeclaredBinding(t *testing.T) {
	runDeploy(t, func(k *kerneltest.K, deploy, _ kernel.Op[deployInput, deployOutput]) {
		kernel.Bind(k.Kernel.Binder(deploy.Descriptor().Service), deploy, deployed, registry.Full{}, kernel.WithResources(passedRole))
		if k.Kernel.Bindings().DeclaresActions(deploy.Descriptor()) {
			t.Error("DeclaresActions = true for a binding without WithActions")
		}
		invokeDeploy(k, deploy, deployInput{Role: deployRole})
		want := []string{"deploy:Deploy on *", "deploy:TagResource on *", "iam:PassRole on " + deployRole}
		if got := usedActions(k); !slices.Equal(got, want) {
			t.Errorf("recorded %v, want %v", got, want)
		}
	})
}

// WithoutSecondaryActions records the decision that no call needs a
// secondary: the primary action alone.
func TestAccessRecordsThePrimaryActionAloneWithoutSecondaryActions(t *testing.T) {
	runDeploy(t, func(k *kerneltest.K, deploy, _ kernel.Op[deployInput, deployOutput]) {
		kernel.Bind(k.Kernel.Binder(deploy.Descriptor().Service), deploy, deployed, registry.Full{}, kernel.WithoutSecondaryActions())
		if !k.Kernel.Bindings().DeclaresActions(deploy.Descriptor()) {
			t.Error("DeclaresActions = false for a binding WithoutSecondaryActions")
		}
		invokeDeploy(k, deploy, deployInput{Tagged: true})
		if got, want := usedActions(k), []string{"deploy:Deploy on *"}; !slices.Equal(got, want) {
			t.Errorf("recorded %v, want %v", got, want)
		}
	})
}

// A call records its operation's primary action and none of the
// same-service secondaries its input did not declare.
func TestAccessRecordsThePrimaryActionByDefault(t *testing.T) {
	runDeploy(t, func(k *kerneltest.K, deploy, _ kernel.Op[deployInput, deployOutput]) {
		kernel.Bind(k.Kernel.Binder(deploy.Descriptor().Service), deploy, deployed, registry.Full{}, kernel.WithActions(tagging))
		invokeDeploy(k, deploy, deployInput{})
		if got, want := usedActions(k), []string{"deploy:Deploy on *"}; !slices.Equal(got, want) {
			t.Errorf("recorded %v, want %v", got, want)
		}
	})
}

// A secondary the input declares is recorded beside the primary.
func TestAccessRecordsADeclaredSecondaryAction(t *testing.T) {
	runDeploy(t, func(k *kerneltest.K, deploy, _ kernel.Op[deployInput, deployOutput]) {
		kernel.Bind(k.Kernel.Binder(deploy.Descriptor().Service), deploy, deployed, registry.Full{}, kernel.WithActions(tagging))
		invokeDeploy(k, deploy, deployInput{Tagged: true})
		if got, want := usedActions(k), []string{"deploy:Deploy on *", "deploy:TagResource on *"}; !slices.Equal(got, want) {
			t.Errorf("recorded %v, want %v", got, want)
		}
	})
}

// A declared action outside the operation's generated list is a bind-site
// bug: logged at Warn and not recorded.
func TestAccessDropsAndLogsADeclaredActionOutsideTheGeneratedList(t *testing.T) {
	var logs syncBuffer
	runDeploy(t, func(k *kerneltest.K, deploy, _ kernel.Op[deployInput, deployOutput]) {
		bogus := registry.IAMAction{Prefix: "deploy", Name: "Undeploy"}
		kernel.Bind(k.Kernel.Binder(deploy.Descriptor().Service), deploy, deployed, registry.Full{},
			kernel.WithActions(func(*kernel.Call, *deployInput) []registry.IAMAction { return []registry.IAMAction{bogus} }))
		invokeDeploy(k, deploy, deployInput{})
		if got, want := usedActions(k), []string{"deploy:Deploy on *"}; !slices.Equal(got, want) {
			t.Errorf("recorded %v, want %v", got, want)
		}
		out := logs.String()
		if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "bug:") || !strings.Contains(out, "action=deploy:Undeploy") {
			t.Errorf("log = %q, want a Warn naming deploy:Undeploy as a bind-site bug", out)
		}
	}, kerneltest.Log(slog.New(slog.NewTextHandler(&logs, nil))))
}

// An action of another service is recorded only on a touched resource of
// that service: iam:PassRole on the passed role, and not at all, rather
// than on "*", when the input passed none. An action of a service no call
// touches (s3-object-lambda) is never recorded.
func TestAccessRecordsAnotherServicesActionOnlyOnItsTouchedResource(t *testing.T) {
	runDeploy(t, func(k *kerneltest.K, deploy, _ kernel.Op[deployInput, deployOutput]) {
		kernel.Bind(k.Kernel.Binder(deploy.Descriptor().Service), deploy, deployed, registry.Full{},
			kernel.WithResources(passedRole), kernel.WithoutSecondaryActions())
		invokeDeploy(k, deploy, deployInput{})
		if got, want := usedActions(k), []string{"deploy:Deploy on *"}; !slices.Equal(got, want) {
			t.Errorf("without a role: recorded %v, want %v", got, want)
		}
		k.Access.Reset()
		invokeDeploy(k, deploy, deployInput{Role: deployRole})
		if got, want := usedActions(k), []string{"deploy:Deploy on *", "iam:PassRole on " + deployRole}; !slices.Equal(got, want) {
			t.Errorf("with a role: recorded %v, want %v", got, want)
		}
	})
}

// An internal operation has no generated list: a call records nothing
// unless the binding declares actions, and then exactly those.
func TestAccessRecordsAnInternalOperationOnlyWhenDeclared(t *testing.T) {
	runDeploy(t, func(k *kerneltest.K, _, writeLogs kernel.Op[deployInput, deployOutput]) {
		kernel.Bind(k.Kernel.Binder(writeLogs.Descriptor().Service), writeLogs, deployed, registry.Full{})
		invokeDeploy(k, writeLogs, deployInput{})
		if got := usedActions(k); len(got) != 0 {
			t.Errorf("undeclared: recorded %v, want nothing", got)
		}
	})
	put := registry.IAMAction{Prefix: "deploy", Name: "PutLogs"}
	runDeploy(t, func(k *kerneltest.K, _, writeLogs kernel.Op[deployInput, deployOutput]) {
		kernel.Bind(k.Kernel.Binder(writeLogs.Descriptor().Service), writeLogs, deployed, registry.Full{},
			kernel.WithActions(func(*kernel.Call, *deployInput) []registry.IAMAction { return []registry.IAMAction{put} }))
		invokeDeploy(k, writeLogs, deployInput{})
		if got, want := usedActions(k), []string{"deploy:PutLogs on *"}; !slices.Equal(got, want) {
			t.Errorf("declared: recorded %v, want %v", got, want)
		}
	})
}

// Declare joins options to an operation's binding from outside its bind
// site, through any binder of the service, keeping the bind site's own.
func TestDeclareJoinsTheBindingsOptions(t *testing.T) {
	runDeploy(t, func(k *kerneltest.K, deploy, _ kernel.Op[deployInput, deployOutput]) {
		svc := deploy.Descriptor().Service
		kernel.Bind(k.Kernel.Binder(svc), deploy, deployed, registry.Full{}, kernel.WithResources(passedRole))
		kernel.Declare(k.Kernel.Binder(svc), deploy, kernel.WithActions(tagging))
		if err := k.Kernel.BindErr(); err != nil {
			t.Fatalf("BindErr = %v", err)
		}
		invokeDeploy(k, deploy, deployInput{Tagged: true, Role: deployRole})
		want := []string{"deploy:Deploy on *", "deploy:TagResource on *", "iam:PassRole on " + deployRole}
		if got := usedActions(k); !slices.Equal(got, want) {
			t.Errorf("recorded %v, want %v", got, want)
		}
	})
}

// A Declare before its operation is bound has no binding to join, so its
// options would silently not apply: it is a bind mistake.
func TestDeclareBeforeBindIsABindMistake(t *testing.T) {
	k := newKernel(t)
	b := k.Binder(sqsapi.Service())
	kernel.Declare(b, sqsapi.OpSendMessage, kernel.WithResources(func(*kernel.Call, *sqsapi.SendMessageInput) []aws.ARN { return nil }))
	kernel.Bind(b, sqsapi.OpSendMessage, func(context.Context, *kernel.Call, *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
		return &sqsapi.SendMessageOutput{}, nil
	}, registry.Full{})
	if bind, ok := errors.AsType[*kernel.BindError](k.BindErr()); !ok || bind.Problem != kernel.DeclaredUnbound {
		t.Errorf("BindErr = %v, want a DeclaredUnbound BindError", k.BindErr())
	}
}

// A raw HTTP binding takes no options, so a Declare for it is a bind
// mistake rather than options that never apply.
func TestDeclareForARawOperationIsABindMistake(t *testing.T) {
	svc := &registry.Service{ID: "stream", SigningName: "stream", IAMPrefix: "stream"}
	desc := &registry.Operation{Service: svc, ID: ref.OperationID{Service: "stream", Name: "Stream"}, Kind: registry.Raw}
	svc.Operations = []*registry.Operation{desc}
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(svc)
	if err != nil {
		t.Fatal(err)
	}
	k, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: ids.NewSeeded(1), Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	b := k.Binder(svc)
	kernel.BindRawHTTP(b, desc, func(context.Context, *kernel.Call, *http.Request, http.ResponseWriter) *awserr.Error { return nil }, registry.Full{})
	kernel.Declare(b, kernel.NewOp[deployInput, deployOutput](desc), kernel.WithActions(tagging))
	if bind, ok := errors.AsType[*kernel.BindError](k.BindErr()); !ok || bind.Problem != kernel.DeclaredRaw {
		t.Errorf("BindErr = %v, want a DeclaredRaw BindError", k.BindErr())
	}
}
