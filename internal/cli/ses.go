package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

// sesPageMax is the largest page GET /ses/messages serves (ADR 0017
// amendment for the SES message endpoints).
const sesPageMax = 1000

func sesCmd() *command {
	return group("ses", "Read the mail SES recorded instead of sending it",
		leaf("messages", "List recorded SES messages, oldest first: sends and verification emails",
			"ses messages [--to ADDR] [--from ADDR] [--message-id ID] [--account A] [--region R] [--since TIME] [--limit N] [--follow]",
			[]string{
				"understudy ses messages",
				"understudy ses messages --to success@simulator.amazonses.com --follow",
				"understudy ses messages --to success@simulator.amazonses.com --output json",
				"understudy ses messages --from sender@example.com --since 2026-01-01T00:00:00Z",
			},
			sesMessages),
	)
}

func sesMessages(fs *flag.FlagSet) action {
	var q admin.SESMessageQuery
	var limit int
	var follow bool
	fs.StringVar(&q.To, "to", "", "messages to this address, as a To, Cc, Bcc or envelope recipient")
	fs.StringVar(&q.From, "from", "", "messages from this address")
	fs.StringVar(&q.MessageID, "message-id", "", "the message with this MessageId, as SendEmail returned it")
	fs.StringVar(&q.Account, "account", "", "messages of this account (default all)")
	fs.StringVar(&q.Region, "region", "", "messages of this region (default all)")
	timeVar(fs, &q.Since, "since", "messages accepted at or after this emulator time")
	intVar(fs, &limit, "limit", 100, "most messages to print, the oldest that match; --follow prints them all")
	fs.BoolVar(&follow, "follow", false, "after the matching messages, print new ones as they are recorded until interrupted")
	return func(ctx context.Context, r *runner, args []string) error {
		if err := wantArgs(args, 0, "no arguments"); err != nil {
			return err
		}
		if limit <= 0 {
			return &usageError{msg: "--limit must be positive"}
		}
		c, err := r.client()
		if err != nil {
			return err
		}
		if follow {
			return r.followSESMessages(ctx, c, q)
		}
		page, more, err := collectSESMessages(ctx, c, q, limit)
		if err != nil {
			return err
		}
		if err := r.emit(page, func(w io.Writer) error { return writeSESMessages(w, page.Messages) }); err != nil || r.output == outputJSON {
			return err
		}
		var notes strings.Builder
		if more {
			fmt.Fprintf(&notes, "showing the oldest %d matching messages; raise --limit, or narrow with --since, --to or --from\n", limit)
		}
		if page.Evicted > 0 {
			fmt.Fprintf(&notes, "the store evicted %d older message(s) in the accounts and regions queried; they cannot be read\n", page.Evicted)
		}
		_, err = io.WriteString(r.o.Stderr, notes.String())
		return err
	}
}

// collectSESMessages reads pages of the messages q selects until limit
// are read or none are left. more reports that messages beyond limit
// match; the page's Next is the cursor to read them from.
func collectSESMessages(ctx context.Context, c *admin.Client, q admin.SESMessageQuery, limit int) (page admin.SESMessagePage, more bool, err error) {
	page.Messages = []admin.SESMessage{}
	for {
		q.Limit = min(limit-len(page.Messages), sesPageMax)
		p, err := c.SESMessages(ctx, q)
		if err != nil {
			return page, false, err
		}
		page.Messages = append(page.Messages, p.Messages...)
		page.Next, page.Evicted = p.Next, p.Evicted
		if p.Next == "" {
			return page, false, nil
		}
		if len(page.Messages) >= limit {
			return page, true, nil
		}
		q.Cursor = p.Next
	}
}

// sesMessageKey identifies a recorded message among those followed.
type sesMessageKey struct{ account, region, messageID string }

func sesKeyOf(m *admin.SESMessage) sesMessageKey {
	return sesMessageKey{m.Account, m.Region, m.MessageID}
}

// sesFollowed is what a follow has printed: since, the newest acceptance
// time seen, which the next listing starts from, and the messages accepted
// at or after it already printed, pruned to those accepted at since itself
// as it advances.
type sesFollowed struct {
	since   time.Time
	printed map[sesMessageKey]time.Time
}

func newSESFollowed(since time.Time) *sesFollowed {
	return &sesFollowed{since: since, printed: map[sesMessageKey]time.Time{}}
}

// seen reports whether m was already printed.
func (f *sesFollowed) seen(m *admin.SESMessage) bool {
	_, ok := f.printed[sesKeyOf(m)]
	return ok
}

