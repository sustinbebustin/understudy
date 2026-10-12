package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

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
// --endpoint nor UNDERSTUDY_ENDPOINT says otherwise.
const DefaultEndpoint = "http://127.0.0.1:4566"

// adminTokenEnv carries the admin token for a server beyond loopback; the
// CLI takes no token flag, which would show in the process list.
const adminTokenEnv = "UNDERSTUDY_ADMIN_TOKEN"

// Run executes the command args name and returns its exit code: 0, 1 for
// an operation that failed, 2 for a usage error, 3 when understudy is not
// reachable, 4 when a condition was not met (admin.ExitCode).
func Run(ctx context.Context, args []string, o Options) int {
	root := tree()
	path, cmd, rest := root.find(args)
	if path == "" && len(rest) > 0 && rest[0] == "help" {
		// understudy help journal is understudy journal --help.
		path, cmd, rest = root.find(rest[1:])
		rest = append([]string{"--help"}, rest...)
	}
	r := &runner{o: o, path: path}
	switch {
	case helpRequested(rest):
		return printHelp(cmd, path, o.Stdout, admin.ExitOK)
	case !cmd.action && len(rest) == 0:
		return printHelp(cmd, path, o.Stderr, admin.ExitUsage)
	case !cmd.action:
		return r.fail(&usageError{msg: fmt.Sprintf("%q is not a command of %s; the commands are %s", rest[0], r.command(), cmd.subNames())})
	}
	return r.fail(cmd.exec(ctx, r, rest))
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
	o        Options
	path     string
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

// endpointURL is --endpoint, else UNDERSTUDY_ENDPOINT, else the default.
func (r *runner) endpointURL() string {
	switch {
	case r.endpoint != "":
		return r.endpoint
	case r.env(config.EndpointEnv) != "":
		return r.env(config.EndpointEnv)
	default:
		return DefaultEndpoint
	}
}

// client is the admin client for the selected endpoint.
func (r *runner) client() (*admin.Client, error) { return r.clientWith(nil) }

// clientWith is the admin client for the selected endpoint sending through
// hc; nil is http.DefaultClient.
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
	local, isLocal := errors.AsType[*localError](err)
	if r.output == outputJSON {
		body := errorBody{Code: "failed", Message: err.Error(), Exit: code}
		var adminErr *admin.Error
		var unreachable *admin.UnreachableError
		switch {
		case errors.As(err, &adminErr):
			body.Code, body.Message, body.Hint, body.Known, body.Pending = string(adminErr.Code), adminErr.Message, adminErr.Hint, adminErr.Known, adminErr.Pending
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
	msg := r.command() + ": " + err.Error()
	switch {
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

// sentence capitalizes s and ends it with a period; an empty s stays empty.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// localError is a failure of the CLI's own file I/O, such as a missing
// rule file or an archive that cannot be written: exit 1, with the next
// step to take, which --output json prints as the hint.
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
