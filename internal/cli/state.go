package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

// pollInterval is how often health --wait asks again.
const pollInterval = 100 * time.Millisecond

// pause waits d, or less when ctx ends first.
func pause(ctx context.Context, d time.Duration) {
	wait, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	<-wait.Done()
}

func healthCmd() *command {
	return leaf("health", "Report whether understudy is ready, and the state of each service", "health [--wait 30s]",
		[]string{"understudy health", "understudy health --wait 30s", "understudy health --output json --endpoint http://127.0.0.1:4566"},
		func(fs *flag.FlagSet) action {
			wait := new(time.Duration)
			durationVar(fs, wait, "wait", 0, "keep asking until ready or this long has passed; exit 4 when still not ready, 3 when never reached")
			return func(ctx context.Context, r *runner, args []string) error {
				if err := wantArgs(args, 0, "no arguments"); err != nil {
					return err
				}
				c, err := r.client()
				if err != nil {
					return err
				}
				h, err := waitHealthy(ctx, c, r.endpointURL(), *wait)
				if err != nil {
					return err
				}
				if err := r.emit(h, func(w io.Writer) error { return writeHealth(w, h) }); err != nil {
					return err
				}
				if !h.Ready {
					return &admin.Error{
						Code:    admin.ConditionNotMet,
						Message: "understudy is not ready; it is still starting " + strings.Join(h.Waiting, ", "),
						Hint:    "wait for it with 'understudy health --wait 30s'",
					}
				}
				return nil
			}
		})
}

// healthAttempt bounds one health call, so a listener that accepts and
// never answers is asked again, or reported, rather than waited on.
const healthAttempt = 5 * time.Second

// waitHealthy asks the server at endpoint for health once, or with wait
// set until it is ready or wait has passed, every attempt included. Once
// any answer came, a later failure to reach the server reports the last
// answer rather than the failure.
func waitHealthy(ctx context.Context, c *admin.Client, endpoint string, wait time.Duration) (*admin.Health, error) {
	if wait <= 0 {
		return askHealth(ctx, c, endpoint)
	}
	deadline, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var last *admin.Health
	for {
		h, err := askHealth(deadline, c, endpoint)
		if err == nil {
			if h.Ready {
				return h, nil
			}
			last = h
		}
		pause(deadline, pollInterval)
		if deadline.Err() != nil {
			if last != nil {
				return last, nil
			}
			return nil, err
		}
	}
}

// askHealth is one health call, bounded by healthAttempt and by ctx. A
// call that got no answer in time is unreachable, saying so; whether the
// connection or the answer stalled is not known.
func askHealth(ctx context.Context, c *admin.Client, endpoint string) (*admin.Health, error) {
	attempt, cancel := context.WithTimeout(ctx, healthAttempt)
	defer cancel()
	h, err := c.Health(attempt)
	if err != nil && errors.Is(attempt.Err(), context.DeadlineExceeded) {
		return nil, &admin.UnreachableError{Endpoint: endpoint, Err: fmt.Errorf(
			"no answer came in time (an attempt waits up to %s, and no longer than --wait); the process on that port may be wedged or may not be understudy", healthAttempt)}
	}
	return h, err
}

func writeHealth(w io.Writer, h *admin.Health) error {
	if !h.Ready {
		_, err := fmt.Fprintf(w, "not ready: starting %s\n", strings.Join(h.Waiting, ", "))
		return err
	}
	tw := newTable(w)
	fmt.Fprintf(tw, "ready: understudy %s, up %s, models %s\n", h.Version, time.Duration(h.Uptime).Round(time.Second), h.Model)
	// A running clock follows wall time, so only a frozen or scaled one,
	// which a test may have left that way, gets a line.
	switch h.Clock.Mode {
	case admin.Frozen:
		fmt.Fprintf(tw, "clock: frozen at %s\n", h.Clock.Now.UTC().Format(time.RFC3339Nano))
	case admin.Scaled:
		fmt.Fprintf(tw, "clock: scaled x%s, now %s\n", strconv.FormatFloat(h.Clock.Factor, 'g', -1, 64), h.Clock.Now.UTC().Format(time.RFC3339Nano))
	case admin.Running:
	}
	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "COMPONENT\tSTATUS\tREASON")
	// A row down for want of Docker or DynamoDB Local carries that
	// component's reason; it names the component instead, whose own row
	// gives the reason once. causes runs from the root, Docker, which
	// DynamoDB Local needs, so a row names only a cause before its own.
	causes := []struct {
		name string
		s    admin.ServiceStatus
	}{{"docker", h.Docker}, {"dynamodb-local", h.DynamoDBLocal}}
	row := func(name string, s admin.ServiceStatus) {
		reason := s.Reason
		for _, cause := range causes {
			if cause.name == name {
				break
			}
			if reason != "" && cause.s.Reason == reason {
				reason = "needs " + cause.name + ", which is " + string(cause.s.Status)
				break
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, s.Status, reason)
	}
	for _, name := range slices.Sorted(maps.Keys(h.Services)) {
		row(name, h.Services[name])
	}
	for _, c := range []struct {
		name string
		s    admin.ServiceStatus
	}{{"docker", h.Docker}, {"dynamodb-local", h.DynamoDBLocal}, {"journal", h.Journal}} {
		if c.s.Status != "" {
			row(c.name, c.s)
		}
	}
	return tw.Flush()
}

