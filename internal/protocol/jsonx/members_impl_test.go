package jsonx_test

import (
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// AppendMembers joins an object's members to an open hand-written object:
// with a comma only between members, and nothing for an empty object.
func TestAppendMembers(t *testing.T) {
	for _, c := range []struct {
		name, b, obj, want string
	}{
		{"into an empty object", `{`, `{"a":1,"b":[{}]}`, `{"a":1,"b":[{}]`},
		{"after a member", `{"__type":"X"`, `{"a":1}`, `{"__type":"X","a":1`},
		{"an empty object", `{"__type":"X"`, `{}`, `{"__type":"X"`},
	} {
		got, err := jsonx.AppendMembers([]byte(c.b), []byte(c.obj))
		if err != nil || string(got) != c.want {
			t.Errorf("%s: AppendMembers(%s, %s) = %s, %v; want %s", c.name, c.b, c.obj, got, err, c.want)
		}
	}
}

// A value that is not an object has no members to join: a usage error,
// with the document unchanged.
func TestAppendMembersOfANonObject(t *testing.T) {
	for _, obj := range []string{`[1]`, `"a"`, `{`, ``} {
		got, err := jsonx.AppendMembers([]byte(`{"a":1`), []byte(obj))
		var usage *jsonx.UsageError
		if !errors.As(err, &usage) || string(got) != `{"a":1` {
			t.Errorf("AppendMembers(%q) = %s, %v; want a *UsageError and the document unchanged", obj, got, err)
		}
	}
}
