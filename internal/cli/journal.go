package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"iter"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

func journalCmd() *command {
	c := leaf("journal", "Query or follow the event journal: API calls, deliveries, invocations, timers and faults",
		"journal [--trace ID] [--request-id ID] [--service S] [--operation O] [--resource ARN] [--kind K] [--principal P] [--errors] [--since 10m] [--limit N] [--follow]",
		[]string{
			"understudy journal --service sqs --since 10m",
			"understudy journal --request-id 6f1c2e8a-... --output json",
			"understudy journal --errors --follow",
			"understudy journal trace 42",
		},
		journalQuery)
	c.sub = []*command{traceCmd()}
	return c
}

// journalFlags are the filters of journal.
type journalFlags struct {
	q          admin.JournalQuery
	errorsOnly bool
	since      time.Duration
	limit      int
	follow     bool
}

func journalQuery(fs *flag.FlagSet) action {
	var f journalFlags
	fs.Uint64Var(&f.q.Trace, "trace", 0, "events of the causal tree with this root event id")
	fs.StringVar(&f.q.RequestID, "request-id", "", "the API call with this request id, as an SDK error or log line shows it")
	fs.StringVar(&f.q.Service, "service", "", "events of a service, such as sqs")
	fs.StringVar(&f.q.Operation, "operation", "", "events of an operation as service.Operation, such as sqs.SendMessage")
	fs.StringVar(&f.q.Resource, "resource", "", "events touching an ARN glob; * matches any run of characters")
	fs.StringVar(&f.q.Kind, "kind", "", "events of a kind, such as api-call, delivery, invocation or fault-injected")
	fs.StringVar(&f.q.Principal, "principal", "", "events of a principal ARN")
	fs.BoolVar(&f.errorsOnly, "errors", false, "only events that failed")
	fs.DurationVar(&f.since, "since", 0, "only events from this long ago in emulator time, such as 10m")
	fs.IntVar(&f.limit, "limit", 1000, "most events a query prints; --follow prints them all")
	fs.BoolVar(&f.follow, "follow", false, "after the matching events, print new ones as they are recorded until interrupted")
	return func(ctx context.Context, r *runner, args []string) error {
		if err := wantArgs(args, 0, "no arguments, or trace ID"); err != nil {
			return err
		}
		if f.limit <= 0 {
			return &usageError{msg: "--limit must be positive"}
		}
		c, err := r.client()
		if err != nil {
			return err
		}
		if f.errorsOnly {
			f.q.Outcome = "error"
		}
		if f.since > 0 {
			now, err := c.Clock(ctx)
			if err != nil {
				return err
			}
			f.q.From = now.Now.Add(-f.since)
		}
		if f.follow {
			return r.follow(ctx, c, f.q)
		}
		_, err = r.printEvents(ctx, c, f.q, f.limit)
		return err
	}
}

// printEvents prints up to limit events q selects, page by page, and
// returns the ids printed.
func (r *runner) printEvents(ctx context.Context, c *admin.Client, q admin.JournalQuery, limit int) (map[uint64]bool, error) {
	printed := map[uint64]bool{}
	for {
		q.Limit = min(limit-len(printed), 500)
		page, err := c.JournalEvents(ctx, q)
		if err != nil {
			return printed, err
		}
		for i := range page.Events {
			if err := r.writeEvent(&page.Events[i]); err != nil {
				return printed, err
			}
			printed[page.Events[i].ID] = true
		}
		if page.Next == 0 || len(printed) >= limit {
			return printed, nil
		}
		q.Cursor = page.Next
	}
}

// follow subscribes to the events q selects, then prints every one already
// recorded, with no --limit, then the stream's, skipping those the query
// printed, until ctx ends, which is a normal end. Subscribing first loses
// nothing recorded between the query and the subscription.
func (r *runner) follow(ctx context.Context, c *admin.Client, q admin.JournalQuery) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The buffer holds what the stream delivers while the query prints;
	// beyond it the server's own buffer reports a gap rather than block.
	s := &subscription{subscribed: make(chan struct{}), events: make(chan admin.JournalEvent, 1024)}
	sc, err := r.clientWith(&http.Client{Transport: &onResponse{base: http.DefaultTransport, done: s.subscribed}})
	if err != nil {
		return err
	}
	go s.read(ctx, sc.JournalStream(ctx, q, 0))
	if !s.ready() {
		return followEnd(ctx, s.err)
	}
	printed, err := r.printEvents(ctx, c, q, math.MaxInt)
	if err != nil {
		return err
	}
	err = s.each(func(e *admin.JournalEvent) error {
		if printed[e.ID] {
			return nil
		}
		return r.writeEvent(e)
	})
	if err != nil {
		return err
	}
	return followEnd(ctx, s.err)
}

// subscription is a journal stream read in the background.
type subscription struct {
	// subscribed closes once the server has subscribed the stream.
	subscribed chan struct{}
	// events closes when the stream ends, after err is set.
	events chan admin.JournalEvent
	err    error
	// early are events ready saw before it saw subscribed.
	early []admin.JournalEvent
}

func (s *subscription) read(ctx context.Context, stream iter.Seq2[admin.JournalEvent, error]) {
	defer close(s.events)
	for e, err := range stream {
		if err != nil {
			s.err = err
			return
		}
		select {
		case s.events <- e:
		case <-ctx.Done():
			return
		}
	}
}

