package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/config"
)

// action runs a command with its positional arguments, after its flags
// are parsed.
type action func(ctx context.Context, r *runner, args []string) error

// command is a node of the command tree. Its body says what it does when
// it is the deepest command named; any command may also have subcommands,
// as journal has trace.
type command struct {
	name     string
	summary  string
	usage    string
	examples []string
	sub      []*command
	body     body
	// epilog ends the help, after the examples; the root's lists the exit
	// codes.
	epilog string
}

// body is what a command does when named: a group runs nothing, a
// flagLeaf parses its flags and runs, a rawLeaf runs on its arguments
// unparsed.
//
//sumtype:decl
type body interface {
	isBody()
}

// groupBody is a command that only lists its subcommands.
type groupBody struct{}

// flagLeaf is a runnable command whose setup registers its flags and
// returns its action.
type flagLeaf struct {
	setup func(fs *flag.FlagSet) action
}

// rawLeaf is a runnable command that receives its arguments unparsed and
// prints its own flag help; serve's flags belong to the config package.
type rawLeaf struct {
	run   action
	flags func(w io.Writer) error
}

func (groupBody) isBody() {}
func (flagLeaf) isBody()  {}
func (rawLeaf) isBody()   {}

// leaf builds a command with flags.
func leaf(name, summary, usage string, examples []string, setup func(fs *flag.FlagSet) action) *command {
	return &command{name: name, summary: summary, usage: usage, examples: examples, body: flagLeaf{setup: setup}}
}

// raw builds a command that parses its own arguments; flags prints the
// flag section of its help.
func raw(name, summary, usage string, examples []string, run action, flags func(w io.Writer) error) *command {
	return &command{name: name, summary: summary, usage: usage, examples: examples, body: rawLeaf{run: run, flags: flags}}
}

// group builds a command of subcommands; its examples are the first of
// each subcommand's.
func group(name, summary string, sub ...*command) *command {
	names := make([]string, len(sub))
	var examples []string
	for i, s := range sub {
		names[i] = s.name
		if len(s.examples) > 0 {
			examples = append(examples, s.examples[0])
		}
	}
	return &command{name: name, summary: summary, usage: name + " " + strings.Join(names, "|") + " ...", sub: sub, examples: examples, body: groupBody{}}
}

// find walks args down the tree to the deepest command they name, and
// returns its path, the command and the arguments after its name.
func (c *command) find(args []string) (path string, cmd *command, rest []string) {
	var names []string
	cmd = c
	for len(args) > 0 && len(cmd.sub) > 0 {
		next := cmd.child(args[0])
		if next == nil {
			break
		}
		names = append(names, next.name)
		cmd, args = next, args[1:]
	}
	return strings.Join(names, " "), cmd, args
}

func (c *command) child(name string) *command {
	for _, s := range c.sub {
		if s.name == name {
			return s
		}
	}
	return nil
}

