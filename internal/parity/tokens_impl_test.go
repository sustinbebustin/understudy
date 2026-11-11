package parity

import (
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// prefixLen is how many leading characters of a task token the shape test
// matches on; enough to be specific, short enough to survive the token
// format changing after its first few bytes.
const prefixLen = 16

// The repository is public, so no capture under testdata/parity/sfn holds a
// whole task token: each is redacted to <task-token>. token-shape.txt keeps
// the length (first line) and leading characters (second line) that
// sfn.task-token.format cites, and is itself too short to match.
func TestSFNCapturesHoldNoTaskToken(t *testing.T) {
	t.Parallel()
	fsys := recordings()
	shape, err := fs.ReadFile(fsys, "sfn/SFN-02/token-shape.txt")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(shape), "\n")
	if len(lines) < 2 {
		t.Fatalf("token-shape.txt has %d lines; want the token length, then its first characters", len(lines))
	}
	length, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		t.Fatalf("token-shape.txt line 1 is not a token length: %v", err)
	}
	if len(lines[1]) < prefixLen || length < prefixLen {
		t.Fatalf("token-shape.txt records length %d and %d leading characters; need at least %d of each", length, len(lines[1]), prefixLen)
	}
	prefix := lines[1][:prefixLen]
	token := regexp.MustCompile(regexp.QuoteMeta(prefix) + `[A-Za-z0-9+/=]{` + strconv.Itoa(length-prefixLen) + `,}`)

	scanned := 0
	err = fs.WalkDir(fsys, "sfn", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		scanned++
		if token.Match(data) {
			t.Errorf("testdata/parity/%s holds a task token (%q... of length %d); replace it with <task-token>", p, prefix, length)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("no file under testdata/parity/sfn; the check proves nothing")
	}
}
