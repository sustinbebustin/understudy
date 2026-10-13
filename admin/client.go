package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Prefix is the path every admin endpoint lives under.
const Prefix = "/_understudy/v1"

// Media types of the admin API's non-JSON bodies.
const (
	// NDJSON is the journal stream's content type.
	NDJSON = "application/x-ndjson"
	// Tar is an uncompressed snapshot archive.
	Tar = "application/x-tar"
	// Gzip is a gzipped snapshot archive.
	Gzip = "application/gzip"
)

// Options configures a Client. The zero value talks to a loopback server
// without a token through http.DefaultClient.
type Options struct {
	// Token is the admin token a non-loopback server requires, sent as
	// Authorization: Bearer <token>.
	Token string
	// HTTPClient sends the requests; nil means http.DefaultClient.
	HTTPClient *http.Client
}

// Client calls the admin API of one running understudy. Every method
// returns an *Error for an error the server answered, an *UnreachableError
// when no answer came, and ExitCode maps either to the CLI's exit code.
type Client struct {
	base  url.URL
	token string
	hc    *http.Client
}

// EndpointError reports an endpoint New cannot use.
type EndpointError struct {
	Endpoint string
	Reason   string
}

func (e *EndpointError) Error() string {
	return fmt.Sprintf("admin: endpoint %q %s; pass the server's base URL, such as http://127.0.0.1:4566", e.Endpoint, e.Reason)
}

// New returns a client for the server at endpoint, its base URL such as
// http://127.0.0.1:4566.
func New(endpoint string, o Options) (*Client, error) {
	u, err := url.Parse(endpoint)
	switch {
	case err != nil:
		return nil, &EndpointError{Endpoint: endpoint, Reason: "is not a URL: " + err.Error()}
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, &EndpointError{Endpoint: endpoint, Reason: "needs an http or https scheme"}
	case u.Host == "":
		return nil, &EndpointError{Endpoint: endpoint, Reason: "has no host"}
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "":
		return nil, &EndpointError{Endpoint: endpoint, Reason: "has a path, query or fragment"}
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: url.URL{Scheme: u.Scheme, Host: u.Host}, token: o.Token, hc: hc}, nil
}

// Health reads the readiness endpoint. A server still starting answers 503
// with Ready false and what it is waiting for, which Health returns with no
// error. Any other answer outside 2xx, or a 503 whose body is not that
// readiness answer (a proxy's error page), is the error send reports.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	resp, err := c.send(ctx, http.MethodGet, "/health", nil, nil, "")
	if err != nil {
		if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
			return nil, err
		}
		return notReady(resp, err)
	}
	var h Health
	if err := c.decode(resp, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// notReady reads a 503 health answer: the readiness body of a server still
// starting, or else sendErr, the error send made of the answer.
func notReady(resp *http.Response, sendErr error) (*Health, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, sendErr
	}
	var h Health
	if decodeStrict(data, &h) != nil || h.Ready || len(h.Waiting) == 0 {
		return nil, sendErr
	}
	return &h, nil
}

// Reset drops the service state r selects.
func (c *Client) Reset(ctx context.Context, r ResetRequest) (*ResetResult, error) {
	return fetch[ResetResult](ctx, c, http.MethodPost, "/reset", nil, r)
}

// Clock reads the emulator clock.
func (c *Client) Clock(ctx context.Context) (*ClockState, error) {
	return fetch[ClockState](ctx, c, http.MethodGet, "/clock", nil, nil)
}

// ChangeClock freezes, resumes, advances, sets or scales the clock.
func (c *Client) ChangeClock(ctx context.Context, op ClockOp) (*ClockResult, error) {
	return fetch[ClockResult](ctx, c, http.MethodPost, "/clock", nil, op)
}

// FaultList is GET /faults.
type FaultList struct {
	Faults []Fault `json:"faults"`
}

// Faults lists the active fault rules in evaluation order.
func (c *Client) Faults(ctx context.Context) ([]Fault, error) {
	out, err := fetch[FaultList](ctx, c, http.MethodGet, "/faults", nil, nil)
	if err != nil {
		return nil, err
	}
	return out.Faults, nil
}