// ready waits until the server has subscribed, and reports false when the
// stream ended first.
func (s *subscription) ready() bool {
	select {
	case <-s.subscribed:
		return true
	case e, open := <-s.events:
		// An event arrives only once subscribed, but select may pick it.
		if open {
			s.early = append(s.early, e)
		}
		return open
	}
}

// each calls f with every event until the stream ends or f fails.
func (s *subscription) each(f func(*admin.JournalEvent) error) error {
	for i := range s.early {
		if err := f(&s.early[i]); err != nil {
			return err
		}
	}
	for e := range s.events {
		if err := f(&e); err != nil {
			return err
		}
	}
	return nil
}

// followEnd is how a follow ends: nil when ctx ended or the server closed
// the stream, else the stream's error.
func followEnd(ctx context.Context, err error) error {
	select {
	case <-ctx.Done():
		return nil
	default:
		return err
	}
}

// onResponse closes done once a request through it has its response
// headers: for the journal stream, the server has subscribed by then.
type onResponse struct {
	base http.RoundTripper
	done chan struct{}
	once sync.Once
}

func (t *onResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		t.once.Do(func() { close(t.done) })
	}
	return resp, err
}

// writeEvent prints one event: an NDJSON line, or a text line.
func (r *runner) writeEvent(e *admin.JournalEvent) error {
	if r.output == outputJSON {
		data, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("encoding event %d: %w", e.ID, err)
		}
		_, err = fmt.Fprintf(r.o.Stdout, "%s\n", data)
		return err
	}
	_, err := fmt.Fprintln(r.o.Stdout, eventLine(e))
	return err
}

// eventLine is an event as one text line: id, emulator time, kind, scope
// and a summary of its detail.
func eventLine(e *admin.JournalEvent) string {
	scope := e.Scope.Account + "/" + e.Scope.Region
	if e.Scope.Account == "" {
		scope = "-"
	}
	return fmt.Sprintf("%-6d %s  %-14s %-22s %s", e.ID, e.At.UTC().Format(time.RFC3339Nano), e.Kind, scope, summary(e.Detail))
}

// summary names the operation and outcome of a detail that has them, and
// is the compact detail otherwise.
func summary(detail json.RawMessage) string {
	var d struct {
		Service   string `json:"service"`
		Operation string `json:"operation"`
		Outcome   struct {
			Kind string `json:"kind"`
		} `json:"outcome"`
	}
	if json.Unmarshal(detail, &d) == nil && d.Operation != "" {
		s := d.Service + "." + d.Operation
		if d.Outcome.Kind != "" {
			s += " " + d.Outcome.Kind
		}
		return s
	}
	text := string(detail)
	if len(text) > 160 {
		text = text[:157] + "..."
	}
	return text
}

func traceCmd() *command {
	return leaf("trace", "Print the causal tree of an event or of the API call with a request id", "journal trace (ID | --request-id ID) [--canonical]",
		[]string{"understudy journal trace 42", "understudy journal trace --request-id 6f1c2e8a-...", "understudy journal trace 42 --canonical --output json"},
		func(fs *flag.FlagSet) action {
			requestID := fs.String("request-id", "", "find the tree by the request id of an API call in it")
			canonical := fs.Bool("canonical", false, "print the normalized tree two runs of one workload agree on")
			return func(ctx context.Context, r *runner, args []string) error {
				key, err := traceKey(args, *requestID)
				if err != nil {
					return err
				}
				c, err := r.client()
				if err != nil {
					return err
				}
				if *canonical {
					t, err := c.CanonicalTrace(ctx, key)
					if err != nil {
						return err
					}
					return r.emit(t, func(w io.Writer) error { return writeCanonical(w, t, 0) })
				}
				t, err := c.Trace(ctx, key)
				if err != nil {
					return err
				}
				return r.emit(t, func(w io.Writer) error { return writeTrace(w, t, 0) })
			}
		})
}

func traceKey(args []string, requestID string) (admin.TraceKey, error) {
	switch {
	case len(args) == 1 && requestID == "":
		id, err := strconv.ParseUint(args[0], 10, 64)
		if err != nil || id == 0 {
			return nil, &usageError{msg: fmt.Sprintf("%q is not an event id; pass a positive integer or --request-id", args[0])}
		}
		return admin.ByEvent{ID: id}, nil
	case len(args) == 0 && requestID != "":
		return admin.ByRequestID{RequestID: requestID}, nil
	default:
		return nil, &usageError{msg: "takes an event id or --request-id ID, not both and not neither"}
	}
}

func writeTrace(w io.Writer, t *admin.Trace, depth int) error {
	if _, err := fmt.Fprintln(w, strings.Repeat("  ", depth)+eventLine(&t.Event)); err != nil {
		return err
	}
	for i := range t.Children {
		if err := writeTrace(w, &t.Children[i], depth+1); err != nil {
			return err
		}
	}
	return nil
}

func writeCanonical(w io.Writer, t *admin.CanonicalTrace, depth int) error {
	line := fmt.Sprintf("%s%s %s %s", strings.Repeat("  ", depth), t.Position, t.Event.Kind, summary(t.Event.Detail))
	if len(t.Links) > 0 {
		line += " links " + strings.Join(t.Links, ",")
	}
	if _, err := fmt.Fprintln(w, line); err != nil {
		return err
	}
	for i := range t.Children {
		if err := writeCanonical(w, &t.Children[i], depth+1); err != nil {
			return err
		}
	}
	return nil
}
