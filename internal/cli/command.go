package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
)

// action runs a command with its positional arguments, after its flags
// are parsed.
type action func(ctx context.Context, r *runner, args []string) error

// command is a node of the command tree: a group of subcommands, or a leaf
// whose setup registers its flags and returns its action.
type command struct {
	name     string
	summary  string
	usage    string
	examples []string
	sub      []*command
	setup    func(fs *flag.FlagSet) action
	// raw leaves receive their arguments unparsed, with their own help
	// text; serve's flags belong to the config package.
	raw      func(ctx context.Context, r *runner, args []string) error
	rawFlags func(w io.Writer) error
	// action marks a runnable leaf.
	action bool
}

// leaf builds a command with flags.
func leaf(name, summary, usage string, examples []string, setup func(fs *flag.FlagSet) action) *command {
	return &command{name: name, summary: summary, usage: usage, examples: examples, setup: setup, action: true}
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
	return &command{name: name, summary: summary, usage: name + " " + strings.Join(names, "|") + " ...", sub: sub, examples: examples}
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
	fs.Var(&r.output, "output", "result format: text or json")
	fs.StringVar(&r.endpoint, "endpoint", "", "understudy's base URL (default UNDERSTUDY_ENDPOINT, else "+DefaultEndpoint+")")
}

// exec parses the leaf's flags, which may follow its positional
// arguments, and runs it.
func (c *command) exec(ctx context.Context, r *runner, args []string) error {
	if c.raw != nil {
		return c.raw(ctx, r, args)
	}
	fs := flag.NewFlagSet(r.path, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	commonFlags(fs, r)
	act := c.setup(fs)
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return &usageError{msg: err.Error()}
	}
	return act(ctx, r, positional)
}

// parseInterspersed parses flags anywhere among args and returns the
// positional arguments; everything after "--" is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if len(args) > len(rest) && args[len(args)-len(rest)-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
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
	if c.action {
		b.WriteString("\nFlags:\n")
		if err := c.writeFlags(&b); err != nil {
			return err
		}
	}
	if len(c.examples) > 0 {
		b.WriteString("\nExamples:\n")
		for _, e := range c.examples {
			b.WriteString("  " + e + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (c *command) writeFlags(w io.Writer) error {
	if c.rawFlags != nil {
		return c.rawFlags(w)
	}
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	commonFlags(fs, &runner{})
	c.setup(fs)
	fs.SetOutput(w)
	fs.PrintDefaults()
	return nil
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
	if len(args) != n {
		return &usageError{msg: fmt.Sprintf("takes %s; got %d argument(s) %q", what, len(args), args)}
	}
	return nil
}