func settleCmd() *command {
	return leaf("settle", "Wait until no tracked work (deliveries, invocations, polls) is pending", "settle [--timeout 30s]",
		[]string{"understudy settle", "understudy settle --timeout 2m --output json"},
		func(fs *flag.FlagSet) action {
			timeout := new(time.Duration)
			durationVar(fs, timeout, "timeout", 30*time.Second, "how long to wait; exit 4 with the pending work when it passes")
			return func(ctx context.Context, r *runner, args []string) error {
				if err := wantArgs(args, 0, "no arguments"); err != nil {
					return err
				}
				return r.settle(ctx, *timeout)
			}
		})
}

func (r *runner) settle(ctx context.Context, timeout time.Duration) error {
	s, err := r.settleWithin(ctx, timeout)
	if err != nil {
		return err
	}
	return r.emit(s, func(w io.Writer) error {
		_, err := fmt.Fprintln(w, "settled: no tracked work is pending")
		return err
	})
}

// settleGrace is how long past a settle timeout the CLI waits for the
// server's answer, which comes by the timeout.
const settleGrace = 10 * time.Second

// settleWithin asks the server to settle within timeout. The server holds
// the call until then, longer than client allows, so it goes through
// slowClient, bounded by timeout and settleGrace.
func (r *runner) settleWithin(ctx context.Context, timeout time.Duration) (*admin.Settle, error) {
	c, err := r.slowClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+settleGrace)
	defer cancel()
	return c.Settle(ctx, timeout)
}

func resetCmd() *command {
	return leaf("reset", "Drop service state: all of it, or the services, accounts and regions named", "reset [--service S] [--account A] [--region R] [--clock] [--faults]",
		[]string{"understudy reset", "understudy reset --service sqs --service sns", "understudy reset --account 111111111111 --region eu-west-1", "understudy reset --clock --faults"},
		func(fs *flag.FlagSet) action {
			var req admin.ResetRequest
			fs.Var((*stringList)(&req.Services), "service", "service to reset, such as sqs, or outbox or lambda-async, understudy's own pending deliveries and asynchronous invocations rather than AWS services; repeat for more (default all)")
			fs.Var((*stringList)(&req.Accounts), "account", "account to reset; repeat for more (default all)")
			fs.Var((*stringList)(&req.Regions), "region", "region to reset; repeat for more (default all)")
			fs.BoolVar(&req.Clock, "clock", false, "also return the clock to how serve started it")
			fs.BoolVar(&req.Faults, "faults", false, "also remove every fault rule")
			return func(ctx context.Context, r *runner, args []string) error {
				if err := wantArgs(args, 0, "no arguments"); err != nil {
					return err
				}
				// A reset answers once the invocations in its scope have
				// drained, which a cold start or a long function can hold
				// past answerTimeout; an interrupt still ends the wait.
				c, err := r.slowClient()
				if err != nil {
					return err
				}
				res, err := c.Reset(ctx, req)
				if err != nil {
					return err
				}
				return r.emit(res, func(w io.Writer) error { return writeReset(w, req, res) })
			}
		})
}

// writeReset prints what a reset dropped: the services, then the mode the
// clock returned to and how many fault rules were removed when it was
// asked to reset them.
func writeReset(w io.Writer, req admin.ResetRequest, res *admin.ResetResult) error {
	text := "reset: " + orNone(res.Services)
	if req.Clock {
		text += "; clock returned to " + string(res.Clock)
	}
	if req.Faults {
		rules := "rules"
		if res.Faults == 1 {
			rules = "rule"
		}
		text += fmt.Sprintf("; %d fault %s removed", res.Faults, rules)
	}
	_, err := fmt.Fprintln(w, text)
	return err
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "nothing"
	}
	return strings.Join(names, ", ")
}

func clockCmd() *command {
	return group("clock", "Read or change the emulator clock",
		leaf("now", "Print emulator time and the clock's mode", "clock now", []string{"understudy clock now", "understudy clock now --output json"},
			func(*flag.FlagSet) action { return runClockNow }),
		clockChange("freeze", "Stop emulator time", "clock freeze", "", []string{"understudy clock freeze"},
			func([]string) (admin.ClockOp, error) { return admin.Freeze{}, nil }),
		clockChange("resume", "Run emulator time with wall time again", "clock resume", "", []string{"understudy clock resume"},
			func([]string) (admin.ClockOp, error) { return admin.Resume{}, nil }),
		clockChange("advance", "Move emulator time forward, firing the timers due on the way", "clock advance DURATION [--settle]", "DURATION",
			[]string{"understudy clock advance 15m", "understudy clock advance 90s --settle"}, parseAdvance),
		clockChange("set", "Move emulator time forward to an instant, firing the timers due on the way", "clock set RFC3339-TIME [--settle]", "TIME",
			[]string{"understudy clock set 2026-12-31T23:59:00Z"}, parseSet),
		clockChange("scale", "Run emulator time faster than wall time", "clock scale FACTOR", "FACTOR",
			[]string{"understudy clock scale 60"}, parseScale),
	)
}

