package store

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// KeyCodec maps a partition key to the relative path its snapshot section is
// stored under, and back. Encode returns the empty string for a key it cannot
// represent, which Export refuses; over every other key it must be injective,
// and Decode must invert it: Import refuses a path that does not decode to a
// key encoding to the same path. The encoded form also orders keys
// (Partitioned.Keys).
type KeyCodec[K any] struct {
	// Encode renders a key as a slash-separated relative path without an
	// extension, such as "000000000000/us-east-1".
	Encode func(K) string
	// Decode parses a path produced by Encode.
	Decode func(string) (K, error)
}

// StringKeys is the identity codec for string keys, such as S3 bucket names.
func StringKeys() KeyCodec[string] {
	return KeyCodec[string]{
		Encode: func(k string) string { return k },
		Decode: func(s string) (string, error) { return s, nil },
	}
}

// AccountKeys encodes an account as its 12 digits, the layout of IAM and STS
// sections. The zero account has no encoding.
func AccountKeys() KeyCodec[aws.AccountID] {
	return KeyCodec[aws.AccountID]{
		Encode: aws.AccountID.String,
		Decode: aws.ParseAccountID,
	}
}

// ScopeKeys encodes a scope as "<account>/<region>", the section layout of
// ARCHITECTURE.md 13. The partition is not encoded: aws is the only partition
// understudy serves, so Decode sets it to aws.PartitionAWS and a scope with
// any other partition, or with a zero account or region, has no encoding.
// Region names are checked for syntax only, as aws.Region.UnmarshalText does,
// because the table that admitted them may hold configured extras.
func ScopeKeys() KeyCodec[aws.Scope] {
	return KeyCodec[aws.Scope]{
		Encode: func(s aws.Scope) string {
			if s.Partition != aws.PartitionAWS() || s.Account.IsZero() || s.Region.IsZero() {
				return ""
			}
			return s.Account.String() + "/" + s.Region.String()
		},
		Decode: func(enc string) (aws.Scope, error) {
			account, region, ok := strings.Cut(enc, "/")
			if !ok || region == "" || strings.Contains(region, "/") {
				return aws.Scope{}, &KeyLayoutError{Encoded: enc, Layout: "<account>/<region>"}
			}
			a, err := aws.ParseAccountID(account)
			if err != nil {
				return aws.Scope{}, err
			}
			var r aws.Region
			if err := r.UnmarshalText([]byte(region)); err != nil {
				return aws.Scope{}, err
			}
			return aws.Scope{Partition: aws.PartitionAWS(), Account: a, Region: r}, nil
		},
	}
}

// KeyLayoutError reports an encoded key whose path segments do not match the
// codec's layout.
type KeyLayoutError struct {
	Encoded string
	Layout  string
}

// Error names the encoded key and the layout it should follow.
func (e *KeyLayoutError) Error() string {
	return fmt.Sprintf("key path %q does not match the layout %s", e.Encoded, e.Layout)
}

// Codec serializes cell state for snapshot sections. Its signatures match
// encoding/json, so the caller picks the encoder; Marshal must be
// deterministic for snapshots to be byte-identical across saves.
type Codec struct {
	Marshal   func(v any) ([]byte, error)
	Unmarshal func(data []byte, v any) error
}

// Section is one snapshot document: the serialized state of one cell.
type Section struct {
	// Path is relative to the owning service's directory in the archive, such
	// as "000000000000/us-east-1.json"; the snapshot layer adds the
	// "services/<svc>/" prefix.
	Path string
	// Version is the schema version of the state that wrote Data.
	Version int
	// Data is the serialized state.
	Data []byte
	// Blobs lists the sha256 hex blob references the state holds, one entry
	// per reference and sorted, so retaining each entry on load rebuilds the
	// live reference counts.
	Blobs []string
}
