package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/server"
)

// frozenAt is where the admin tests freeze the emulator clock.
var frozenAt = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// adminServer runs a seeded server frozen at frozenAt with cfg's other
// settings and returns a client for it.
func adminServer(t *testing.T, cfg server.Config) (c *admin.Client, endpoint string) {
	t.Helper()
	seed := uint64(11)
	cfg.Seed, cfg.ClockStart, cfg.Version = &seed, server.ClockFrozen{At: frozenAt}, "test"
	r := launch(t, cfg)
	r.stopOnCleanup(t)
	c, err := admin.New(r.endpoint, admin.Options{Token: cfg.AdminToken})
	if err != nil {
		t.Fatal(err)
	}
	return c, r.endpoint
}

// sqsCall sends one signed awsJson1_0 SQS request as the default account's
// root and returns the status, the request id the server answered with and
// the body.
func sqsCall(t *testing.T, endpoint, target, body string) (status int, requestID, out string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS."+target)
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	auth.Sign(req, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "sqs"}, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("X-Amzn-Requestid"), string(data)
}

// mustSQS sends an SQS request that must succeed and returns its body.
func mustSQS(t *testing.T, endpoint, target, body string) string {
	t.Helper()
	status, _, out := sqsCall(t, endpoint, target, body)
	if status != http.StatusOK {
		t.Fatalf("%s = %d %s", target, status, out)
	}
	return out
}

// queue creates a queue holding one message and returns its URL.
func queue(t *testing.T, endpoint, name, message string) string {
	t.Helper()
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"`+name+`"}`)
	url := endpoint + "/000000000000/" + name
	mustSQS(t, endpoint, "SendMessage", `{"QueueUrl":"`+url+`","MessageBody":"`+message+`"}`)
	return url
}

// wantCode checks err is an admin error with code.
func wantCode(t *testing.T, err error, code admin.ErrorCode) {
	t.Helper()
	adminErr(t, err, code)
}

// adminErr checks err is an admin error with code and returns it.
func adminErr(t *testing.T, err error, code admin.ErrorCode) admin.Error {
	t.Helper()
	var e *admin.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
	return *e
}

func TestAdminHealthReportsReadinessAndServices(t *testing.T) {
	c, _ := adminServer(t, server.Config{})
	h, err := c.Health(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !h.Ready || h.Version != "test" || h.Services["sqs"].Status != admin.Enabled || !strings.HasPrefix(h.Model, "api-models-aws@") {
		t.Fatalf("health = %+v", h)
	}
	if len(h.Services) != 12 {
		t.Fatalf("health lists %d services, want the twelve of phase 1: %+v", len(h.Services), h.Services)
	} // Every wired service holds its state under its registry id, so a
	// reset can name each one.
	names := make([]string, 0, len(h.Services))
	for name := range h.Services {
		names = append(names, name)
	}
	if _, err := c.Reset(t.Context(), admin.ResetRequest{Services: names}); err != nil {
		t.Fatalf("resetting every service by its registry id: %v", err)
	}
}

// Each protocol's codec is registered: a request for an operation no
// service binds yet reaches its service and is answered 501 in that
// protocol's envelope.
func TestEveryProtocolReachesItsService(t *testing.T) {
	_, endpoint := adminServer(t, server.Config{})
	cases := []struct {
		name, method, path, signing, body string
		header                            map[string]string
		want                              string
	}{
		{
			name: "awsQuery", method: http.MethodPost, path: "/", signing: "sns", body: "Action=ListTopics&Version=2010-03-31",
			header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, want: "<ErrorResponse",
		},
		{
			name: "awsJson1_1", method: http.MethodPost, path: "/", signing: "logs", body: "{}",
			header: map[string]string{"Content-Type": "application/x-amz-json-1.1", "X-Amz-Target": "Logs_20140328.DescribeLogGroups"}, want: "NotImplemented",
		},
		{
			name: "rpcv2Cbor", method: http.MethodPost, path: "/service/GraniteServiceVersion20100801/operation/ListMetrics", signing: "monitoring",
			header: map[string]string{"Content-Type": "application/cbor", "Smithy-Protocol": "rpc-v2-cbor"}, want: "NotImplemented",
		},
		{name: "restJson1", method: http.MethodGet, path: "/2015-03-31/functions/", signing: "lambda", want: "understudy does not implement lambda.ListFunctions"},
		{name: "restXml", method: http.MethodGet, path: "/", signing: "s3", want: "understudy does not implement s3.ListBuckets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), tc.method, endpoint+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			region, err := aws.KnownRegions().Parse("us-east-1")
			if err != nil {
				t.Fatal(err)
			}
			auth.Sign(req, []byte(tc.body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: tc.signing}, time.Now())
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusNotImplemented || !bytes.Contains(body, []byte(tc.want)) {
				t.Fatalf("%s %s = %d %q, want 501 containing %q", tc.method, tc.path, resp.StatusCode, body, tc.want)
			}
		})
	}
}

// A frozen clock advanced past a received message's visibility timeout
// fires the message's visibility timer, and the answer names it.
func TestAdminClockAdvanceFiresTimers(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	url := queue(t, endpoint, "orders", "m1")
	mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+url+`","VisibilityTimeout":30}`)

	st, err := c.Clock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != admin.Frozen || !st.Now.Equal(frozenAt) {
		t.Fatalf("clock = %+v, want frozen at %s", st, frozenAt)
	}
	res, err := c.ChangeClock(t.Context(), admin.Advance{By: 29 * time.Second})
	if err != nil || len(res.Fired) != 0 {
		t.Fatalf("advance 29s = %+v, %v; want nothing fired", res, err)
	}
	res, err = c.ChangeClock(t.Context(), admin.Advance{By: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fired) != 1 || res.Fired[0].Kind != "sqs.visibility" || !strings.HasPrefix(res.Fired[0].ID, "arn:aws:sqs:us-east-1:000000000000:orders/") {
		t.Fatalf("fired = %+v, want the message's visibility timer", res.Fired)
	}
	if want := frozenAt.Add(30 * time.Second); !res.Clock.Now.Equal(want) || res.Clock.Mode != admin.Frozen {
		t.Fatalf("clock after advance = %+v, want frozen at %s", res.Clock, want)
	}
	if body := mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+url+`"}`); !strings.Contains(body, `"Body":"m1"`) {
		t.Fatalf("after the visibility timeout the message is not receivable: %s", body)
	}
}

