package jsonx_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// messageState is a sealed sum shaped like the ones services persist.
type messageState interface{ isMessageState() }

type visible struct{}

type inFlight struct {
	Receipt  string `json:"receipt"`
	Deadline int64  `json:"deadline"`
	// Previous nests the sum inside one of its own variants.
	Previous messageState `json:"previous"`
}

type delayed struct {
	Until int64 `json:"until"`
}

func (visible) isMessageState()  {}
func (inFlight) isMessageState() {}
func (delayed) isMessageState()  {}

// origin is a second sum, so one Codec carries two tables.
type origin interface{ isOrigin() }

type fromClient struct {
	Addr string `json:"addr"`
}

type fromTimer struct{}

func (fromClient) isOrigin() {}
func (fromTimer) isOrigin()  {}

type message struct {
	ID      string                  `json:"id"`
	State   messageState            `json:"state"`
	Origin  origin                  `json:"origin"`
	History []messageState          `json:"history"`
	ByGroup map[string]messageState `json:"byGroup"`
	// Last holds a variant type directly, not through the sum.
	Last inFlight `json:"last"`
}

// isUnionError reports whether err is a *jsonx.UnionError that names a sum.
func isUnionError(err error) bool {
	uerr, ok := errors.AsType[*jsonx.UnionError](err)
	return ok && uerr.Sum != ""
}

