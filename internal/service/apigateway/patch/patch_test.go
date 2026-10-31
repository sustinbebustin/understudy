package patch_test

import (
	"maps"
	"net/http"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/patch"
)

// A patch path is a JSON Pointer: "Any slash ("/") character appearing in
// path names must be escaped with "~1"" ([S] PatchOperation$path,
// apigateway-rest.md 3.8), and "~0" is "~" per JSON Pointer.
func TestSegments(t *testing.T) {
	for in, want := range map[string][]string{
		"/a/b~1c~0d":                       {"a", "b/c~d"},
		"/binaryMediaTypes/image~1png":     {"binaryMediaTypes", "image/png"},
		"/name":                            {"name"},
		"/~1":                              {"/"},
		"/~01":                             {"~1"},
		"/requestModels/application~1json": {"requestModels", "application/json"},
	} {
		if got := patch.Segments(in); !slices.Equal(got, want) {
			t.Errorf("Segments(%q) = %q, want %q", in, got, want)
		}
	}
	if got := patch.Segments(""); got != nil {
		t.Errorf("Segments(\"\") = %q, want nil", got)
	}
}

// FromModel keeps each operation as sent: op, path ("" when absent),
// value and from.
func TestFromModel(t *testing.T) {
	op := apigatewayapi.OpReplace
	got := patch.FromModel([]apigatewayapi.PatchOperation{
		{Op: &op, Path: new("/name"), Value: new("n")},
		{Op: new(apigatewayapi.OpCopy), From: new("/a")},
	})
	if len(got) != 2 {
		t.Fatalf("FromModel = %+v, want two operations", got)
	}
	if got[0].Op != apigatewayapi.OpReplace || got[0].Path != "/name" || got[0].Value == nil || *got[0].Value != "n" || got[0].From != nil {
		t.Errorf("first = %+v, want replace /name value n", got[0])
	}
	if got[1].Op != apigatewayapi.OpCopy || got[1].Path != "" || got[1].Value != nil || got[1].From == nil || *got[1].From != "/a" {
		t.Errorf("second = %+v, want copy from /a with no path", got[1])
	}
}

// The recorded refusal texts (aws-recorded, localstack@8b9a79f
// tests/aws/services/apigateway/test_apigateway_api.snapshot.json, lines
// beside each case; apigateway-rest.md 3.8).
func TestRecordedMessages(t *testing.T) {
	add := patch.Operation{Op: apigatewayapi.OpAdd, Path: "/binaryMediaTypes"}
	for name, c := range map[string]struct{ got, want string }{
		"L507 InvalidPath":           {patch.InvalidPath(add), "Invalid patch path /binaryMediaTypes"},
		"InvalidPath without a path": {patch.InvalidPath(patch.Operation{Op: apigatewayapi.OpAdd}), "Invalid patch path null"},
		"L689 MustBeOneOf": {
			patch.MustBeOneOf("/parentId", "/pathPart")(patch.Operation{Op: apigatewayapi.OpReplace, Path: "/invalid"}),
			"Invalid patch path  '/invalid' specified for op 'replace'. Must be one of: [/parentId, /pathPart]",
		},
		"L792 ChooseSupported": {
			patch.ChooseSupported(patch.Operation{Op: apigatewayapi.OpRemove, Path: "/pathPart"}),
			"Invalid patch path  '/pathPart' specified for op 'remove'. Please choose supported operations",
		},
		"L468 OpsOneOf": {
			patch.OpsOneOf(apigatewayapi.OpReplace)(patch.Operation{Op: apigatewayapi.OpAdd, Path: "/minimumCompressionSize"}),
			"Invalid patch operation specified. Must be one of: [replace]",
		},
		"L4774 OpsAddRemoveReplace": {
			patch.OpsAddRemoveReplace(patch.Operation{Op: apigatewayapi.OpMove, Path: "/endpointConfiguration/types/0"}),
			"Invalid patch operation specified. Must be 'add'|'remove'|'replace'",
		},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q\nwant %q", name, c.got, c.want)
		}
	}
}

// doc is a toy resource for the table machinery.
type doc struct {
	Name string
	Tags map[string]string
	List []string
	Deep []string
}

func cloneDoc(d doc) doc {
	return doc{Name: d.Name, Tags: maps.Clone(d.Tags), List: slices.Clone(d.List), Deep: slices.Clone(d.Deep)}
}