// Set moves forward only; mode changes are journaled.
func TestAdminClockModes(t *testing.T) {
	c, _ := adminServer(t, server.Config{})
	_, err := c.ChangeClock(t.Context(), admin.Set{To: frozenAt.Add(-time.Hour)})
	if e := adminErr(t, err, admin.InvalidRequest); e.Field != "to" || admin.ExitCode(err) != admin.ExitUsage {
		t.Fatalf("set backwards = %+v", e)
	}
	res, err := c.ChangeClock(t.Context(), admin.Set{To: frozenAt.Add(time.Hour)})
	if err != nil || !res.Clock.Now.Equal(frozenAt.Add(time.Hour)) {
		t.Fatalf("set forward = %+v, %v", res, err)
	}
	if res, err = c.ChangeClock(t.Context(), admin.Scale{Factor: 60}); err != nil || res.Clock.Mode != admin.Scaled || res.Clock.Factor != 60 {
		t.Fatalf("scale = %+v, %v", res, err)
	}
	if res, err = c.ChangeClock(t.Context(), admin.Resume{}); err != nil || res.Clock.Mode != admin.Running {
		t.Fatalf("resume = %+v, %v", res, err)
	}
	if res, err = c.ChangeClock(t.Context(), admin.Freeze{}); err != nil || res.Clock.Mode != admin.Frozen {
		t.Fatalf("freeze = %+v, %v", res, err)
	}
	_, err = c.ChangeClock(t.Context(), admin.Scale{Factor: 0})
	wantCode(t, err, admin.InvalidRequest)
	page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Kind: "clock-changed"})
	if err != nil || len(page.Events) != 3 {
		t.Fatalf("clock-changed events = %+v, %v; want scale, resume and freeze", page, err)
	}
}