// PutFault adds rule, or replaces the active rule with its id. The server
// validates it against the errors its operations really send.
func (c *Client) PutFault(ctx context.Context, rule FaultRule) (*Fault, error) {
	if rule.ID == "" {
		return nil, &Error{Code: InvalidRequest, Field: "id", Message: "a fault rule needs an id to be put", Hint: "set FaultRule.ID, such as orders-throttle"}
	}
	return fetch[Fault](ctx, c, http.MethodPut, "/faults/"+url.PathEscape(rule.ID), nil, rule)
}

// DeleteFault removes the rule with id.
func (c *Client) DeleteFault(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/faults/"+url.PathEscape(id), nil, nil, nil)
}

// ClearFaults removes every rule.
func (c *Client) ClearFaults(ctx context.Context) error {
	return c.call(ctx, http.MethodDelete, "/faults", nil, nil, nil)
}

// JournalEvents returns one page of the events q selects.
func (c *Client) JournalEvents(ctx context.Context, q JournalQuery) (*JournalPage, error) {
	return fetch[JournalPage](ctx, c, http.MethodGet, "/journal/events", q.Values(), nil)
}

// JournalStream follows the events q selects as they are recorded, starting
// after the event with id after (0 starts live). The stream ends when ctx
// does; a consumer too slow for the emulator receives a "gap" event for
// what it missed.
func (c *Client) JournalStream(ctx context.Context, q JournalQuery, after uint64) iter.Seq2[JournalEvent, error] {
	return func(yield func(JournalEvent, error) bool) {
		v := q.Values()
		v.Del(ParamLimit)
		v.Del(ParamCursor)
		if after != 0 {
			v.Set(ParamAfter, strconv.FormatUint(after, 10))
		}
		resp, err := c.send(ctx, http.MethodGet, "/journal/stream", v, nil, "")
		if err != nil {
			yield(JournalEvent{}, err)
			return
		}
		defer resp.Body.Close()
		br := bufio.NewReader(resp.Body)
		for {
			e, ok, err := nextEvent(br)
			switch {
			case err != nil && ctx.Err() != nil:
				return
			case err != nil:
				yield(JournalEvent{}, err)
				return
			case !ok:
				return
			case !yield(e, nil):
				return
			}
		}
	}
}

// nextEvent reads the next event of an NDJSON stream, skipping blank lines;
// ok is false at the end of the stream.
func nextEvent(br *bufio.Reader) (e JournalEvent, ok bool, err error) {
	for {
		line, readErr := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if err := json.Unmarshal(line, &e); err != nil {
				return e, false, fmt.Errorf("admin: the journal stream sent a line that is not an event: %w", err)
			}
			return e, true, nil
		}
		switch {
		case errors.Is(readErr, io.EOF):
			return e, false, nil
		case readErr != nil:
			return e, false, fmt.Errorf("admin: reading the journal stream: %w", readErr)
		}
	}
}

// Trace returns the causal tree key names.
func (c *Client) Trace(ctx context.Context, key TraceKey) (*Trace, error) {
	path, v, err := tracePath(key)
	if err != nil {
		return nil, err
	}
	return fetch[Trace](ctx, c, http.MethodGet, path, v, nil)
}

// CanonicalTrace returns the normalized form of the causal tree key names.
func (c *Client) CanonicalTrace(ctx context.Context, key TraceKey) (*CanonicalTrace, error) {
	path, v, err := tracePath(key)
	if err != nil {
		return nil, err
	}
	v.Set(ParamCanonical, "1")
	return fetch[CanonicalTrace](ctx, c, http.MethodGet, path, v, nil)
}

func tracePath(key TraceKey) (string, url.Values, error) {
	switch k := key.(type) {
	case ByEvent:
		return "/journal/traces/" + strconv.FormatUint(k.ID, 10), url.Values{}, nil
	case ByRequestID:
		return "/journal/traces", url.Values{ParamRequestID: {k.RequestID}}, nil
	default:
		return "", nil, &Error{Code: InvalidRequest, Message: "a trace needs an event id or a request id", Hint: "pass admin.ByEvent{ID: n} or admin.ByRequestID{RequestID: id}"}
	}
}

