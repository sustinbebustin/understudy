package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"iter"
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
	eventIDVar(fs, &f.q.Trace, "trace", "events of the causal tree with this root event id")
	fs.StringVar(&f.q.RequestID, "request-id", "", "the API call with this request id, as an SDK error or log line shows it")
	fs.StringVar(&f.q.Service, "service", "", "events of a service, such as sqs")
	fs.StringVar(&f.q.Operation, "operation", "", "events of an operation as service.Operation, such as sqs.SendMessage")
	fs.StringVar(&f.q.Resource, "resource", "", "events touching an ARN glob; * matches any run of characters")
	fs.StringVar(&f.q.Kind, "kind", "", "events of a kind, such as api-call, delivery, invocation or fault-injected")
	fs.StringVar(&f.q.Principal, "principal", "", "events of a principal ARN")
	fs.BoolVar(&f.errorsOnly, "errors", false, "only events that failed")
	durationVar(fs, &f.since, "since", 0, "only events from this long ago in emulator time, such as 10m")
	intVar(fs, &f.limit, "limit", 1000, "most events a query prints, the newest ones; --follow prints them all")
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
		return r.printNewest(ctx, c, f.q, f.limit)
	}
}

// journalPageSize is the page size the CLI reads the journal in.
const journalPageSize = 500

// eachEvent calls f with every event q selects, oldest first, page by page.
func eachEvent(ctx context.Context, c *admin.Client, q admin.JournalQuery, f func(*admin.JournalEvent) error) error {
	q.Limit = journalPageSize
	for {
		page, err := c.JournalEvents(ctx, q)
		if err != nil {
			return err
		}
		for i := range page.Events {
			if err := f(&page.Events[i]); err != nil {
				return err
			}
		}
		if page.Next == 0 {
			return nil
		}
		q.Cursor = page.Next
	}
}

// printNewest prints the newest limit events q selects, oldest first: the
// end of a long session is what a query is usually after. In text, a
// notice on stderr says when events were left out or none matched.
func (r *runner) printNewest(ctx context.Context, c *admin.Client, q admin.JournalQuery, limit int) error {
	// newest is a ring of the last limit events seen; matched counts them
	// all, so matched % limit is the oldest kept once it is full.
	var newest []admin.JournalEvent
	matched := 0
	err := eachEvent(ctx, c, q, func(e *admin.JournalEvent) error {
		if len(newest) < limit {
			newest = append(newest, *e)
		} else {
			newest[matched%limit] = *e
		}
		matched++
		return nil
	})
	if err != nil {
		return err
	}
	start := 0
	if matched > limit {
		start = matched % limit
	}
	for i := range newest {
		if err := r.writeEvent(&newest[(start+i)%len(newest)]); err != nil {
			return err
		}
	}
	if r.output == outputJSON {
		return nil
	}
	switch {
	case matched == 0:
		_, err = fmt.Fprintln(r.o.Stderr, "no events matched")
	case matched > limit:
		_, err = fmt.Fprintf(r.o.Stderr, "showing the newest %d of %d matching events; raise --limit, narrow with --since, --service or --errors, or use --follow\n", limit, matched)
	}
	return err
}

// printAll prints every event q selects and returns the ids printed.
func (r *runner) printAll(ctx context.Context, c *admin.Client, q admin.JournalQuery) (map[uint64]bool, error) {
	printed := map[uint64]bool{}
	err := eachEvent(ctx, c, q, func(e *admin.JournalEvent) error {
		printed[e.ID] = true
		return r.writeEvent(e)
	})
	return printed, err
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
	// The stream's headers come once the server subscribes, so the
	// answer bound holds; the body that follows is not bounded.
	sc, err := r.clientWith(&http.Client{Transport: &onResponse{base: newAnswerBound(r.answer), done: s.subscribed}})
	if err != nil {
		return err
	}
	go s.read(ctx, sc.JournalStream(ctx, q, 0))
	if !s.ready() {
		return followEnd(ctx, s.err)
	}
	printed, err := r.printAll(ctx, c, q)
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
	text, requestID := summary(e)
	if requestID != "" {
		text += " request-id " + requestID
	}
	return fmt.Sprintf("%-6d %s  %-14s %-22s %s", e.ID, e.At.UTC().Format(eventTime), e.Kind, scope, text)
}

// eventTime is RFC 3339 with every nanosecond digit kept, so the columns
// after it line up; RFC3339Nano drops trailing zeros.
const eventTime = "2006-01-02T15:04:05.000000000Z07:00"

