package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
)

// refLen is the length of a sha256 digest in hex.
const refLen = 2 * sha256.Size

// Ref names a blob by the lowercase hex sha256 of its bytes. The zero Ref
// names nothing; ParseRef is the only way to build one from a string.
type Ref struct {
	hex string
}

// ParseRef parses a lowercase hex sha256 digest. Anything else, including
// uppercase hex, is refused with *InvalidRefError, so a Ref can always be
// used as a file name.
func ParseRef(s string) (Ref, error) {
	if len(s) != refLen {
		return Ref{}, &InvalidRefError{Value: s}
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return Ref{}, &InvalidRefError{Value: s}
		}
	}
	return Ref{hex: s}, nil
}

func refOf(h hash.Hash) Ref {
	return Ref{hex: hex.EncodeToString(h.Sum(nil))}
}

// String returns the hex digest.
func (r Ref) String() string {
	return r.hex
}

// MarshalText renders the hex digest, so a Ref can sit in persisted state.
// The zero Ref is refused with *ZeroRefError: UnmarshalText could not read it
// back, so the save fails instead of the later load.
func (r Ref) MarshalText() ([]byte, error) {
	if r.hex == "" {
		return nil, &ZeroRefError{}
	}
	return []byte(r.hex), nil
}

// UnmarshalText parses a hex digest with ParseRef.
func (r *Ref) UnmarshalText(b []byte) error {
	parsed, err := ParseRef(string(b))
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}

// path is the blob's location under a store root. The name is hex, so no
// caller-supplied string ever becomes a path component.
func (r Ref) path() string {
	return blobDir + "/" + r.hex
}