// record marks messages printed, advances since to the newest acceptance
// time among them and prunes what since has passed.
func (f *sesFollowed) record(messages []admin.SESMessage) {
	for i := range messages {
		m := &messages[i]
		f.printed[sesKeyOf(m)] = m.Accepted
		if m.Accepted.After(f.since) {
			f.since = m.Accepted
		}
	}
	for k, accepted := range f.printed {
		if accepted.Before(f.since) {
			delete(f.printed, k)
		}
	}
}

// followSESMessages prints every message q selects, with no --limit, then
// each one recorded later until ctx ends, which is a normal end. The
// journal stream only wakes it: a message is read through the same listing
// as the first ones, from the newest acceptance time seen, skipping those
// already printed. Subscribing before the first listing loses nothing
// recorded between the two.
func (r *runner) followSESMessages(ctx context.Context, c *admin.Client, q admin.SESMessageQuery) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Every SES call, v1 or v2, is an api-call event; a message is
	// recorded as its call is accepted.
	s := subscribe(ctx, c, admin.JournalQuery{Kind: "api-call"})
	if !s.ready() {
		return followEnd(ctx, s.err)
	}
	followed := newSESFollowed(q.Since)
	rows := &sesRowWriter{w: r.o.Stdout}
	drain := func() error {
		q.Since = followed.since
		var fresh []admin.SESMessage
		err := eachSESMessage(ctx, c, q, func(m *admin.SESMessage) {
			if !followed.seen(m) {
				fresh = append(fresh, *m)
			}
		})
		if err != nil {
			return err
		}
		followed.record(fresh)
		return r.writeFollowedSES(fresh, rows)
	}
	// A read cut short by cancellation is the normal end, not a failure.
	if err := drain(); err != nil {
		return followEnd(ctx, err)
	}
	err := s.each(func(e *admin.JournalEvent) error {
		if !wakesSESFollow(e) {
			return nil
		}
		return drain()
	})
	if err != nil {
		return followEnd(ctx, err)
	}
	return followEnd(ctx, s.err)
}

// wakesSESFollow reports whether e may have recorded a message: an SES
// call, or a gap, which says events were missed.
func wakesSESFollow(e *admin.JournalEvent) bool {
	if e.Kind == "gap" {
		return true
	}
	d, ok := decodeDetail[struct {
		Service string `json:"service"`
	}](e.Detail)
	return ok && (d.Service == "ses" || d.Service == "sesv2")
}

// eachSESMessage calls f with every message q selects, oldest first, page
// by page.
func eachSESMessage(ctx context.Context, c *admin.Client, q admin.SESMessageQuery, f func(*admin.SESMessage)) error {
	q.Limit = sesPageMax
	for {
		page, err := c.SESMessages(ctx, q)
		if err != nil {
			return err
		}
		for i := range page.Messages {
			f(&page.Messages[i])
		}
		if page.Next == "" {
			return nil
		}
		q.Cursor = page.Next
	}
}

// writeFollowedSES prints messages as NDJSON lines, or through rows.
func (r *runner) writeFollowedSES(messages []admin.SESMessage, rows *sesRowWriter) error {
	if r.output != outputJSON {
		return rows.write(messages)
	}
	for i := range messages {
		data, err := json.Marshal(&messages[i])
		if err != nil {
			return fmt.Errorf("encoding message %s: %w", messages[i].MessageID, err)
		}
		if _, err := fmt.Fprintf(r.o.Stdout, "%s\n", data); err != nil {
			return err
		}
	}
	return nil
}

func writeSESMessages(w io.Writer, messages []admin.SESMessage) error {
	if len(messages) == 0 {
		_, err := fmt.Fprintln(w, "no messages")
		return err
	}
	return (&sesRowWriter{w: w}).write(messages)
}

// sesRowWriter prints messages as table rows to w, led by one header,
// which is printed with the first row.
type sesRowWriter struct {
	w      io.Writer
	headed bool
}

func (t *sesRowWriter) write(messages []admin.SESMessage) error {
	if len(messages) == 0 {
		return nil
	}
	tw := newTable(t.w)
	if !t.headed {
		fmt.Fprintln(tw, "ACCEPTED\tKIND\tOPERATION\tMESSAGE ID\tFROM\tTO\tSUBJECT")
		t.headed = true
	}
	for i := range messages {
		m := &messages[i]
		subject := "-"
		if m.Subject != nil {
			subject = m.Subject.Data
		}
		fmt.Fprintf(tw, "%s\t%s\t%s.%s\t%s\t%s\t%s\t%s\n", m.Accepted.UTC().Format(time.RFC3339Nano), m.Kind, m.API, m.Operation, m.MessageID,
			orDash(strings.Join(m.From, ",")), orDash(strings.Join(m.Envelope, ",")), subject)
	}
	return tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