// summary is an event's detail in brief, one line per kind: an API call
// names its operation and outcome, and when it failed the error; a
// delivery its path, ends, attempt and outcome; an invocation its function
// and outcome; a fault hit its rule and the call it hit. A detail the CLI
// cannot read, such as a kind newer than it, is printed compact and cut,
// pointing at --output json. requestID is the id of a call that failed,
// which 'journal trace --request-id' takes.
func summary(e *admin.JournalEvent) (text, requestID string) {
	var ok bool
	switch e.Kind {
	case "api-call":
		text, requestID, ok = callSummary(e.Detail)
	case "fault-injected":
		text, ok = faultHitSummary(e)
	case "delivery":
		text, ok = deliverySummary(e.Detail)
	case "invocation":
		text, ok = invocationSummary(e.Detail)
	case "timer-fired":
		text, ok = timerSummary(e.Detail)
	case "state-change":
		text, ok = stateChangeSummary(e.Detail)
	case "clock-changed":
		text, ok = clockChangeSummary(e.Detail)
	case "advisory":
		text, ok = advisorySummary(e.Detail)
	case "unclaimed":
		text, ok = unclaimedSummary(e.Detail)
	case "gap":
		text, ok = gapSummary(e.Detail)
	}
	if ok {
		return text, requestID
	}
	text = string(e.Detail)
	if len(text) > 160 {
		text = text[:157] + "... (--output json has the full detail)"
	}
	return text, ""
}

// decodeDetail reads an event's detail into a kind's view of it; false
// when it does not decode.
func decodeDetail[T any](detail json.RawMessage) (T, bool) {
	var d T
	return d, json.Unmarshal(detail, &d) == nil
}

// deliverySummary is a delivery attempt in brief: "sns-to-sqs t1 -> q1
// attempt 1 delivered", with the next attempt's time, the dead-letter
// queue or the reason it was dropped.
func deliverySummary(detail json.RawMessage) (string, bool) {
	d, ok := decodeDetail[struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Mechanism string `json:"mechanism"`
		Attempt   int    `json:"attempt"`
		Outcome   struct {
			Kind   string     `json:"kind"`
			Rule   string     `json:"rule"`
			Next   *time.Time `json:"next"`
			DLQ    string     `json:"dlq"`
			Reason string     `json:"reason"`
		} `json:"outcome"`
	}](detail)
	if !ok || d.Mechanism == "" || d.Outcome.Kind == "" {
		return "", false
	}
	text := fmt.Sprintf("%s %s -> %s attempt %d %s", d.Mechanism, shortARN(d.From), shortARN(d.To), d.Attempt, d.Outcome.Kind)
	switch {
	case d.Outcome.Rule != "":
		text += " by " + d.Outcome.Rule
	case d.Outcome.Next != nil:
		text += ", next attempt at " + d.Outcome.Next.UTC().Format(time.RFC3339Nano)
	case d.Outcome.DLQ != "":
		text += " to " + shortARN(d.Outcome.DLQ)
	case d.Outcome.Reason != "":
		text += ": " + d.Outcome.Reason
	}
	return text, true
}

// invocationSummary is a function invocation in brief: "worker ok cold
// 120ms", with a function error's type or an exit's code.
func invocationSummary(detail json.RawMessage) (string, bool) {
	d, ok := decodeDetail[struct {
		Function string        `json:"function"`
		Version  string        `json:"version"`
		Cold     bool          `json:"cold"`
		Duration time.Duration `json:"duration"`
		Outcome  struct {
			Kind string `json:"kind"`
			Type string `json:"type"`
			Code int    `json:"code"`
		} `json:"outcome"`
	}](detail)
	if !ok || d.Function == "" || d.Outcome.Kind == "" {
		return "", false
	}
	parts := []string{shortARN(d.Function)}
	if d.Version != "" && d.Version != "$LATEST" {
		parts[0] += ":" + d.Version
	}
	parts = append(parts, d.Outcome.Kind)
	switch {
	case d.Outcome.Type != "":
		parts = append(parts, d.Outcome.Type)
	case d.Outcome.Kind == "exit-error":
		parts = append(parts, "code "+strconv.Itoa(d.Outcome.Code))
	}
	if d.Cold {
		parts = append(parts, "cold")
	}
	if d.Duration > 0 {
		parts = append(parts, d.Duration.String())
	}
	return strings.Join(parts, " "), true
}

// timerSummary is a fired timer: its kind and id.
func timerSummary(detail json.RawMessage) (string, bool) {
	d, ok := decodeDetail[struct {
		Kind string `json:"timerKind"`
		ID   string `json:"timerId"`
	}](detail)
	return d.Kind + " " + d.ID, ok && d.Kind != ""
}

// stateChangeSummary is a resource's move between states.
func stateChangeSummary(detail json.RawMessage) (string, bool) {
	d, ok := decodeDetail[struct {
		Resource string `json:"resource"`
		From     string `json:"from"`
		To       string `json:"to"`
	}](detail)
	return fmt.Sprintf("%s %s -> %s", shortARN(d.Resource), d.From, d.To), ok && d.To != ""
}

// clockMode is a clock mode as the journal writes it.
type clockMode struct {
	Kind   string     `json:"kind"`
	At     *time.Time `json:"at"`
	Factor float64    `json:"factor"`
}

func (m clockMode) String() string {
	switch {
	case m.At != nil:
		return m.Kind + " at " + m.At.UTC().Format(time.RFC3339Nano)
	case m.Factor != 0:
		return m.Kind + " x" + strconv.FormatFloat(m.Factor, 'g', -1, 64)
	}
	return m.Kind
}

