package filterpolicy_test

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/service/sns/filterpolicy"
)

// r09Dir is the sns/R09 recording (2026-10-14), relative to this package.
var r09Dir = filepath.Join("..", "..", "..", "..", "testdata", "parity", "sns", "R09")

// r09Subscribe is what a sns/R09 Subscribe capture records: the filter
// policy and scope it sent, and whether AWS accepted it (exit 0).
type r09Subscribe struct {
	policy   string
	scope    filterpolicy.Scope
	accepted bool
}

func readR09Subscribe(t *testing.T, step string) r09Subscribe {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r09Dir, step+".json"))
	if err != nil {
		t.Fatalf("read sns/R09/%s.json: %v", step, err)
	}
	var capture struct {
		Exit      int `json:"exit"`
		Exchanges []struct {
			Request struct {
				Body string `json:"body"`
			} `json:"request"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil || len(capture.Exchanges) == 0 {
		t.Fatalf("sns/R09/%s.json has no exchange (%v)", step, err)
	}
	form, err := url.ParseQuery(capture.Exchanges[0].Request.Body)
	if err != nil {
		t.Fatalf("sns/R09/%s.json request body: %v", step, err)
	}
	attrs := map[string]string{}
	for i := 1; form.Has("Attributes.entry." + strconv.Itoa(i) + ".key"); i++ {
		n := "Attributes.entry." + strconv.Itoa(i)
		attrs[form.Get(n+".key")] = form.Get(n + ".value")
	}
	policy, ok := attrs["FilterPolicy"]
	if !ok {
		t.Fatalf("sns/R09/%s.json sent no FilterPolicy", step)
	}
	s := r09Subscribe{policy: policy, scope: filterpolicy.MessageAttributes, accepted: capture.Exit == 0}
	if attrs["FilterPolicyScope"] == "MessageBody" {
		s.scope = filterpolicy.MessageBody
	}
	return s
}

// TestRecordedSubscribes compiles the policy of each sns/R09 Subscribe
// capture that backs a compile switch and accepts it exactly when AWS did:
// anything-but suffix (T07, H7b-01), numbers past the documented range
// (T15, the H5 subscribes), 3 wildcard points per star (H11) and a
// malformed "$or" rejected (H10). TestSNSRecordedReasons (internal/ruler)
// compares the refusal texts.
func TestRecordedSubscribes(t *testing.T) {
	t.Parallel()
	for _, step := range []string{
		"T07-subscribe", "H7b-01-subscribe", "T15-subscribe",
		"H5-01-attr-subscribe", "H5-01-body-subscribe", "H5-02-attr-subscribe", "H5-02-body-subscribe",
		"H5-03-attr-subscribe", "H5-03-body-subscribe", "H5-04-attr-subscribe", "H5-04-body-subscribe",
		"H5-05-attr-subscribe", "H5-05-body-subscribe", "H5-06-attr-subscribe", "H5-06-body-subscribe",
		"H5-09-attr-subscribe", "H5-09-body-subscribe",
		"H11-10-single-subscribe", "H11-11-single-subscribe", "H11-3-wildcards-subscribe",
		"H11-4-wildcards-subscribe", "H11-5-two-wildcard-subscribe",
		"H10-01-subscribe", "H10-02-subscribe", "H10-03-subscribe",
		"H10-04-subscribe", "H10-05-subscribe", "H10-06-subscribe",
	} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			s := readR09Subscribe(t, step)
			_, err := filterpolicy.Compile(s.policy, s.scope)
			if accepted := err == nil; accepted != s.accepted {
				t.Fatalf("Compile(%s) accepted = %t (%v), AWS accepted = %t", s.policy, accepted, err, s.accepted)
			}
		})
	}
}

// summaryRow is one row of sns/R09/summary.tsv: a message published to the
// topic of its case's subscription and whether AWS delivered it.
type summaryRow struct {
	kase, message, body, attributes string
	delivered                       bool
}

func readSummary(t *testing.T) []summaryRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r09Dir, "summary.tsv"))
	if err != nil {
		t.Fatalf("read sns/R09/summary.tsv: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	rows := make([]summaryRow, 0, len(lines))
	for _, line := range lines[1:] {
		f := strings.Split(line, "\t")
		if len(f) != 5 {
			t.Fatalf("sns/R09/summary.tsv row %q has %d fields, want case, message, result, body, attributes", line, len(f))
		}
		rows = append(rows, summaryRow{kase: f[0], message: f[1], delivered: f[2] == "delivered", body: f[3], attributes: f[4]})
	}
	return rows
}

// messageAttributes parses a summary row's attributes column, the
// MessageAttributes the row published.
func messageAttributes(t *testing.T, column string) []delivery.MessageAttribute {
	t.Helper()
	var in map[string]struct{ DataType, StringValue string }
	if err := json.Unmarshal([]byte(column), &in); err != nil {
		t.Fatalf("attributes %s: %v", column, err)
	}
	out := make([]delivery.MessageAttribute, 0, len(in))
	for name, a := range in {
		var v delivery.AttributeValue
		switch a.DataType {
		case "Number":
			v = delivery.NumberAttribute{Value: a.StringValue}
		case "String":
			v = delivery.StringAttribute{Value: a.StringValue}
		case "String.Array":
			v = delivery.StringAttribute{Label: "Array", Value: a.StringValue}
		default:
			t.Fatalf("attribute %s has DataType %q, which this test does not read", name, a.DataType)
		}
		out = append(out, delivery.MessageAttribute{Name: name, Value: v})
	}
	return out
}

// TestRecordedDeliveries replays every sns/R09/summary.tsv row of the cases
// that back a match switch, compiling the policy the case's Subscribe
// capture sent: bare numbers compared as numbers (T01 to T03, H6), a
// Number attribute read as a JSON number (T19a, T19b), numbers past the
// documented range (T15, H5), anything-but over an array (T08, H4) and
// sibling keys kept within one array element (T14). A row of the
// MessageAttributes scope publishes its attributes, one of the MessageBody
// scope its body.
func TestRecordedDeliveries(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"T01": true, "T02": true, "T03": true, "H6-attr": true, "H6-body": true, "H6-numeric-string-attr": true,
		"T19a": true, "T19b": true,
		"T15": true,
		"T08": true, "H4-attr": true, "H4-body": true,
		"T14": true,
	}
	for _, c := range []string{"H5-01", "H5-02", "H5-03", "H5-04", "H5-05", "H5-06", "H5-09"} {
		cases[c+"-attr"], cases[c+"-body"] = true, true
	}
	seen := map[string]bool{}
	for _, row := range readSummary(t) {
		if !cases[row.kase] {
			continue
		}
		seen[row.kase] = true
		t.Run(row.kase+" "+row.message, func(t *testing.T) {
			t.Parallel()
			s := readR09Subscribe(t, row.kase+"-subscribe")
			p, err := filterpolicy.Compile(s.policy, s.scope)
			if err != nil {
				t.Fatalf("Compile(%s): %q; AWS accepted it", s.policy, err.Reason)
			}
			var attrs []delivery.MessageAttribute
			if s.scope == filterpolicy.MessageAttributes {
				attrs = messageAttributes(t, row.attributes)
			}
			if got, merr := p.Match(attrs, row.body); merr != nil || got != row.delivered {
				t.Fatalf("Match(%s) of body %s, attributes %s = %v, %v; AWS delivered = %v", s.policy, row.body, row.attributes, got, merr, row.delivered)
			}
		})
	}
	for c := range cases {
		if !seen[c] {
			t.Errorf("sns/R09/summary.tsv has no row for case %s", c)
		}
	}
}
