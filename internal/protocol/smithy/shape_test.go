package smithy_test

import (
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

func TestParseShapeID(t *testing.T) {
	tests := []struct{ in, namespace, name string }{
		{"com.amazonaws.sqs#QueueDoesNotExist", "com.amazonaws.sqs", "QueueDoesNotExist"},
		{"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException", "com.amazonaws.dynamodb.v20120810", "ResourceNotFoundException"},
		{"smithy.api#Unit", "smithy.api", "Unit"},
		{"aws.protocoltests.json10#JsonRpc10", "aws.protocoltests.json10", "JsonRpc10"},
		{"_a#_b1", "_a", "_b1"},
	}
	for _, tt := range tests {
		id, err := smithy.Parse(tt.in)
		if err != nil || id.Namespace() != tt.namespace || id.Name() != tt.name || string(id) != tt.in {
			t.Errorf("Parse(%q) = %q (%q, %q), %v", tt.in, id, id.Namespace(), id.Name(), err)
		}
	}
	for _, in := range []string{"", "Name", "#Name", "ns#", "ns#1Name", "a..b#C", "ns#Name$member", "ns#Na-me", "1ns#Name", "ns#A#B", "ns#_", "_#A", "ns#__", "a.__#B"} {
		_, err := smithy.Parse(in)
		var target *smithy.ShapeIDError
		if !errors.As(err, &target) || target.Text != in {
			t.Errorf("Parse(%q) error = %v, want *ShapeIDError", in, err)
		}
	}
}

func TestShapeIDRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ident := rapid.StringMatching(`(_+[A-Za-z0-9]|[A-Za-z])[A-Za-z0-9_]{0,10}`)
		parts := rapid.SliceOfN(ident, 1, 4).Draw(t, "namespace")
		name := ident.Draw(t, "name")
		ns := strings.Join(parts, ".")
		id, err := smithy.Parse(ns + "#" + name)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if id.Namespace() != ns || id.Name() != name {
			t.Fatalf("Parse(%q) split into %q and %q", ns+"#"+name, id.Namespace(), id.Name())
		}
	})
}

func TestShapeTypeNamesRoundTrip(t *testing.T) {
	names := []string{
		"structure", "union", "list", "map", "string", "enum", "intEnum", "blob", "boolean", "byte",
		"short", "integer", "long", "float", "double", "bigInteger", "bigDecimal", "timestamp", "document",
	}
	for _, n := range names {
		st, err := smithy.ParseShapeType(n)
		if err != nil || st.String() != n {
			t.Errorf("ParseShapeType(%q) = %v, %v", n, st, err)
		}
	}
	// No JSON AST shape has type "unit": smithy.api#Unit is a structure.
	for _, n := range []string{"service", "operation", "set", "apply", "Structure", "unit"} {
		var target *smithy.ShapeTypeError
		if _, err := smithy.ParseShapeType(n); !errors.As(err, &target) {
			t.Errorf("ParseShapeType(%q) error = %v, want *ShapeTypeError", n, err)
		}
	}
}
