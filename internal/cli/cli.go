package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/config"
)

// Options are what the CLI runs against: the process's streams,
// environment and files, injected so tests drive it in process.
type Options struct {
	Stdout io.Writer
	Stderr io.Writer
	// Env is the environment as "NAME=value" pairs (os.Environ).
	Env []string
	// ReadFile reads small local files (os.ReadFile): the config file, the
	// container markers, fault rule files and overlays. Snapshot archives,
	// which may be gigabytes, are streamed from the filesystem instead.
	ReadFile func(path string) ([]byte, error)
	// Version is the build's version, stamped by cmd/understudy.
	Version string
}

// DefaultEndpoint is where the CLI looks for understudy when neither
// --endpoint, UNDERSTUDY_ENDPOINT nor UNDERSTUDY_LISTEN says otherwise.
const DefaultEndpoint = "http://127.0.0.1:4566"

// adminTokenEnv carries the admin token for a server beyond loopback; the
// CLI takes no token flag, which would show in the process list.
const adminTokenEnv = "UNDERSTUDY_ADMIN_TOKEN"

// Run executes the command args name and returns its exit code: 0, 1 for
// an operation that failed, 2 for a usage error, 3 when understudy is not
// reachable or does not answer, 4 when a condition was not met, 5 when the
// server refused the admin token (admin.ExitCode).
func Run(ctx context.Context, args []string, o Options) int {
	return run(ctx, args, o, answerTimeout)
}

// run is Run with answer as the bound on waiting for a prompt call's
// answer, which tests shorten.
func run(ctx context.Context, args []string, o Options, answer time.Duration) int {
	root := tree()
	path, cmd, rest := root.find(args)
	if path == "" && len(rest) > 0 {
		switch rest[0] {
		case "help":
			// understudy help journal is understudy journal --help.
			path, cmd, rest = root.find(rest[1:])
			rest = append([]string{"--help"}, rest...)
		case "--version", "-version", "-v":
			path, cmd, rest = root.find(append([]string{"version"}, rest[1:]...))
		}
	}
	r := &runner{o: o, path: path, answer: answer}
	if helpRequested(rest) {
		return printHelp(cmd, path, o.Stdout, admin.ExitOK)
	}
	switch body := cmd.body.(type) {
	case groupBody:
		if len(rest) == 0 {
			return printHelp(cmd, path, o.Stderr, admin.ExitUsage)
		}
		return r.fail(notACommand(r.command(), cmd, rest[0]))
	case flagLeaf:
		return r.fail(body.exec(ctx, r, rest))
	case rawLeaf:
		return r.fail(body.run(ctx, r, rest))
	}
	// Every command is built by group, leaf or raw, which set its body.
	return r.fail(fmt.Errorf("understudy %s has no body; this is a bug in understudy", path))
}

// notACommand is the usage error for arg where a subcommand of cmd, run
// as line, goes: a flag there is in the wrong place rather than unknown.
func notACommand(line string, cmd *command, arg string) error {
	if strings.HasPrefix(arg, "-") {
		return &usageError{msg: fmt.Sprintf("%s comes before the command; flags go after it, as in '%s <command> %s ...'; the commands are %s",
			arg, line, arg, cmd.subNames())}
	}
	return &usageError{msg: fmt.Sprintf("%q is not a command of %s; the commands are %s", arg, line, cmd.subNames())}
}

// printHelp prints cmd's help to w and returns code.
func printHelp(cmd *command, path string, w io.Writer, code int) int {
	if err := cmd.help(w, path); err != nil {
		return admin.ExitFailed
	}
	return code
}

// helpRequested reports whether args ask for help: help as the first
// argument, or -h or --help before any "--".
func helpRequested(args []string) bool {
	if len(args) > 0 && args[0] == "help" {
		return true
	}
	for _, a := range args {
		switch a {
		case "--":
			return false
		case "-h", "-help", "--help":
			return true
		}
	}
	return false
}

// output is how a command prints its result.
type output uint8

const (
	outputText output = iota
	outputJSON
)

func (o *output) String() string {
	if *o == outputJSON {
		return "json"
	}
	return "text"
}

// Set reads --output.
func (o *output) Set(s string) error {
	switch s {
	case "text":
		*o = outputText
	case "json":
		*o = outputJSON
	default:
		return fmt.Errorf("%q is not text or json", s)
	}
	return nil
}

// runner is one command's run: the options, the parsed common flags and
// the admin client they select.
type runner struct {
	o    Options
	path string
	// answer bounds the wait for the answer to a call sent through client.
	answer   time.Duration
	output   output
	endpoint string
}

// command is the command line the runner runs, such as "understudy faults
// add", or "understudy" at the root.
func (r *runner) command() string {
	if r.path == "" {
		return "understudy"
	}
	return "understudy " + r.path
}