func equalDoc(a, b doc) bool {
	return a.Name == b.Name && maps.Equal(a.Tags, b.Tags) && slices.Equal(a.List, b.List) && slices.Equal(a.Deep, b.Deep)
}

const (
	unknownText = "unknown path"
	tableOpText = "table op text"
	ruleOpText  = "rule op text"
	failText    = "rule refused"
)

func value(op patch.Operation) string {
	if op.Value == nil {
		return ""
	}
	return *op.Value
}

// table is a toy path table: replace /name, add/replace/remove /tags/*,
// add /list/* (no value needed, the unescaped segment is appended), add
// /deep/** (the wild segments appended), replace /fail (always refused by
// its Apply).
func table() patch.Table[doc] {
	return patch.Table[doc]{
		Rules: []patch.Rule[doc]{
			{Path: "/name", Ops: []apigatewayapi.Op{apigatewayapi.OpReplace}, Apply: func(d *doc, op patch.Operation, _ []string) *awserr.Error {
				d.Name = value(op)
				return nil
			}},
			{Path: "/tags/*", Ops: []apigatewayapi.Op{apigatewayapi.OpAdd, apigatewayapi.OpReplace, apigatewayapi.OpRemove}, OpText: func(patch.Operation) string { return ruleOpText }, Apply: func(d *doc, op patch.Operation, wild []string) *awserr.Error {
				if d.Tags == nil {
					d.Tags = map[string]string{}
				}
				if op.Op == apigatewayapi.OpRemove {
					delete(d.Tags, wild[0])
				} else {
					d.Tags[wild[0]] = value(op)
				}
				return nil
			}},
			{Path: "/list/*", Ops: []apigatewayapi.Op{apigatewayapi.OpAdd}, Apply: func(d *doc, _ patch.Operation, wild []string) *awserr.Error {
				d.List = append(d.List, wild[0])
				return nil
			}},
			{Path: "/deep/**", Ops: []apigatewayapi.Op{apigatewayapi.OpAdd}, Apply: func(d *doc, _ patch.Operation, wild []string) *awserr.Error {
				d.Deep = append(d.Deep, wild...)
				return nil
			}},
			{Path: "/fail", Ops: []apigatewayapi.Op{apigatewayapi.OpReplace}, Apply: func(*doc, patch.Operation, []string) *awserr.Error {
				return apigatewayapi.ErrBadRequestException(failText)
			}},
		},
		UnknownPath: func(patch.Operation) string { return unknownText },
		OpText:      func(patch.Operation) string { return tableOpText },
		Clone:       cloneDoc,
	}
}

func wantBadRequest(t testing.TB, err *awserr.Error, text string) {
	t.Helper()
	bad := apigatewayapi.IdentityBadRequestException()
	if err == nil || err.ID.Shape != bad.Shape || err.ID.Status != http.StatusBadRequest || err.Message != text {
		t.Fatalf("error = %+v, want BadRequestException 400 %q (contract 5: path and op refusals)", err, text)
	}
}

// Apply runs the operations in order ("in the order specified in the
// list", [S] ListOfPatchOperation, apigateway-rest.md 3.8), passes the
// unescaped wild segments, and returns the result.
func TestApplyInOrder(t *testing.T) {
	v := doc{Name: "old"}
	got, err := table().Apply(v, []patch.Operation{
		{Op: apigatewayapi.OpReplace, Path: "/name", Value: new("a")},
		{Op: apigatewayapi.OpReplace, Path: "/name", Value: new("b")},
		{Op: apigatewayapi.OpAdd, Path: "/tags/k~1x", Value: new("1")},
		{Op: apigatewayapi.OpAdd, Path: "/list/image~1png"},
		{Op: apigatewayapi.OpAdd, Path: "/list/-"},
		{Op: apigatewayapi.OpAdd, Path: "/deep/a/b~1c"},
	})
	if err != nil {
		t.Fatalf("Apply = %v", err)
	}
	want := doc{Name: "b", Tags: map[string]string{"k/x": "1"}, List: []string{"image/png", "-"}, Deep: []string{"a", "b/c"}}
	if !equalDoc(got, want) {
		t.Errorf("Apply = %+v, want %+v", got, want)
	}
	if v.Name != "old" || v.Tags != nil || v.List != nil {
		t.Errorf("Apply changed its input to %+v", v)
	}
}