// A rule naming an error the operation does not send is refused with the
// codes it does send; a valid rule fires and is listed with its hits.
func TestAdminFaults(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	bad := admin.FaultRule{
		ID: "bad", Phase: admin.RequestPhase,
		Match:   admin.FaultMatch{Operations: []string{"sqs.SendMessage"}},
		Action:  admin.InjectError{Shape: "com.amazonaws.sns#NotFoundException"},
		Trigger: admin.Always{},
	}
	_, err := c.PutFault(t.Context(), bad)
	e := adminErr(t, err, admin.InvalidRequest)
	if e.Field != "action.shape" || !slices.Contains(e.Known, "com.amazonaws.sqs#QueueDoesNotExist") {
		t.Fatalf("invalid rule = %+v, want the SQS codes", e)
	}

	url := queue(t, endpoint, "q", "m")
	put, err := c.PutFault(t.Context(), admin.FaultRule{
		ID: "throttle-sends", Phase: admin.RequestPhase,
		Match:   admin.FaultMatch{Operations: []string{"sqs.SendMessage"}},
		Action:  admin.Throttle{},
		Trigger: admin.NextN{N: 2},
	})
	if err != nil || put.Rule.ID != "throttle-sends" || put.Warning == "" {
		t.Fatalf("put = %+v, %v; want the rule with a retry warning", put, err)
	}
	if status, _, body := sqsCall(t, endpoint, "SendMessage", `{"QueueUrl":"`+url+`","MessageBody":"x"}`); status == http.StatusOK {
		t.Fatalf("SendMessage under a throttle rule = %d %s", status, body)
	}
	faults, err := c.Faults(t.Context())
	if err != nil || len(faults) != 1 || faults[0].Hits != 1 || !reflectEqualRule(faults[0].Rule, put.Rule) {
		t.Fatalf("faults = %+v, %v; want the rule with one hit", faults, err)
	}
	if err := c.DeleteFault(t.Context(), "throttle-sends"); err != nil {
		t.Fatal(err)
	}
	wantCode(t, c.DeleteFault(t.Context(), "throttle-sends"), admin.NotFound)
	if _, err := c.PutFault(t.Context(), put.Rule); err != nil {
		t.Fatal(err)
	}
	if err := c.ClearFaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	if faults, err := c.Faults(t.Context()); err != nil || len(faults) != 0 {
		t.Fatalf("faults after clear = %+v, %v", faults, err)
	}
}

func reflectEqualRule(a, b admin.FaultRule) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// The journal answers filtered queries, finds a call's tree by request id,
// and a stream resumed after an event replays what followed it, then
// carries new events live.
func TestAdminJournal(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	url := queue(t, endpoint, "j", "m")
	received := receiptHandle.FindStringSubmatch(mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+url+`"}`))
	if received == nil {
		t.Fatal("no message received")
	}
	receipt := received[1]
	page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Operation: "sqs.CreateQueue"})
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != "api-call" {
		t.Fatalf("CreateQueue events = %+v, %v", page, err)
	}
	created := page.Events[0].ID
	_, err = c.JournalEvents(t.Context(), admin.JournalQuery{Operation: "sqs.Explode"})
	wantCode(t, err, admin.InvalidRequest)

	_, rid, _ := sqsCall(t, endpoint, "GetQueueUrl", `{"QueueName":"j"}`)
	tree, err := c.Trace(t.Context(), admin.ByRequestID{RequestID: rid})
	if err != nil || tree.Event.Kind != "api-call" || !strings.Contains(string(tree.Event.Detail), rid) {
		t.Fatalf("trace by request id %s = %+v, %v", rid, tree, err)
	}
	if byID, err := c.Trace(t.Context(), admin.ByEvent{ID: tree.Event.ID}); err != nil || byID.Event.ID != tree.Event.ID {
		t.Fatalf("trace by event = %+v, %v", byID, err)
	}
	canonical, err := c.CanonicalTrace(t.Context(), admin.ByRequestID{RequestID: rid})
	if err != nil || canonical.Position != "0" || canonical.Event.ID != 0 {
		t.Fatalf("canonical trace = %+v, %v", canonical, err)
	}
	_, err = c.Trace(t.Context(), admin.ByEvent{ID: 1 << 40})
	wantCode(t, err, admin.NotFound)

	ctx, cancel := context.WithTimeout(t.Context(), waitTimeout)
	defer cancel()
	var ops []string
	for e, err := range c.JournalStream(ctx, admin.JournalQuery{Kind: "api-call"}, created) {
		if err != nil {
			t.Fatal(err)
		}
		var call struct {
			Operation string `json:"operation"`
		}
		if err := json.Unmarshal(e.Detail, &call); err != nil {
			t.Fatal(err)
		}
		ops = append(ops, call.Operation)
		if len(ops) == 3 {
			mustSQS(t, endpoint, "DeleteMessage", `{"QueueUrl":"`+url+`","ReceiptHandle":"`+receipt+`"}`)
		}
		if call.Operation == "DeleteMessage" {
			break
		}
	}
	if want := []string{"SendMessage", "ReceiveMessage", "GetQueueUrl", "DeleteMessage"}; !slices.Equal(ops, want) {
		t.Fatalf("streamed after CreateQueue: %v, want %v", ops, want)
	}
}