// clockChangeSummary is the clock's move from one mode to another.
func clockChangeSummary(detail json.RawMessage) (string, bool) {
	d, ok := decodeDetail[struct {
		From clockMode `json:"from"`
		To   clockMode `json:"to"`
	}](detail)
	return d.From.String() + " -> " + d.To.String(), ok && d.To.Kind != ""
}

// advisorySummary is an advisory's name and its text, in full.
func advisorySummary(detail json.RawMessage) (string, bool) {
	d, ok := decodeDetail[struct {
		Advisory string `json:"advisory"`
		Text     string `json:"text"`
	}](detail)
	return d.Advisory + ": " + d.Text, ok && d.Advisory != ""
}

// unclaimedSummary is a request no route claimed, with why each declined.
func unclaimedSummary(detail json.RawMessage) (string, bool) {
	d, ok := decodeDetail[struct {
		Method  string   `json:"method"`
		Host    string   `json:"host"`
		Path    string   `json:"path"`
		Reasons []string `json:"reasons"`
	}](detail)
	text := d.Method + " " + d.Host + d.Path
	if len(d.Reasons) > 0 {
		text += ": " + strings.Join(d.Reasons, "; ")
	}
	return text, ok && d.Method != ""
}

// gapSummary is a stream's notice of the events it missed.
func gapSummary(detail json.RawMessage) (string, bool) {
	d, ok := decodeDetail[struct {
		Missed uint64 `json:"missed"`
	}](detail)
	return fmt.Sprintf("missed %d event(s)", d.Missed), ok
}

// shortARN is an ARN's resource without its type: q1 for
// arn:aws:sqs:us-east-1:000000000000:q1, worker for
// arn:aws:lambda:us-east-1:000000000000:function:worker. Text that is no
// ARN is returned as is.
func shortARN(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" {
		return arn
	}
	resource := parts[5]
	if kind, name, found := strings.Cut(resource, ":"); found && !strings.Contains(kind, "/") {
		return name
	}
	return resource
}

// faultHitSummary is a fault-injected event in brief: the rule, which hit
// of it, and the event of the call it hit; ok is false when the detail is
// not a fault hit.
func faultHitSummary(e *admin.JournalEvent) (text string, ok bool) {
	var d struct {
		Rule string `json:"rule"`
		Hit  int    `json:"hit"`
		Of   int    `json:"of"`
	}
	if json.Unmarshal(e.Detail, &d) != nil || d.Rule == "" {
		return "", false
	}
	text = fmt.Sprintf("rule %s hit %d", d.Rule, d.Hit)
	if d.Of > 0 {
		text += fmt.Sprintf(" of %d", d.Of)
	}
	if e.Cause.Parent != 0 {
		text += fmt.Sprintf(", on event %d", e.Cause.Parent)
	}
	return text, true
}

// callSummary is an API call's detail in brief: its operation and outcome,
// with the error and the rule that injected it; requestID is set when the
// call failed. A call the service could not name, such as one its front
// end claimed as an unknown operation, has an empty operation. ok is false
// when detail does not decode as an API call.
func callSummary(detail json.RawMessage) (text, requestID string, ok bool) {
	var d struct {
		Service   string `json:"service"`
		Operation string `json:"operation"`
		RequestID string `json:"requestId"`
		Outcome   struct {
			Kind     string `json:"kind"`
			Identity struct {
				Shape string `json:"shape"`
			} `json:"identity"`
			Status int    `json:"status"`
			Rule   string `json:"rule"`
		} `json:"outcome"`
	}
	if json.Unmarshal(detail, &d) != nil || d.Service == "" {
		return "", "", false
	}
	operation := d.Operation
	if operation == "" {
		operation = "<unknown operation>"
	}
	parts := []string{d.Service + "." + operation}
	if d.Outcome.Kind != "" {
		parts = append(parts, d.Outcome.Kind)
	}
	if d.Outcome.Identity.Shape != "" {
		parts = append(parts, shortShape(d.Outcome.Identity.Shape))
	}
	if d.Outcome.Status != 0 {
		parts = append(parts, strconv.Itoa(d.Outcome.Status))
	}
	if d.Outcome.Rule != "" {
		parts = append(parts, "by rule "+d.Outcome.Rule)
	}
	if d.Outcome.Kind == "failed" || d.Outcome.Kind == "injected" {
		requestID = d.RequestID
	}
	return strings.Join(parts, " "), requestID, true
}

// shortShape is an error shape id without its namespace, as AWS's error
// codes and faults add --error spell it: QueueDoesNotExist for
// com.amazonaws.sqs#QueueDoesNotExist.
func shortShape(id string) string {
	_, name, found := strings.Cut(id, "#")
	if !found {
		return id
	}
	return name
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
	// The request id stays out: two runs of one workload differ in it
	// unless seeded, and the canonical tree is for comparing runs.
	text, _ := summary(&t.Event)
	line := fmt.Sprintf("%s%s %s %s", strings.Repeat("  ", depth), t.Position, t.Event.Kind, text)
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
