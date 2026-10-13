package cli

import (
	"strings"
	"testing"
)

// Every command in the tree, including ones added later, has a body and
// prints its help: a group lists its subcommands and no flags, a leaf
// lists its flags.
func TestEveryCommandHasABodyAndHelp(t *testing.T) {
	var walk func(path string, c *command)
	walk = func(path string, c *command) {
		var b strings.Builder
		if err := c.help(&b, path); err != nil {
			t.Errorf("%q help: %v", path, err)
			return
		}
		help := b.String()
		switch c.body.(type) {
		case groupBody:
			if len(c.sub) == 0 || !strings.Contains(help, "Commands:") || strings.Contains(help, "Flags:") {
				t.Errorf("group %q: %d subcommands, help %q", path, len(c.sub), help)
			}
		case flagLeaf, rawLeaf:
			if !strings.Contains(help, "Flags:\n  --") {
				t.Errorf("leaf %q help lists no flags: %q", path, help)
			}
		case nil:
			t.Errorf("command %q has no body; build it with group, leaf or raw", path)
		}
		for _, s := range c.sub {
			walk(strings.TrimSpace(path+" "+s.name), s)
		}
	}
	walk("", tree())
}