func testCodec(t *testing.T) jsonx.Codec {
	t.Helper()
	states, err := jsonx.NewUnion[messageState]("messageState",
		jsonx.Variant[messageState, visible]("visible"),
		jsonx.Variant[messageState, inFlight]("inFlight"),
		jsonx.Variant[messageState, delayed]("delayed"),
	)
	if err != nil {
		t.Fatal(err)
	}
	origins, err := jsonx.NewUnion[origin]("origin",
		jsonx.Variant[origin, fromClient]("client"),
		jsonx.Variant[origin, fromTimer]("timer"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return jsonx.NewCodec(states, origins)
}

func TestUnionWritesTheKindConventionWithSortedKeys(t *testing.T) {
	c := testCodec(t)
	m := message{
		ID:      "m1",
		State:   inFlight{Receipt: "r", Deadline: 30, Previous: delayed{Until: 9}},
		Origin:  fromTimer{},
		History: []messageState{visible{}},
	}
	got, err := c.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"byGroup":{},"history":[{"kind":"visible"}],"id":"m1",` +
		`"last":{"deadline":0,"kind":"inFlight","previous":null,"receipt":""},` +
		`"origin":{"kind":"timer"},` +
		`"state":{"deadline":30,"kind":"inFlight","previous":{"kind":"delayed","until":9},"receipt":"r"}}`
	if string(got) != want {
		t.Errorf("Marshal =\n%s\nwant\n%s", got, want)
	}
}

func drawState(t *rapid.T, depth int) messageState {
	choices := 3
	if depth == 0 {
		choices = 2
	}
	switch rapid.IntRange(0, choices-1).Draw(t, "variant") {
	case 0:
		return visible{}
	case 1:
		return delayed{Until: rapid.Int64().Draw(t, "until")}
	default:
		var prev messageState
		if rapid.Bool().Draw(t, "hasPrevious") {
			prev = drawState(t, depth-1)
		}
		return inFlight{Receipt: rapid.String().Draw(t, "receipt"), Deadline: rapid.Int64().Draw(t, "deadline"), Previous: prev}
	}
}

func TestUnionRoundTrips(t *testing.T) {
	c := testCodec(t)
	rapid.Check(t, func(t *rapid.T) {
		m := message{
			ID:      rapid.String().Draw(t, "id"),
			History: []messageState{},
			ByGroup: map[string]messageState{},
		}
		if rapid.Bool().Draw(t, "hasState") {
			m.State = drawState(t, 2)
		}
		if rapid.Bool().Draw(t, "fromClient") {
			m.Origin = fromClient{Addr: rapid.String().Draw(t, "addr")}
		}
		for range rapid.IntRange(0, 3).Draw(t, "history") {
			m.History = append(m.History, drawState(t, 1))
		}
		for _, k := range rapid.SliceOfDistinct(rapid.String(), func(s string) string { return s }).Draw(t, "groups") {
			m.ByGroup[k] = drawState(t, 1)
		}
		if last, ok := drawState(t, 2).(inFlight); ok {
			m.Last = last
		}
		data, err := c.Marshal(m)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var back message
		if err := c.Unmarshal(data, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if !reflect.DeepEqual(back, m) {
			t.Fatalf("round trip changed the value:\n got %#v\nwant %#v\njson %s", back, m, data)
		}
		again, err := c.Marshal(back)
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("second Marshal = %s, %v; want the same bytes %s", again, err, data)
		}
	})
}

func TestUnionRejects(t *testing.T) {
	c := testCodec(t)
	for name, in := range map[string]string{
		"unknown kind":           `{"state":{"kind":"gone"}}`,
		"missing kind":           `{"state":{"until":1}}`,
		"kind is not a string":   `{"state":{"kind":1}}`,
		"not an object":          `{"state":"visible"}`,
		"wrong kind for a field": `{"last":{"kind":"delayed","until":1}}`,
		"unknown member":         `{"state":{"kind":"delayed","until":1,"extra":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var m message
			if err := c.Unmarshal([]byte(in), &m); err == nil {
				t.Errorf("Unmarshal(%s) succeeded: %#v", in, m)
			}
		})
	}
	// A *delayed written as "delayed" would read back as a delayed value,
	// changing the type behind the sum across a save and load.
	for name, state := range map[string]messageState{
		"pointer variant":     &delayed{Until: 3},
		"nil pointer variant": (*delayed)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			if out, err := c.Marshal(message{State: state}); !isUnionError(err) {
				t.Errorf("Marshal = %s, %v, want a *UnionError", out, err)
			}
		})
	}
	t.Run("undeclared variant", func(t *testing.T) {
		states, err := jsonx.NewUnion[messageState]("messageState", jsonx.Variant[messageState, visible]("visible"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = jsonx.NewCodec(states).Marshal(message{State: delayed{}})
		if !isUnionError(err) {
			t.Errorf("Marshal error = %v, want a *UnionError", err)
		}
	})
}

type kindField struct {
	Kind string `json:"kind"`
}

func (kindField) isMessageState() {}

type pointerOnly struct{}

func (*pointerOnly) isMessageState() {}

func TestNewUnionRejectsBadTables(t *testing.T) {
	tests := map[string]func() error{
		"sum is not an interface": func() error {
			_, err := jsonx.NewUnion[visible]("visible", jsonx.Variant[visible, visible]("v"))
			return err
		},
		"empty kind": func() error {
			_, err := jsonx.NewUnion[messageState]("s", jsonx.Variant[messageState, visible](""))
			return err
		},
		"not a struct": func() error {
			_, err := jsonx.NewUnion[messageState]("s", jsonx.Variant[messageState, *pointerOnly]("p"))
			return err
		},
		"does not implement the sum": func() error {
			_, err := jsonx.NewUnion[messageState]("s", jsonx.Variant[messageState, fromTimer]("t"))
			return err
		},
		"duplicate kind": func() error {
			_, err := jsonx.NewUnion[messageState]("s", jsonx.Variant[messageState, visible]("v"), jsonx.Variant[messageState, delayed]("v"))
			return err
		},
		"duplicate type": func() error {
			_, err := jsonx.NewUnion[messageState]("s", jsonx.Variant[messageState, visible]("v"), jsonx.Variant[messageState, visible]("w"))
			return err
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			if !isUnionError(build()) {
				t.Error("NewUnion succeeded or failed with another error type, want a *UnionError")
			}
		})
	}
	t.Run("variant with a kind field", func(t *testing.T) {
		states, err := jsonx.NewUnion[messageState]("s", jsonx.Variant[messageState, kindField]("k"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = jsonx.NewCodec(states).Marshal(message{State: kindField{Kind: "x"}})
		if !isUnionError(err) {
			t.Errorf("Marshal error = %v, want a *UnionError naming the reserved member", err)
		}
	})
}

func TestCodecIsStrict(t *testing.T) {
	type plain struct {
		A int `json:"a"`
	}
	for _, in := range []string{`{"a":1,"b":2}`, `{"a":1,"a":2}`, "{\"a\":1,\"\xff\":2}"} {
		var p plain
		if err := jsonx.Unmarshal([]byte(in), &p); err == nil {
			t.Errorf("Unmarshal(%q) succeeded, want an error", in)
		}
	}
	got, err := jsonx.Marshal(map[string]plain{"b": {A: 2}, "a": {A: 1}})
	if err != nil || string(got) != `{"a":{"a":1},"b":{"a":2}}` {
		t.Errorf("Marshal = %s, %v", got, err)
	}
}