func (c *command) subNames() string {
	names := make([]string, len(c.sub))
	for i, s := range c.sub {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}

// commonFlags registers the flags every command takes.
func commonFlags(fs *flag.FlagSet, r *runner) {
	fs.Var(&r.output, "output", "the `FORMAT` of results: text or json")
	fs.StringVar(&r.endpoint, "endpoint", "", "understudy's base `URL` (default UNDERSTUDY_ENDPOINT, else the port of UNDERSTUDY_LISTEN on loopback, else "+DefaultEndpoint+")")
}

// exec parses the leaf's flags, which may follow its positional
// arguments, and runs it.
func (l flagLeaf) exec(ctx context.Context, r *runner, args []string) error {
	fs := flag.NewFlagSet(r.path, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	commonFlags(fs, r)
	act := l.setup(fs)
	positional, err := parseInterspersed(fs, r.command(), args)
	if err != nil {
		return err
	}
	return act(ctx, r, positional)
}

// flagValueError is a flag value that does not parse as its flag's type.
type flagValueError struct {
	flag, value string
	// want is the form the flag takes, such as "a duration such as 10m".
	want string
}

func (e *flagValueError) Error() string {
	return fmt.Sprintf("--%s %q is not %s", e.flag, e.value, e.want)
}

// typedValue is a flag of type T whose parse error names the flag and the
// form it takes, and whose help shows placeholder.
type typedValue[T any] struct {
	name, placeholder, want string
	dst                     *T
	parse                   func(string) (T, error)
	format                  func(T) string
}

func (v *typedValue[T]) String() string {
	if v.dst == nil {
		return ""
	}
	return v.format(*v.dst)
}

// Set parses s; a refusal names the flag and the form it takes.
func (v *typedValue[T]) Set(s string) error {
	x, err := v.parse(s)
	if err != nil {
		return &flagValueError{flag: v.name, value: s, want: v.want}
	}
	*v.dst = x
	return nil
}

func (v *typedValue[T]) helpPlaceholder() string { return v.placeholder }

func typedVar[T any](fs *flag.FlagSet, v *typedValue[T], def T, usage string) {
	*v.dst = def
	fs.Var(v, v.name, usage)
}

// durationVar registers a duration flag such as --since 10m.
func durationVar(fs *flag.FlagSet, p *time.Duration, name string, def time.Duration, usage string) {
	typedVar(fs, &typedValue[time.Duration]{
		name: name, placeholder: "DURATION", want: "a duration such as 90s, 10m or 1h30m", dst: p,
		parse: time.ParseDuration, format: time.Duration.String,
	}, def, usage)
}

// intVar registers a whole-number flag such as --limit 100.
func intVar(fs *flag.FlagSet, p *int, name string, def int, usage string) {
	typedVar(fs, &typedValue[int]{
		name: name, placeholder: "N", want: "a whole number such as 100", dst: p,
		parse: strconv.Atoi, format: strconv.Itoa,
	}, def, usage)
}

// eventIDVar registers a journal event id flag such as --trace 42.
func eventIDVar(fs *flag.FlagSet, p *uint64, name, usage string) {
	typedVar(fs, &typedValue[uint64]{
		name: name, placeholder: "ID", want: "an event id, a positive whole number such as 42", dst: p,
		parse: func(s string) (uint64, error) {
			id, err := strconv.ParseUint(s, 10, 64)
			if err == nil && id == 0 {
				// Event ids start at 1; 0 is the flag left unset.
				err = strconv.ErrRange
			}
			return id, err
		},
		format: func(n uint64) string { return strconv.FormatUint(n, 10) },
	}, 0, usage)
}

// fractionVar registers a number flag such as --probability 0.25.
func fractionVar(fs *flag.FlagSet, p *float64, name, usage string) {
	typedVar(fs, &typedValue[float64]{
		name: name, placeholder: "P", want: "a number such as 0.25", dst: p,
		parse:  func(s string) (float64, error) { return strconv.ParseFloat(s, 64) },
		format: func(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) },
	}, 0, usage)
}

// parseInterspersed sets fs's flags from anywhere among args, the command
// line's, and returns the positional arguments. A flag is -name or --name,
// its value after = or in the next argument; a switch takes no next
// argument. Everything after "--" is positional, and so is a negative
// number such as -5s, which no flag name starts with, so a command can
// refuse it for what it is. Errors are usage errors naming the flag as
// --name, an unknown one with the closest name line takes.
func parseInterspersed(fs *flag.FlagSet, line string, args []string) ([]string, error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(positional, args[i+1:]...), nil
		}
		if !isFlag(a) {
			positional = append(positional, a)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			return nil, unknownFlag(fs, line, name)
		}
		switch {
		case hasValue:
		case isSwitch(f):
			value = "true"
		case i+1 < len(args):
			i++
			value = args[i]
		default:
			return nil, &usageError{msg: fmt.Sprintf("--%s needs a value: %s", name, f.Usage)}
		}
		if err := fs.Set(name, value); err != nil {
			return nil, refusedFlag(f, value, err)
		}
	}
	return positional, nil
}

// isFlag reports whether a command-line argument is a flag: a dash and a
// name, where a name never starts with a digit or a dot.
func isFlag(arg string) bool {
	return len(arg) > 1 && arg[0] == '-' && !strings.ContainsRune("0123456789.", rune(arg[1]))
}