// A snapshot saved, then a reset, then the snapshot loaded brings back the
// queue and its message.
func TestAdminSnapshotRestoresAfterReset(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	url := queue(t, endpoint, "keep", "hello")
	var archive bytes.Buffer
	if err := c.SaveSnapshot(t.Context(), &archive, admin.SaveOptions{Gzip: true, IncludeJournal: true}); err != nil {
		t.Fatal(err)
	}
	reset, err := c.Reset(t.Context(), admin.ResetRequest{})
	if err != nil || !slices.Contains(reset.Services, "sqs") {
		t.Fatalf("reset = %+v, %v", reset, err)
	}
	if res, err := c.Resources(t.Context(), admin.ResourceQuery{}); err != nil || len(res) != 0 {
		t.Fatalf("resources after reset = %+v, %v", res, err)
	}
	info, err := c.LoadSnapshot(t.Context(), &archive)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(info.Services, "sqs") || info.Mode != admin.Frozen || !info.Clock.Equal(frozenAt) || info.Writer != "understudy test" {
		t.Fatalf("loaded = %+v", info)
	}
	if body := mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+url+`"}`); !strings.Contains(body, `"Body":"hello"`) {
		t.Fatalf("after load the message is gone: %s", body)
	}
	_, err = c.LoadSnapshot(t.Context(), strings.NewReader("not an archive"))
	wantCode(t, err, admin.InvalidRequest)
}

// Reset by service and region drops only what it selects.
func TestAdminResetSelects(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queue(t, endpoint, "stays", "m")
	if _, err := c.Reset(t.Context(), admin.ResetRequest{Regions: []string{"eu-west-1"}}); err != nil {
		t.Fatal(err)
	}
	if res, err := c.Resources(t.Context(), admin.ResourceQuery{}); err != nil || len(res) != 1 {
		t.Fatalf("resources after resetting another region = %+v, %v", res, err)
	}
	_, err := c.Reset(t.Context(), admin.ResetRequest{Services: []string{"nope"}})
	if e := adminErr(t, err, admin.InvalidRequest); !slices.Contains(e.Known, "sqs") {
		t.Fatalf("unknown service = %+v", e)
	}
	_, err = c.Reset(t.Context(), admin.ResetRequest{Accounts: []string{"12"}})
	wantCode(t, err, admin.InvalidRequest)

	if _, err := c.ChangeClock(t.Context(), admin.Advance{By: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reset(t.Context(), admin.ResetRequest{Clock: true}); err != nil {
		t.Fatal(err)
	}
	if st, err := c.Clock(t.Context()); err != nil || !st.Now.Equal(frozenAt) || st.Mode != admin.Frozen {
		t.Fatalf("clock after a reset with clock = %+v, %v; want frozen at %s again", st, err, frozenAt)
	}
}

// A reset drops the timers of the state it drops and keeps those of the
// state it keeps: timers are derived from state (ARCHITECTURE.md 13).
func TestAdminResetRebuildsTimers(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	url := queue(t, endpoint, "orders", "m1")
	mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+url+`","VisibilityTimeout":30}`)
	if _, err := c.Reset(t.Context(), admin.ResetRequest{Regions: []string{"eu-west-1"}}); err != nil {
		t.Fatal(err)
	}
	res, err := c.ChangeClock(t.Context(), admin.Advance{By: 30 * time.Second})
	if err != nil || len(res.Fired) != 1 || res.Fired[0].Kind != "sqs.visibility" {
		t.Fatalf("advance after resetting another region = %+v, %v; want the kept message's visibility timer", res, err)
	}

	mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+url+`","VisibilityTimeout":30}`)
	if _, err := c.Reset(t.Context(), admin.ResetRequest{}); err != nil {
		t.Fatal(err)
	}
	res, err = c.ChangeClock(t.Context(), admin.Advance{By: 30 * time.Second})
	if err != nil || len(res.Fired) != 0 {
		t.Fatalf("advance after a reset = %+v, %v; want no timer of the dropped queue", res, err)
	}
}

