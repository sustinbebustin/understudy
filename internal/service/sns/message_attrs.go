package sns

import (
	"encoding/base64"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// maxAttributeNameBytes is the longest message attribute name (sns.md 4.2).
const maxAttributeNameBytes = 256

// messageAttributes checks publish attributes (sns.md 4.2) and returns them
// sorted by name, since AWS's own order is not stable. Every failure is
// ParameterValueInvalid with the recorded text, except the reserved-prefix,
// numeric-value and String.Array checks, whose texts are inferred.
func messageAttributes(in map[string]snsapi.MessageAttributeValue) ([]delivery.MessageAttribute, *awserr.Error) {
	out := make([]delivery.MessageAttribute, 0, len(in))
	for _, name := range slices.Sorted(maps.Keys(in)) {
		if err := checkAttributeName(name); err != nil {
			return nil, err
		}
		v, err := attributeValue(name, in[name])
		if err != nil {
			return nil, err
		}
		out = append(out, delivery.MessageAttribute{Name: name, Value: v})
	}
	return out, nil
}

// checkAttributeName checks a message attribute name: up to 256 bytes of
// letters, digits, hyphens, underscores and dots, no dot at either end, no
// two dots together, and no reserved prefix.
func checkAttributeName(name string) *awserr.Error {
	if len(name) > maxAttributeNameBytes {
		return errParameterValue("Length of message attribute name must be less than 256 bytes.")
	}
	for _, r := range name {
		if !isAlnum(r) && r != '-' && r != '_' && r != '.' {
			return errParameterValue(fmt.Sprintf("Invalid non-alphanumeric character '#x%X' was found in the message attribute name. "+
				"Can only include alphanumeric characters, hyphens, underscores, or dots.", r))
		}
	}
	switch {
	case strings.HasPrefix(name, "."):
		return errParameterValue("Invalid message attribute name starting with character '.' was found.")
	case strings.HasSuffix(name, "."):
		return errParameterValue("Invalid message attribute name ending with character '.' was found.")
	case strings.Contains(name, ".."):
		return errParameterValue("Message attribute name can not have successive '.' character.")
	}
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "aws.") || strings.HasPrefix(lower, "amazon.") {
		return errParameterValue("Message attribute name '" + name + "' starts with a reserved prefix; names may not start with AWS. or Amazon.")
	}
	return nil
}

// attributeValue checks one attribute's type and value field.
func attributeValue(name string, v snsapi.MessageAttributeValue) (delivery.AttributeValue, *awserr.Error) {
	base, label, _ := strings.Cut(v.DataType, ".")
	switch base {
	case "String", "Number":
		if v.StringValue == nil || *v.StringValue == "" {
			if v.StringValue == nil && v.BinaryValue != nil {
				return nil, errParameterValue("The message attribute '" + name + "' with type '" + base + "' must use field 'String'.")
			}
			return nil, errEmptyAttribute(name, base)
		}
		if base == "String" {
			return stringAttribute(name, label, *v.StringValue)
		}
		if _, err := document.ParseNumber(strings.TrimSpace(*v.StringValue)); err != nil {
			return nil, errParameterValue("The message attribute '" + name + "' with type 'Number' has a value that is not a number: " + *v.StringValue)
		}
		return delivery.NumberAttribute{Label: label, Value: *v.StringValue}, nil
	case "Binary":
		if len(v.BinaryValue) == 0 {
			if v.StringValue != nil {
				return nil, errParameterValue("The message attribute '" + name + "' with type 'Binary' must use field 'Binary'.")
			}
			return nil, errEmptyAttribute(name, base)
		}
		return delivery.BinaryAttribute{Label: label, Value: v.BinaryValue}, nil
	}
	return nil, errParameterValue("The message attribute '" + name + "' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String.")
}

// stringAttribute checks a non-empty String value; a String.Array one must
// be a JSON array.
func stringAttribute(name, label, value string) (delivery.AttributeValue, *awserr.Error) {
	if label == "Array" && !validStringArray(value) {
		return nil, errParameterValue("The message attribute '" + name + "' with type 'String.Array' has a value that is not a JSON array " +
			"of strings, numbers, true, false or null: " + value)
	}
	return delivery.StringAttribute{Label: label, Value: value}, nil
}

// validStringArray reports a String.Array value: a JSON array whose
// elements are strings, numbers, true, false or null (sns.md 4.2).
func validStringArray(s string) bool {
	v, ok := parseJSON(s)
	if !ok {
		return false
	}
	elems, ok := v.(document.List)
	if !ok {
		return false
	}
	for _, e := range elems {
		switch e.(type) {
		case document.String, document.Number, document.Bool, document.Null:
		case document.List, document.Map:
			return false
		}
	}
	return true
}

func errEmptyAttribute(name, base string) *awserr.Error {
	return errParameterValue("The message attribute '" + name + "' must contain non-empty message attribute value for message attribute type '" + base + "'.")
}

// attributeBytes is what attributes add to a message's size: each name,
// DataType and value (sns.md 4.1).
func attributeBytes(attrs []delivery.MessageAttribute) int {
	n := 0
	for _, a := range attrs {
		n += len(a.Name) + len(a.Value.DataType())
		switch v := a.Value.(type) {
		case delivery.StringAttribute:
			n += len(v.Value)
		case delivery.NumberAttribute:
			n += len(v.Value)
		case delivery.BinaryAttribute:
			n += len(v.Value)
		}
	}
	return n
}

// envelopeAttributes renders attributes as an envelope carries them: the
// StringValue, or the BinaryValue in base64 (sns.md 5.1).
func envelopeAttributes(attrs []delivery.MessageAttribute) []envelope.Attribute {
	out := make([]envelope.Attribute, 0, len(attrs))
	for _, a := range attrs {
		e := envelope.Attribute{Name: a.Name, Type: a.Value.DataType()}
		switch v := a.Value.(type) {
		case delivery.StringAttribute:
			e.Value = v.Value
		case delivery.NumberAttribute:
			e.Value = v.Value
		case delivery.BinaryAttribute:
			e.Value = base64.StdEncoding.EncodeToString(v.Value)
		}
		out = append(out, e)
	}
	return out
}