// env looks up name in the environment, empty when unset.
func (r *runner) env(name string) string {
	for _, kv := range r.o.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v
		}
	}
	return ""
}

// endpointURL is --endpoint, else UNDERSTUDY_ENDPOINT, else the address
// UNDERSTUDY_LISTEN gives serve, else the default. Reading the listen
// address lets a command run beside serve, such as the image's
// healthcheck, follow a moved port without a second variable.
func (r *runner) endpointURL() string {
	switch {
	case r.endpoint != "":
		return r.endpoint
	case r.env(config.EndpointEnv) != "":
		return r.env(config.EndpointEnv)
	}
	if u, ok := listenEndpoint(r.env(config.ListenEnv)); ok {
		return u
	}
	return DefaultEndpoint
}

// listenEndpoint is the URL that reaches a server listening on listen, an
// address and port: an unspecified address (0.0.0.0, ::) is reached on
// loopback. It is false when listen is not an address and port, or its
// port is 0, which serve replaces with a port no other process can know.
func listenEndpoint(listen string) (string, bool) {
	a, err := netip.ParseAddrPort(listen)
	if err != nil || a.Port() == 0 {
		return "", false
	}
	host := a.Addr()
	switch {
	case host.IsUnspecified() && host.Is4():
		host = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	case host.IsUnspecified():
		host = netip.IPv6Loopback()
	}
	return "http://" + netip.AddrPortFrom(host, a.Port()).String(), true
}

// Bounds on the admin calls the CLI makes, so a listener that accepts and
// never answers (a wedged server, another program on the port) fails the
// command with exit 3 instead of hanging it.
const (
	// connectTimeout bounds opening a connection.
	connectTimeout = 10 * time.Second
	// answerTimeout bounds the wait for an answer's headers once the
	// request is sent. Calls that may work longer than this before
	// answering (settle, reset, image pulls, snapshots) use slowClient.
	answerTimeout = time.Minute
)

// answerBound is the transport of client's calls: a call sent and not
// answered within the bound fails with a *noAnswerError, since the server
// took the connection and is not down.
type answerBound struct {
	base   http.RoundTripper
	within time.Duration
}

func newAnswerBound(within time.Duration) *answerBound {
	return &answerBound{base: adminTransport(within), within: within}
}

func (t *answerBound) RoundTrip(req *http.Request) (*http.Response, error) {
	var sent atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(w httptrace.WroteRequestInfo) { sent.Store(w.Err == nil) }}
	resp, err := t.base.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if timeout, ok := errors.AsType[net.Error](err); ok && timeout.Timeout() && sent.Load() && req.Context().Err() == nil {
		return nil, &noAnswerError{endpoint: req.URL.Scheme + "://" + req.URL.Host, within: t.within, err: err}
	}
	return resp, err
}

// noAnswerError is an admin call the server at endpoint took but did not
// answer within the CLI's bound: it is busy with a long call, wedged, or
// not understudy. Like an unreachable server it exits 3.
type noAnswerError struct {
	endpoint string
	within   time.Duration
	err      error
}

func (e *noAnswerError) Error() string {
	return fmt.Sprintf("the server at %s took the call but sent no answer within %s; it may still be working on it, it may be wedged, or the program on that port may not be understudy",
		e.endpoint, e.within)
}

func (e *noAnswerError) Unwrap() error { return e.err }

// noAnswerNext is the step to take after a *noAnswerError.
const noAnswerNext = "check the server with 'understudy health' and its log, then run the command again"

// adminTransport is the transport of the CLI's admin calls: Go's default
// transport with connectTimeout, and answer as the response header
// timeout (0 waits as long as the call's context allows).
func adminTransport(answer time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   connectTimeout,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: answer,
	}
}

// client is the admin client for the selected endpoint, for calls the
// server answers promptly: it gives up after r.answer without an answer.
// A streamed answer's body, such as the journal stream's, is not bounded.
func (r *runner) client() (*admin.Client, error) {
	return r.clientWith(&http.Client{Transport: newAnswerBound(r.answer)})
}

// slowClient is the admin client for calls the server may work on for
// longer than answerTimeout before answering; the caller bounds each call
// with its context.
func (r *runner) slowClient() (*admin.Client, error) {
	return r.clientWith(&http.Client{Transport: adminTransport(0)})
}

// clientWith is the admin client for the selected endpoint sending through
// hc.
func (r *runner) clientWith(hc *http.Client) (*admin.Client, error) {
	c, err := admin.New(r.endpointURL(), admin.Options{Token: r.env(adminTokenEnv), HTTPClient: hc})
	if err != nil {
		return nil, &usageError{msg: err.Error()}
	}
	return c, nil
}