// With persistence the path form saves and loads under the data directory;
// without it the path form is unavailable.
func TestAdminSnapshotPathForm(t *testing.T) {
	dir := t.TempDir()
	c, endpoint := adminServer(t, server.Config{Persist: server.PersistDir{Path: dir}})
	queue(t, endpoint, "p", "m")
	saved, err := c.SaveSnapshotTo(t.Context(), "snaps/one.tar", admin.SaveOptions{})
	if err != nil || saved.Path != filepath.Join("snaps", "one.tar") || saved.Bytes == 0 {
		t.Fatalf("save to path = %+v, %v", saved, err)
	}
	if _, err := c.Reset(t.Context(), admin.ResetRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LoadSnapshotFrom(t.Context(), "snaps/one.tar"); err != nil {
		t.Fatal(err)
	}
	if res, err := c.Resources(t.Context(), admin.ResourceQuery{Services: []string{"sqs"}}); err != nil || len(res) != 1 {
		t.Fatalf("resources after loading from a path = %+v, %v", res, err)
	}
	_, err = c.SaveSnapshotTo(t.Context(), "../escape.tar", admin.SaveOptions{})
	wantCode(t, err, admin.InvalidRequest)
	_, err = c.LoadSnapshotFrom(t.Context(), "snaps/missing.tar")
	wantCode(t, err, admin.NotFound)
	_, err = c.LoadSnapshotFrom(t.Context(), "snaps")
	wantCode(t, err, admin.InvalidRequest)

	plain, _ := adminServer(t, server.Config{})
	_, err = plain.SaveSnapshotTo(t.Context(), "x.tar", admin.SaveOptions{})
	wantCode(t, err, admin.Unavailable)
}

// Two saves to one path each write their own temporary file, so the
// archive left at the path is whole and loads.
func TestAdminConcurrentSavesToOnePath(t *testing.T) {
	dir := t.TempDir()
	c, endpoint := adminServer(t, server.Config{Persist: server.PersistDir{Path: dir}})
	queue(t, endpoint, "p", "m")
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := c.SaveSnapshotTo(t.Context(), "snaps/same.tar", admin.SaveOptions{})
			errs <- err
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent save = %v", err)
		}
	}
	if _, err := c.LoadSnapshotFrom(t.Context(), "snaps/same.tar"); err != nil {
		t.Fatalf("loading the archive two saves raced on = %v", err)
	}
	if res, err := c.Resources(t.Context(), admin.ResourceQuery{Services: []string{"sqs"}}); err != nil || len(res) != 1 {
		t.Fatalf("resources after the load = %+v, %v", res, err)
	}
	left, err := filepath.Glob(filepath.Join(dir, "snaps", "*.tmp"))
	if err != nil || len(left) != 0 {
		t.Fatalf("temporary files left behind = %v, %v", left, err)
	}
}