// isSwitch reports whether f takes no value, as --follow does.
func isSwitch(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// unknownFlag is the usage error for --name, which line does not take.
func unknownFlag(fs *flag.FlagSet, line, name string) error {
	var names []string
	fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	msg := fmt.Sprintf("--%s is not a flag of %s", name, line)
	if c := config.Closest(name, names); c != "" {
		msg += "; the closest valid name is --" + c
	}
	return &usageError{msg: msg}
}

// refusedFlag is the usage error for value, which f refused with err.
func refusedFlag(f *flag.Flag, value string, err error) error {
	if refused, ok := errors.AsType[*flagValueError](err); ok {
		return &usageError{msg: refused.Error()}
	}
	if isSwitch(f) {
		return &usageError{msg: fmt.Sprintf("--%s takes no value, or true or false; got %q", f.Name, value)}
	}
	return &usageError{msg: fmt.Sprintf("--%s: %v", f.Name, err)}
}

// help prints the command's summary, usage, subcommands or flags, and
// examples.
func (c *command) help(w io.Writer, path string) error {
	var b strings.Builder
	b.WriteString(c.summary + "\n\nUsage:\n  understudy " + c.usage + "\n")
	if len(c.sub) > 0 {
		b.WriteString("\nCommands:\n")
		for _, s := range c.sub {
			fmt.Fprintf(&b, "  %-12s %s\n", s.name, s.summary)
		}
		at := path
		if at != "" {
			at += " "
		}
		b.WriteString("\nRun 'understudy " + at + "<command> --help' for a command's flags and examples.\n")
	}
	var flags func(w io.Writer) error
	switch body := c.body.(type) {
	case groupBody:
	case flagLeaf:
		flags = body.writeFlags
	case rawLeaf:
		flags = body.flags
	}
	if flags != nil {
		b.WriteString("\nFlags:\n")
		if err := flags(&b); err != nil {
			return err
		}
	}
	if len(c.examples) > 0 {
		b.WriteString("\nExamples:\n")
		for _, e := range c.examples {
			b.WriteString("  " + e + "\n")
		}
	}
	if c.epilog != "" {
		b.WriteString("\n" + c.epilog)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeFlags prints the leaf's flags, the common ones first, with their
// placeholders, usage and defaults.
func (l flagLeaf) writeFlags(w io.Writer) error {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	commonFlags(fs, &runner{})
	l.setup(fs)
	var b strings.Builder
	fs.VisitAll(func(f *flag.Flag) {
		name, usage := flag.UnquoteUsage(f)
		b.WriteString("  --" + f.Name)
		if ph := placeholder(f, name); ph != "" {
			b.WriteString(" " + ph)
		}
		b.WriteString("\n      " + usage)
		if !slices.Contains([]string{"", "0", "0s", "false"}, f.DefValue) {
			b.WriteString(" (default " + f.DefValue + ")")
		}
		b.WriteString("\n")
	})
	_, err := io.WriteString(w, b.String())
	return err
}

// placeholder is the value placeholder help shows after a flag: none for
// a switch, the typed value's own, else the backquoted word of its usage
// or its type in capitals.
func placeholder(f *flag.Flag, typeName string) string {
	if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
		return ""
	}
	if p, ok := f.Value.(interface{ helpPlaceholder() string }); ok {
		return p.helpPlaceholder()
	}
	if typeName == "" || typeName == "value" || typeName == "string" {
		return "VALUE"
	}
	return strings.ToUpper(typeName)
}

// stringList is a flag that may repeat.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

// Set appends one value.
func (l *stringList) Set(s string) error {
	*l = append(*l, s)
	return nil
}

// wantArgs checks the number of positional arguments.
func wantArgs(args []string, n int, what string) error {
	if len(args) == n {
		return nil
	}
	got := "none"
	if len(args) > 0 {
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = strconv.Quote(a)
		}
		got = strings.Join(quoted, ", ")
	}
	return &usageError{msg: fmt.Sprintf("takes %s; got %s", what, got)}
}
