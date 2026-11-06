package cli

import (
	"context"
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
			"ses messages [--to ADDR] [--from ADDR] [--message-id ID] [--account A] [--region R] [--since TIME] [--limit N]",
			[]string{
				"understudy ses messages",
				"understudy ses messages --to success@simulator.amazonses.com --output json",
				"understudy ses messages --from sender@example.com --since 2026-01-01T00:00:00Z",
			},
			sesMessages),
	)
}

func sesMessages(fs *flag.FlagSet) action {
	var q admin.SESMessageQuery
	var limit int
	fs.StringVar(&q.To, "to", "", "messages to this address, as a To, Cc, Bcc or envelope recipient")
	fs.StringVar(&q.From, "from", "", "messages from this address")
	fs.StringVar(&q.MessageID, "message-id", "", "the message with this MessageId, as SendEmail returned it")
	fs.StringVar(&q.Account, "account", "", "messages of this account (default all)")
	fs.StringVar(&q.Region, "region", "", "messages of this region (default all)")
	timeVar(fs, &q.Since, "since", "messages accepted at or after this emulator time")
	intVar(fs, &limit, "limit", 100, "most messages to print, the oldest that match")
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

func writeSESMessages(w io.Writer, messages []admin.SESMessage) error {
	if len(messages) == 0 {
		_, err := fmt.Fprintln(w, "no messages")
		return err
	}
	tw := newTable(w)
	fmt.Fprintln(tw, "ACCEPTED\tKIND\tOPERATION\tMESSAGE ID\tFROM\tTO\tSUBJECT")
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