func TestAdminResourcesListsQueues(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queue(t, endpoint, "listed", "m")
	res, err := c.Resources(t.Context(), admin.ResourceQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := admin.Resource{Service: "sqs", Account: "000000000000", Region: "us-east-1", ARN: "arn:aws:sqs:us-east-1:000000000000:listed", Summary: "1 messages"}
	if len(res) != 1 || res[0] != want {
		t.Fatalf("resources = %+v, want %+v", res, want)
	}
	if res, err := c.Resources(t.Context(), admin.ResourceQuery{Regions: []string{"eu-west-1"}}); err != nil || len(res) != 0 {
		t.Fatalf("resources in eu-west-1 = %+v, %v", res, err)
	}
}

func TestAdminSettle(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queue(t, endpoint, "s", "m")
	got, err := c.Settle(t.Context(), 5*time.Second)
	if err != nil || !got.Settled {
		t.Fatalf("settle = %+v, %v", got, err)
	}
}

// The access record names the actions a principal used and renders its
// least-privilege policy.
func TestAdminIAM(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queue(t, endpoint, "i", "m")
	const root = "arn:aws:iam::000000000000:root"
	uses, err := c.IAMUsage(t.Context(), admin.UsageQuery{Principal: root, Service: "sqs"})
	if err != nil {
		t.Fatal(err)
	}
	actions := make([]string, 0, len(uses))
	for _, u := range uses {
		actions = append(actions, u.Action)
	}
	if !slices.Contains(actions, "sqs:CreateQueue") || !slices.Contains(actions, "sqs:SendMessage") {
		t.Fatalf("usage = %+v", uses)
	}
	policy, err := c.IAMPolicy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(policy.Document), `"sqs:CreateQueue"`) || !strings.Contains(string(policy.Document), "arn:aws:sqs:us-east-1:000000000000:i") {
		t.Fatalf("policy = %s", policy.Document)
	}
	_, err = c.IAMPolicy(t.Context(), "arn:aws:iam::123456789012:root")
	wantCode(t, err, admin.NotFound)
	_, err = c.IAMPolicy(t.Context(), "sns.amazonaws.com")
	wantCode(t, err, admin.InvalidRequest)
}

// Endpoints whose service this build does not implement answer
// unavailable with a hint.
func TestAdminUnimplementedHooksAreUnavailable(t *testing.T) {
	c, _ := adminServer(t, server.Config{})
	_, err := c.Coverage(t.Context(), "json")
	wantCode(t, err, admin.Unavailable)
	wantCode(t, c.HotReload(t.Context(), "fn", admin.HotReloadRequest{Dir: "/tmp/build"}), admin.Unavailable)
	_, err = c.PullImages(t.Context(), admin.ImagePullRequest{})
	wantCode(t, err, admin.Unavailable)
	_, err = c.SweepTTL(t.Context())
	wantCode(t, err, admin.Unavailable)
	_, err = c.SubscriptionToken(t.Context(), "arn:aws:sns:us-east-1:000000000000:t:1")
	if e := adminErr(t, err, admin.Unavailable); e.Hint == "" {
		t.Fatalf("unavailable without a hint: %+v", e)
	}
}

