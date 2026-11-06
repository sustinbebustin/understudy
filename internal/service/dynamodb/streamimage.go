package dynamodb

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
)

// image is an item, or its key, as DynamoDB Local wrote it in a stream
// record: each attribute's value in DynamoDB JSON, kept as Local sent it
// (dynamodb-streams.md 11.1 item 4: images are copied from Local).
type image map[string]json.RawMessage

// ImageError is an attribute of a stream record from DynamoDB Local that
// is not DynamoDB JSON; the feed drops nothing for it but fails the drain.
type ImageError struct {
	Attribute string
	Reason    string
}

func (e *ImageError) Error() string {
	return fmt.Sprintf("DynamoDB Local's stream record has attribute %q that is not a DynamoDB attribute value: %s", e.Attribute, e.Reason)
}

// typed returns im as the Streams model's attribute map.
func (im image) typed() (map[string]dynamodbstreamsapi.AttributeValue, error) {
	out := make(map[string]dynamodbstreamsapi.AttributeValue, len(im))
	for _, k := range slices.Sorted(maps.Keys(im)) {
		v, err := attributeValue(im[k])
		if err != nil {
			return nil, &ImageError{Attribute: k, Reason: err.Error()}
		}
		out[k] = v
	}
	return out, nil
}

// attributeValue parses one attribute value in DynamoDB JSON: an object
// with exactly one of the members S, N, B, SS, NS, BS, M, L, NULL, BOOL.
func attributeValue(raw json.RawMessage) (dynamodbstreamsapi.AttributeValue, error) {
	var tagged map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tagged); err != nil {
		return nil, err
	}
	if len(tagged) != 1 {
		return nil, fmt.Errorf("an attribute value has exactly one member, this has %d", len(tagged))
	}
	tag := slices.Collect(maps.Keys(tagged))[0]
	return taggedValue(tag, tagged[tag])
}

func taggedValue(tag string, v json.RawMessage) (dynamodbstreamsapi.AttributeValue, error) {
	switch tag {
	case "S":
		x, err := decodeAs[string](v)
		return dynamodbstreamsapi.AttributeValueMemberS{Value: x}, err
	case "N":
		x, err := decodeAs[string](v)
		return dynamodbstreamsapi.AttributeValueMemberN{Value: x}, err
	case "B":
		x, err := decodeAs[[]byte](v)
		return dynamodbstreamsapi.AttributeValueMemberB{Value: x}, err
	case "SS":
		x, err := decodeAs[[]string](v)
		return dynamodbstreamsapi.AttributeValueMemberSS{Value: x}, err
	case "NS":
		x, err := decodeAs[[]string](v)
		return dynamodbstreamsapi.AttributeValueMemberNS{Value: x}, err
	case "BS":
		x, err := decodeAs[[][]byte](v)
		return dynamodbstreamsapi.AttributeValueMemberBS{Value: x}, err
	case "NULL":
		x, err := decodeAs[bool](v)
		return dynamodbstreamsapi.AttributeValueMemberNULL{Value: x}, err
	case "BOOL":
		x, err := decodeAs[bool](v)
		return dynamodbstreamsapi.AttributeValueMemberBOOL{Value: x}, err
	case "M":
		m, err := decodeAs[image](v)
		if err != nil {
			return nil, err
		}
		x, err := m.typed()
		return dynamodbstreamsapi.AttributeValueMemberM{Value: x}, err
	case "L":
		raws, err := decodeAs[[]json.RawMessage](v)
		if err != nil {
			return nil, err
		}
		x := make([]dynamodbstreamsapi.AttributeValue, len(raws))
		for i, r := range raws {
			if x[i], err = attributeValue(r); err != nil {
				return nil, err
			}
		}
		return dynamodbstreamsapi.AttributeValueMemberL{Value: x}, nil
	}
	return nil, fmt.Errorf("unknown member %q", tag)
}

func decodeAs[T any](raw json.RawMessage) (T, error) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err
}