// A refused operation ends the call with its text and the original value
// (all-or-nothing, apigateway-rest.md 12.4): an unknown path answers the
// table's UnknownPath text, an op its rule does not allow the rule's
// OpText or else the table's, and a rule's own error passes through.
func TestApplyRefusals(t *testing.T) {
	v := doc{Name: "old", Tags: map[string]string{"a": "1"}}
	first := patch.Operation{Op: apigatewayapi.OpReplace, Path: "/name", Value: new("new")}
	for _, c := range []struct {
		op   patch.Operation
		text string
	}{
		{patch.Operation{Op: apigatewayapi.OpReplace, Path: "/nope"}, unknownText},
		{patch.Operation{Op: apigatewayapi.OpReplace, Path: "/list"}, unknownText},
		{patch.Operation{Op: apigatewayapi.OpAdd, Path: "/name", Value: new("x")}, tableOpText},
		{patch.Operation{Op: apigatewayapi.OpMove, Path: "/tags/a"}, ruleOpText},
		{patch.Operation{Op: apigatewayapi.OpReplace, Path: "/fail"}, failText},
	} {
		got, err := table().Apply(v, []patch.Operation{first, c.op})
		wantBadRequest(t, err, c.text)
		if !equalDoc(got, v) || v.Name != "old" || v.Tags["a"] != "1" {
			t.Errorf("%s %s: Apply = %+v and input %+v, want the input unchanged", c.op.Op, c.op.Path, got, v)
		}
	}
}

// opGen draws operations over the toy table, some of them refused.
func opGen(valid bool) *rapid.Generator[patch.Operation] {
	return rapid.Custom(func(t *rapid.T) patch.Operation {
		key := rapid.SampledFrom([]string{"a", "b", "c~1d"}).Draw(t, "key")
		val := rapid.StringMatching(`[a-z]{0,3}`).Draw(t, "value")
		ops := []patch.Operation{
			{Op: apigatewayapi.OpReplace, Path: "/name", Value: &val},
			{Op: apigatewayapi.OpAdd, Path: "/tags/" + key, Value: &val},
			{Op: apigatewayapi.OpReplace, Path: "/tags/" + key, Value: &val},
			{Op: apigatewayapi.OpRemove, Path: "/tags/" + key},
			{Op: apigatewayapi.OpAdd, Path: "/list/" + key},
			{Op: apigatewayapi.OpAdd, Path: "/deep/" + key + "/" + key},
		}
		if !valid {
			ops = []patch.Operation{
				{Op: apigatewayapi.OpRemove, Path: "/name"},
				{Op: apigatewayapi.OpReplace, Path: "/missing/" + key},
				{Op: apigatewayapi.OpCopy, Path: "/tags/" + key},
				{Op: apigatewayapi.OpReplace, Path: "/fail"},
			}
		}
		return rapid.SampledFrom(ops).Draw(t, "op")
	})
}

// Applying a request's operations equals applying them one at a time in
// order (spec 6b ticket 3).
func TestApplyEqualsOneAtATime(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ops := rapid.SliceOfN(opGen(true), 0, 8).Draw(t, "ops")
		v := doc{Name: "start", Tags: map[string]string{"a": "0"}}
		all, err := table().Apply(cloneDoc(v), ops)
		if err != nil {
			t.Fatalf("Apply(%+v) = %v", ops, err)
		}
		step := cloneDoc(v)
		for i, op := range ops {
			step, err = table().Apply(step, []patch.Operation{op})
			if err != nil {
				t.Fatalf("op %d %+v alone = %v", i, op, err)
			}
		}
		if !equalDoc(all, step) {
			t.Fatalf("Apply all = %+v, one at a time = %+v", all, step)
		}
	})
}

// A request containing a refused operation changes nothing, wherever the
// refused operation sits (spec 6b ticket 3; apigateway-rest.md 12.4).
func TestApplyRefusedChangesNothing(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		before := rapid.SliceOfN(opGen(true), 0, 4).Draw(t, "before")
		bad := opGen(false).Draw(t, "refused")
		after := rapid.SliceOfN(opGen(true), 0, 4).Draw(t, "after")
		v := doc{Name: "start", Tags: map[string]string{"a": "0"}, List: []string{"x"}}
		keep := cloneDoc(v)
		got, err := table().Apply(v, slices.Concat(before, []patch.Operation{bad}, after))
		if err == nil || err.ID.Status != http.StatusBadRequest {
			t.Fatalf("Apply with %+v = %v, want a 400 refusal", bad, err)
		}
		if !equalDoc(got, keep) || !equalDoc(v, keep) {
			t.Fatalf("Apply with %+v returned %+v and left the input %+v, want both %+v", bad, got, v, keep)
		}
	})
}
