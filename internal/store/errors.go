package store

import "fmt"

// VersionMismatchError reports a snapshot section written by a state schema
// version other than the running binary's. Until the first tagged release
// there are no migrations (ADR 0009), so the section is refused.
type VersionMismatchError struct {
	Path    string
	Section int
	Running int
}

// Error names the section and both state versions.
func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf("snapshot section %q has state version %d but this binary runs version %d; load it with the understudy version that wrote it",
		e.Path, e.Section, e.Running)
}

// SectionError reports a snapshot section that cannot be exported or
// imported. Err is the underlying cause, if any.
type SectionError struct {
	Path   string
	Defect SectionDefect
	Err    error
}

// Error names the section, the defect and the cause, if any.
func (e *SectionError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("snapshot section %q: %s", e.Path, e.Defect)
	}
	return fmt.Sprintf("snapshot section %q: %s: %v", e.Path, e.Defect, e.Err)
}

// Unwrap returns the underlying cause.
func (e *SectionError) Unwrap() error {
	return e.Err
}

// SectionDefect says what is wrong with a section.
type SectionDefect uint8

// SectionDefect values.
const (
	// MissingExtension: the path does not end in ".json".
	MissingExtension SectionDefect = iota + 1
	// NoEncoding: the key codec has no encoding for the key (Export), or the
	// path names an empty key (Import).
	NoEncoding
	// NonLocalPath: the key's encoding is not a local, slash-separated path,
	// so the section would land outside its service's directory.
	NonLocalPath
	// UndecodablePath: the key codec cannot decode the path to a key.
	UndecodablePath
	// NonCanonicalPath: the path decodes to a key whose encoding differs.
	NonCanonicalPath
	// DuplicateKey: another section in the same import names the same key
	// (Import), or another key encodes to the same path, which a KeyCodec
	// that is not injective allows (Export).
	DuplicateKey
	// UndecodableState: the section data does not decode into the state.
	UndecodableState
	// UnencodableState: the state does not encode.
	UnencodableState
)

// String describes the defect.
func (d SectionDefect) String() string {
	switch d {
	case MissingExtension:
		return "path does not end in " + sectionExt
	case NoEncoding:
		return "the key codec has no encoding for this key"
	case NonLocalPath:
		return "key encodes to a non-local path"
	case UndecodablePath:
		return "path does not decode to a key"
	case NonCanonicalPath:
		return "path is not the canonical encoding of its key"
	case DuplicateKey:
		return "another section has the same path"
	case UndecodableState:
		return "state does not decode"
	case UnencodableState:
		return "state does not encode"
	default:
		return fmt.Sprintf("unknown section defect %d", uint8(d))
	}
}