// emit prints a result: JSON of v, or what text writes.
func (r *runner) emit(v any, text func(w io.Writer) error) error {
	if r.output == outputJSON {
		enc := json.NewEncoder(r.o.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("writing the result: %w", err)
		}
		return nil
	}
	if err := text(r.o.Stdout); err != nil {
		return fmt.Errorf("writing the result: %w", err)
	}
	return nil
}

// usageError is a command line the CLI cannot run: exit code 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// cliError is an error as --output json prints it to stderr.
type cliError struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Known   []string       `json:"known,omitempty"`
	Pending map[string]int `json:"pending,omitempty"`
	Exit    int            `json:"exit"`
}

// fail prints err to stderr, unless nil, and returns its exit code.
func (r *runner) fail(err error) int {
	code := exitCode(err)
	if err == nil {
		return code
	}
	hint := "run '" + r.command() + " --help' for usage and examples"
	adminErr, isAdmin := errors.AsType[*admin.Error](err)
	if isAdmin && adminErr.Code == admin.Unauthorized {
		adminErr = r.unauthorized(adminErr)
		err = adminErr
	}
	noAnswer, isNoAnswer := errors.AsType[*noAnswerError](err)
	if isNoAnswer {
		// admin wraps it as unreachable, whose text says to start a server
		// that is running.
		err = noAnswer
	}
	local, isLocal := errors.AsType[*localError](err)
	if r.output == outputJSON {
		body := errorBody{Code: "failed", Message: err.Error(), Exit: code}
		var unreachable *admin.UnreachableError
		switch {
		case isAdmin:
			body.Code, body.Message, body.Hint, body.Known, body.Pending = string(adminErr.Code), adminErr.Message, adminErr.Hint, adminErr.Known, adminErr.Pending
		case isNoAnswer:
			body.Code, body.Hint = "unreachable", noAnswerNext
		case errors.As(err, &unreachable):
			body.Code = "unreachable"
		case code == admin.ExitUsage:
			body.Code, body.Hint = "usage", hint
		case isLocal:
			body.Code, body.Hint = "local", local.next
		}
		data, merr := json.Marshal(cliError{Error: body})
		if merr == nil {
			_, merr = fmt.Fprintf(r.o.Stderr, "%s\n", data)
		}
		if merr != nil {
			return admin.ExitFailed
		}
		return code
	}
	msg := r.command() + ": " + errorText(err, adminErr)
	switch {
	case isAdmin && adminErr.Hint != "":
		// The server's hint, or for a refused token the CLI's, already
		// names the next step.
	case isNoAnswer:
		msg += "\n" + sentence(noAnswerNext)
	case code == admin.ExitUsage:
		msg += "\n" + sentence(hint)
	case isLocal && local.next != "":
		msg += "\n" + sentence(local.next)
	}
	if _, werr := fmt.Fprintln(r.o.Stderr, msg); werr != nil {
		return admin.ExitFailed
	}
	return code
}

// unauthorized returns a copy of refused, the server's refusal of an admin
// call for its token, with the step a CLI user takes in place of the
// server's hint, which is written for HTTP clients. Whether the CLI sent a
// token tells a missing token from a rejected one.
func (r *runner) unauthorized(refused *admin.Error) *admin.Error {
	out := *refused
	if r.env(adminTokenEnv) == "" {
		out.Hint = adminTokenEnv + " is not set; set it to the admin token 'understudy serve' was started with"
	} else {
		out.Hint = "the server rejected the token in " + adminTokenEnv + "; set it to the admin token 'understudy serve' was started with"
	}
	return &out
}

// errorText is err as the text form prints it. An admin error, adminErr
// when err is or wraps one, leaves out its machine code, which the exit
// code and --output json carry: its message stands alone.
func errorText(err error, adminErr *admin.Error) string {
	if adminErr == nil {
		return err.Error()
	}
	full := adminErr.Error()
	return strings.Replace(err.Error(), full, strings.TrimPrefix(full, string(adminErr.Code)+": "), 1)
}

// sentence capitalizes s and ends it with a period; an empty s stays empty.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// localError is a failure of the CLI's own I/O, such as a missing rule
// file, an archive that cannot be written or an address serve cannot
// listen on: exit 1, with the next step to take, which --output json
// prints as the hint.
type localError struct {
	// op says what failed, such as "reading the fault rule file x.json".
	op   string
	err  error
	next string
}

func (e *localError) Error() string { return e.op + ": " + e.err.Error() }
func (e *localError) Unwrap() error { return e.err }

// exitCode maps an error to the CLI's exit code: usage errors and flag
// errors are 2, the rest follow admin.ExitCode.
func exitCode(err error) int {
	var usage *usageError
	switch {
	case err == nil:
		return admin.ExitOK
	case errors.As(err, &usage), errors.Is(err, flag.ErrHelp):
		return admin.ExitUsage
	default:
		return admin.ExitCode(err)
	}
}