// Unknown paths, methods and parameters are refused in the admin envelope,
// on every route: a filter misplaced in the query of a destructive call
// changes nothing. HEAD is refused on the endpoints that wait for events.
func TestAdminRefusesUnknownRequests(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queue(t, endpoint, "survives", "m")
	if _, err := c.PutFault(t.Context(), admin.FaultRule{
		ID: "kept", Phase: admin.RequestPhase, Action: admin.Throttle{}, Trigger: admin.NextN{N: 1},
		Match: admin.FaultMatch{Operations: []string{"sqs.DeleteQueue"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/_understudy/v1/nope", http.StatusNotFound},
		{http.MethodDelete, "/_understudy/v1/clock", http.StatusMethodNotAllowed},
		{http.MethodGet, "/_understudy/v1/journal/events?bogus=1", http.StatusBadRequest},
		{http.MethodGet, "/_understudy/v1/settle?timeout=soon", http.StatusBadRequest},
		{http.MethodPost, "/_understudy/v1/reset?services=sqs", http.StatusBadRequest},
		{http.MethodDelete, "/_understudy/v1/faults?id=other", http.StatusBadRequest},
		{http.MethodGet, "/_understudy/v1/clock?at=now", http.StatusBadRequest},
		{http.MethodGet, "/_understudy/v1/health?verbose=1", http.StatusBadRequest},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, endpoint+tc.path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var env admin.Envelope
		err = errors.Join(json.NewDecoder(resp.Body).Decode(&env), resp.Body.Close())
		if err != nil || resp.StatusCode != tc.status || env.Error == nil {
			t.Errorf("%s %s = %d %+v (%v), want %d in the admin envelope", tc.method, tc.path, resp.StatusCode, env, err, tc.status)
		}
	}
	if res, err := c.Resources(t.Context(), admin.ResourceQuery{Services: []string{"sqs"}}); err != nil || len(res) != 1 {
		t.Fatalf("resources after refused calls = %+v, %v; want the queue kept", res, err)
	}
	if faults, err := c.Faults(t.Context()); err != nil || len(faults) != 1 {
		t.Fatalf("faults after refused calls = %+v, %v; want the rule kept", faults, err)
	}
	_, err := c.ChangeClock(t.Context(), admin.Advance{By: -time.Second})
	wantCode(t, err, admin.InvalidRequest)

	for _, path := range []string{"/_understudy/v1/journal/stream", "/_understudy/v1/settle"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodHead, endpoint+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodGet {
			t.Errorf("HEAD %s = %d, Allow %q; want 405 with Allow: GET", path, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodHead, endpoint+"/_understudy/v1/clock", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD clock = %d, %v; want 200", resp.StatusCode, err)
	}
}

// A non-loopback listener needs an admin token; the refusal names the token
// and the loopback alternative.
func TestNonLoopbackListenerNeedsAToken(t *testing.T) {
	exposed := netip.MustParseAddrPort("0.0.0.0:0")
	_, err := server.New(server.Config{Listen: exposed, LogOutput: io.Discard})
	var le *server.ListenError
	if !errors.As(err, &le) || !strings.Contains(err.Error(), "admin token") || !strings.Contains(err.Error(), "--listen 127.0.0.1:") {
		t.Fatalf("New on 0.0.0.0 without a token = %v", err)
	}
	newServer(t, server.Config{Listen: exposed, AdminToken: "s3cret", LogOutput: io.Discard})
}

// With a token, every admin call without it is refused as unauthorized,
// and the client that sends it is served.
func TestAdminTokenGuardsEveryEndpoint(t *testing.T) {
	srv := newServer(t, server.Config{AdminToken: "s3cret", LogOutput: io.Discard})
	paths := []struct{ method, path string }{
		{http.MethodGet, "health"},
		{http.MethodPost, "reset"},
		{http.MethodGet, "clock"},
		{http.MethodPost, "clock"},
		{http.MethodGet, "faults"},
		{http.MethodDelete, "faults"},
		{http.MethodPut, "faults/x"},
		{http.MethodDelete, "faults/x"},
		{http.MethodGet, "journal/events"},
		{http.MethodGet, "journal/stream"},
		{http.MethodGet, "journal/traces/1"},
		{http.MethodPost, "snapshots"},
		{http.MethodPut, "snapshots"},
		{http.MethodGet, "resources"},
		{http.MethodGet, "coverage"},
		{http.MethodGet, "iam/usage"},
		{http.MethodGet, "iam/policy"},
		{http.MethodGet, "settle"},
		{http.MethodPost, "lambda/functions/f/hot-reload"},
		{http.MethodPost, "lambda/images/pull"},
		{http.MethodPost, "dynamodb/ttl-sweep"},
		{http.MethodGet, "sns/subscriptions/arn/token"},
	}
	for _, p := range paths {
		for _, auth := range []string{"", "Bearer wrong", "s3cret"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), p.method, "/_understudy/v1/"+p.path, http.NoBody)
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			srv.Handler().ServeHTTP(rec, req)
			var env admin.Envelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != http.StatusUnauthorized || env.Error.Code != admin.Unauthorized {
				t.Errorf("%s %s with Authorization %q = %d %s, want 401 unauthorized", p.method, p.path, auth, rec.Code, rec.Body)
			}
		}
	}
	// The auth-scheme name is case-insensitive (RFC 7235 2.1).
	for _, auth := range []string{"Bearer s3cret", "bearer s3cret", "BEARER s3cret"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_understudy/v1/clock", http.NoBody)
		req.Header.Set("Authorization", auth)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET clock with Authorization %q = %d %s, want 200", auth, rec.Code, rec.Body)
		}
	}

	c, _ := adminServer(t, server.Config{AdminToken: "s3cret"})
	if h, err := c.Health(t.Context()); err != nil || !h.Ready {
		t.Fatalf("health with the token = %+v, %v", h, err)
	}
}