// SaveSnapshot writes an archive of the emulator's state to w and reports
// its size and the effects the cut was taken without.
func (c *Client) SaveSnapshot(ctx context.Context, w io.Writer, o SaveOptions) (*SavedArchive, error) {
	resp, err := c.send(ctx, http.MethodPost, "/snapshots", o.values(), nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var saved SavedArchive
	if h := resp.Header.Get(PendingEffectsHeader); h != "" {
		if saved.PendingEffects, err = strconv.Atoi(h); err != nil || saved.PendingEffects < 0 {
			return nil, fmt.Errorf("admin: the server answered %s: %q, which is not a count; nothing was written to the archive's writer", PendingEffectsHeader, h)
		}
	}
	if saved.Bytes, err = io.Copy(w, resp.Body); err != nil {
		return nil, fmt.Errorf("admin: copying the snapshot archive after %d bytes: %w", saved.Bytes, err)
	}
	return &saved, nil
}

// SaveSnapshotTo writes an archive to path under the server's data
// directory, which needs serve --persist.
func (c *Client) SaveSnapshotTo(ctx context.Context, path string, o SaveOptions) (*SnapshotInfo, error) {
	return fetch[SnapshotInfo](ctx, c, http.MethodPost, "/snapshots", o.values(), SnapshotRequest{Path: path})
}

// LoadSnapshot replaces the emulator's state with the archive r holds,
// gzipped or not.
func (c *Client) LoadSnapshot(ctx context.Context, r io.Reader) (*SnapshotInfo, error) {
	resp, err := c.send(ctx, http.MethodPut, "/snapshots", nil, r, Tar)
	if err != nil {
		return nil, err
	}
	var out SnapshotInfo
	if err := c.decode(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LoadSnapshotFrom loads the archive at path under the server's data
// directory, which needs serve --persist.
func (c *Client) LoadSnapshotFrom(ctx context.Context, path string) (*SnapshotInfo, error) {
	return fetch[SnapshotInfo](ctx, c, http.MethodPut, "/snapshots", nil, SnapshotRequest{Path: path})
}

func (o SaveOptions) values() url.Values {
	v := url.Values{}
	if o.Gzip {
		v.Set("gzip", "1")
	}
	if o.IncludeJournal {
		v.Set("journal", "1")
	}
	return v
}

// ResourceList is GET /resources.
type ResourceList struct {
	Resources []Resource `json:"resources"`
}

// Resources lists the resources q selects, by service, account, region and
// ARN.
func (c *Client) Resources(ctx context.Context, q ResourceQuery) ([]Resource, error) {
	v := url.Values{"service": q.Services, "account": q.Accounts, "region": q.Regions}
	out, err := fetch[ResourceList](ctx, c, http.MethodGet, "/resources", v, nil)
	if err != nil {
		return nil, err
	}
	return out.Resources, nil
}

// Coverage returns the coverage report in format, "json" or "markdown".
func (c *Client) Coverage(ctx context.Context, format string) ([]byte, error) {
	resp, err := c.send(ctx, http.MethodGet, "/coverage", url.Values{"format": {format}}, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// UsageList is GET /iam/usage.
type UsageList struct {
	Usage []Usage `json:"usage"`
}

// IAMUsage returns the recorded uses q selects.
func (c *Client) IAMUsage(ctx context.Context, q UsageQuery) ([]Usage, error) {
	v := url.Values{}
	if q.Principal != "" {
		v.Set("principal", q.Principal)
	}
	if q.Service != "" {
		v.Set("service", q.Service)
	}
	out, err := fetch[UsageList](ctx, c, http.MethodGet, "/iam/usage", v, nil)
	if err != nil {
		return nil, err
	}
	return out.Usage, nil
}

// IAMPolicy returns the least-privilege policy for principal, an IAM ARN.
func (c *Client) IAMPolicy(ctx context.Context, principal string) (*Policy, error) {
	return fetch[Policy](ctx, c, http.MethodGet, "/iam/policy", url.Values{"principal": {principal}}, nil)
}

// Settle waits up to timeout for the emulator's tracked work to finish. On
// timeout it returns an *Error with code condition_not_met whose Pending
// names what is still outstanding.
func (c *Client) Settle(ctx context.Context, timeout time.Duration) (*Settle, error) {
	return fetch[Settle](ctx, c, http.MethodGet, "/settle", url.Values{"timeout": {timeout.String()}}, nil)
}

// HotReload binds the host directory r.Dir as the code of function.
func (c *Client) HotReload(ctx context.Context, function string, r HotReloadRequest) error {
	return c.call(ctx, http.MethodPost, "/lambda/functions/"+url.PathEscape(function)+"/hot-reload", nil, r, nil)
}

// PullImages pre-pulls the pinned runtime images r names.
func (c *Client) PullImages(ctx context.Context, r ImagePullRequest) (*ImagePullResult, error) {
	return fetch[ImagePullResult](ctx, c, http.MethodPost, "/lambda/images/pull", nil, r)
}

// SweepTTL runs the DynamoDB TTL sweep now.
func (c *Client) SweepTTL(ctx context.Context) (*TTLSweepResult, error) {
	return fetch[TTLSweepResult](ctx, c, http.MethodPost, "/dynamodb/ttl-sweep", nil, nil)
}

// SubscriptionToken reads the confirmation token of the pending SNS
// subscription with ARN arn.
func (c *Client) SubscriptionToken(ctx context.Context, arn string) (*SubscriptionToken, error) {
	return fetch[SubscriptionToken](ctx, c, http.MethodGet, "/sns/subscriptions/"+url.PathEscape(arn)+"/token", nil, nil)
}

// fetch sends a JSON request (in may be nil) and returns the JSON answer
// decoded as T, or nil and the error.
func fetch[T any](ctx context.Context, c *Client, method, path string, v url.Values, in any) (*T, error) {
	var out T
	if err := c.call(ctx, method, path, v, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// call sends a JSON request (in may be nil) and decodes a JSON answer into
// out (nil discards it).
func (c *Client) call(ctx context.Context, method, path string, v url.Values, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("admin: encoding the %s %s request: %w", method, path, err)
		}
		body, contentType = bytes.NewReader(data), "application/json"
	}
	resp, err := c.send(ctx, method, path, v, body, contentType)
	if err != nil {
		return err
	}
	return c.decode(resp, out)
}

// send makes one request. An answer outside 2xx is returned as the *Error
// its envelope holds, with the response (body unread) for the caller that
// reads past it; otherwise the caller closes the body.
func (c *Client) send(ctx context.Context, method, path string, v url.Values, body io.Reader, contentType string) (*http.Response, error) {
	u := c.base
	u.Path = Prefix + path
	u.RawPath = ""
	if strings.Contains(path, "%") {
		u.RawPath = Prefix + path
		unescaped, err := url.PathUnescape(path)
		if err != nil {
			return nil, fmt.Errorf("admin: path %q: %w", path, err)
		}
		u.Path = Prefix + unescaped
	}
	u.RawQuery = v.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("admin: building the %s %s request: %w", method, path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, &UnreachableError{Endpoint: c.base.String(), Err: err}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	closeErr := resp.Body.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return nil, &UnreachableError{Endpoint: c.base.String(), Err: err}
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Error == nil {
		return resp, &Error{
			Code:    Unavailable,
			Message: fmt.Sprintf("%s %s answered %s without an admin error", method, path, resp.Status),
			Hint:    "check that --endpoint points at understudy and not another server",
		}
	}
	return resp, env.Error
}

// maxErrorBytes bounds an error body the client reads.
const maxErrorBytes = 1 << 20

// decode reads a JSON answer into out and closes the body.
func (c *Client) decode(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if out == nil {
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("admin: the server answered %s %s with a body that does not decode: %w", resp.Request.Method, resp.Request.URL.Path, err)
	}
	return nil
}

// decodeStrict decodes one JSON value, refusing unknown members and
// trailing data.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}