func runClockNow(ctx context.Context, r *runner, args []string) error {
	if err := wantArgs(args, 0, "no arguments"); err != nil {
		return err
	}
	c, err := r.client()
	if err != nil {
		return err
	}
	s, err := c.Clock(ctx)
	if err != nil {
		return err
	}
	return r.emit(s, func(w io.Writer) error { return writeClock(w, *s) })
}

// clockChange builds a command that sends the op parse reads from its one
// positional argument, or from none when positional is empty. advance and
// set also take --settle.
func clockChange(name, summary, usage, positional string, examples []string, parse func(args []string) (admin.ClockOp, error)) *command {
	return leaf(name, summary, usage, examples, func(fs *flag.FlagSet) action {
		settle := new(false)
		settleTimeout := new(30 * time.Second)
		if name == "advance" || name == "set" {
			fs.BoolVar(settle, "settle", false, "then wait until the work the fired timers started is done")
			durationVar(fs, settleTimeout, "settle-timeout", 30*time.Second, "how long --settle waits")
		}
		want, n := "no arguments", 0
		if positional != "" {
			want, n = "one argument, "+positional, 1
		}
		return func(ctx context.Context, r *runner, args []string) error {
			if err := wantArgs(args, n, want); err != nil {
				return err
			}
			op, err := parse(args)
			if err != nil {
				return err
			}
			var settleFor *time.Duration
			if *settle {
				settleFor = settleTimeout
			}
			return r.changeClock(ctx, op, settleFor)
		}
	})
}

// changeClock sends op and, when settleFor is set, waits that long for
// the work the fired timers started.
func (r *runner) changeClock(ctx context.Context, op admin.ClockOp, settleFor *time.Duration) error {
	c, err := r.client()
	if err != nil {
		return err
	}
	res, err := c.ChangeClock(ctx, op)
	if err != nil {
		return err
	}
	out := clockChanged{ClockResult: res}
	if settleFor != nil {
		if _, err := r.settleWithin(ctx, *settleFor); err != nil {
			return errors.Join(r.emit(out, func(w io.Writer) error { return writeClockChange(w, out) }), err)
		}
		out.Settled = new(true)
	}
	return r.emit(out, func(w io.Writer) error { return writeClockChange(w, out) })
}

// clockChanged is a clock change's result and, with --settle, whether the
// work the fired timers started settled.
type clockChanged struct {
	*admin.ClockResult
	Settled *bool `json:"settled,omitempty"`
}

func parseAdvance(args []string) (admin.ClockOp, error) {
	d, err := time.ParseDuration(args[0])
	switch {
	case err == nil && d < 0:
		return nil, &usageError{msg: fmt.Sprintf("%q would move emulator time back, and it only moves forward; 'understudy reset --clock' returns it to how serve started it", args[0])}
	case err != nil || d == 0:
		return nil, &usageError{msg: fmt.Sprintf("%q is not a positive duration such as 90s or 15m", args[0])}
	}
	return admin.Advance{By: d}, nil
}

func parseSet(args []string) (admin.ClockOp, error) {
	t, err := time.Parse(time.RFC3339, args[0])
	if err != nil {
		return nil, &usageError{msg: fmt.Sprintf("%q is not an RFC 3339 time such as 2026-12-31T23:59:00Z", args[0])}
	}
	return admin.Set{To: t}, nil
}

func parseScale(args []string) (admin.ClockOp, error) {
	f, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		return nil, &usageError{msg: fmt.Sprintf("%q is not a number such as 60", args[0])}
	}
	return admin.Scale{Factor: f}, nil
}

func writeClockChange(w io.Writer, c clockChanged) error {
	if err := writeClock(w, c.Clock); err != nil {
		return err
	}
	var b strings.Builder
	if len(c.Fired) > 0 {
		fmt.Fprintf(&b, "fired %d timer(s):\n", len(c.Fired))
		for _, t := range c.Fired {
			fmt.Fprintf(&b, "  %s %s\n", t.Kind, t.ID)
		}
	}
	if c.Settled != nil {
		b.WriteString("settled: no tracked work is pending\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeClock(w io.Writer, s admin.ClockState) error {
	mode := string(s.Mode)
	if s.Mode == admin.Scaled {
		mode += " x" + strconv.FormatFloat(s.Factor, 'g', -1, 64)
	}
	_, err := fmt.Fprintf(w, "%s %s\n", s.Now.UTC().Format(time.RFC3339Nano), mode)
	return err
}
