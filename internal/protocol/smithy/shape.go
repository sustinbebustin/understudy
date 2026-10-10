package smithy

import (
	"fmt"
	"strings"
)

// ShapeID is an absolute Smithy shape id, namespace#Name. It is a string type
// so generated descriptors can hold it as a constant; the generator emits only
// ids it parsed, and hand-written code parses with Parse.
type ShapeID string

// ShapeIDError reports text that is not an absolute shape id.
type ShapeIDError struct {
	Text string
}

func (e *ShapeIDError) Error() string {
	return fmt.Sprintf("%q is not an absolute shape id: the form is namespace#Name, such as com.amazonaws.sqs#QueueDoesNotExist", e.Text)
}

// Parse accepts namespace#Name where the namespace is dot-separated
// identifiers and each identifier is a letter or underscore followed by
// letters, digits and underscores.
func Parse(s string) (ShapeID, error) {
	ns, name, ok := strings.Cut(s, "#")
	if !ok || !identifier(name) {
		return "", &ShapeIDError{Text: s}
	}
	for part := range strings.SplitSeq(ns, ".") {
		if !identifier(part) {
			return "", &ShapeIDError{Text: s}
		}
	}
	return ShapeID(s), nil
}

// Namespace returns the part before #.
func (id ShapeID) Namespace() string {
	ns, _, _ := strings.Cut(string(id), "#")
	return ns
}

// Name returns the part after #.
func (id ShapeID) Name() string {
	_, name, _ := strings.Cut(string(id), "#")
	return name
}

// identifier reports whether s is a Smithy identifier:
// IdentifierStart = 1*"_" (ALPHA / DIGIT) / ALPHA, then ALPHA, DIGIT or "_".
func identifier(s string) bool {
	rest := strings.TrimLeft(s, "_")
	if rest == "" {
		return false
	}
	first := rest[0]
	if !isAlpha(first) && (len(rest) == len(s) || !isDigit(first)) {
		return false
	}
	for i := range len(rest) {
		if c := rest[i]; !isAlpha(c) && !isDigit(c) && c != '_' {
			return false
		}
	}
	return true
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// ShapeType is the kind of a shape: every Smithy 2.0 kind the AWS models and
// the protocol compliance corpus use.
type ShapeType uint8

// ShapeType values, one per Smithy kind.
const (
	TypeStructure ShapeType = iota + 1
	TypeUnion
	TypeList
	TypeMap
	TypeString
	TypeEnum
	TypeIntEnum
	TypeBlob
	TypeBoolean
	TypeByte
	TypeShort
	TypeInteger
	TypeLong
	TypeFloat
	TypeDouble
	TypeBigInteger
	TypeBigDecimal
	TypeTimestamp
	TypeDocument
	// TypeUnit has no JSON AST "type" of its own: smithy.api#Unit is a
	// structure with @unitType, and the generator sets TypeUnit when it sees
	// that shape or trait. ParseShapeType never returns it.
	TypeUnit
)

// String returns the Smithy JSON AST spelling, such as "intEnum".
func (t ShapeType) String() string {
	switch t {
	case TypeStructure:
		return "structure"
	case TypeUnion:
		return "union"
	case TypeList:
		return "list"
	case TypeMap:
		return "map"
	case TypeString:
		return "string"
	case TypeEnum:
		return "enum"
	case TypeIntEnum:
		return "intEnum"
	case TypeBlob:
		return "blob"
	case TypeBoolean:
		return "boolean"
	case TypeByte:
		return "byte"
	case TypeShort:
		return "short"
	case TypeInteger:
		return "integer"
	case TypeLong:
		return "long"
	case TypeFloat:
		return "float"
	case TypeDouble:
		return "double"
	case TypeBigInteger:
		return "bigInteger"
	case TypeBigDecimal:
		return "bigDecimal"
	case TypeTimestamp:
		return "timestamp"
	case TypeDocument:
		return "document"
	case TypeUnit:
		return "unit"
	default:
		return fmt.Sprintf("ShapeType(%d)", uint8(t))
	}
}

// ShapeTypeError reports a JSON AST type the runtime does not represent.
type ShapeTypeError struct {
	Text string
}

func (e *ShapeTypeError) Error() string {
	return fmt.Sprintf("shape type %q has no runtime schema kind: service, resource, operation, apply and set shapes never reach a codec, and smithy.api#Unit is a structure with @unitType rather than type \"unit\"; check the model's spelling", e.Text)
}

// ParseShapeType maps a JSON AST "type" value to its ShapeType. It rejects
// "unit", which no JSON AST shape carries (see TypeUnit).
func ParseShapeType(s string) (ShapeType, error) {
	for t := TypeStructure; t <= TypeDocument; t++ {
		if t.String() == s {
			return t, nil
		}
	}
	return 0, &ShapeTypeError{Text: s}
}
