package snapshot

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// FormatError reports an archive written in a newer format than this binary
// reads. Nothing was loaded.
type FormatError struct {
	// Archive is the archive's format and Reader the newest this binary
	// reads.
	Archive, Reader int
	// Writer names the build that wrote the archive.
	Writer string
}

func (e *FormatError) Error() string {
	return fmt.Sprintf("snapshot archive format %d is newer than format %d, the newest this understudy reads; the archive was written by %s; nothing was loaded (load it with that understudy version or a newer one)",
		e.Archive, e.Reader, writerName(e.Writer))
}

// SchemaError reports a section whose state schema version differs from the
// running binary's, or that belongs to a service the binary does not have.
// Until the first tagged release there are no migrations, so the archive is
// refused and nothing is loaded.
type SchemaError struct {
	Path    string
	Service ref.ServiceID
	// Section is the section's schema version.
	Section int
	// Running is the running binary's schema version for Service; Known is
	// false when the binary has no such service.
	Running int
	Known   bool
	// Writer names the build that wrote the archive.
	Writer string
}

func (e *SchemaError) Error() string {
	if !e.Known {
		return fmt.Sprintf("snapshot section %q belongs to service %q at state version %d, which this understudy does not have; the archive was written by %s; nothing was loaded (load it with that understudy version)",
			e.Path, e.Service, e.Section, writerName(e.Writer))
	}
	return fmt.Sprintf("snapshot section %q has %s state version %d but this understudy runs version %d; the archive was written by %s; nothing was loaded (load it with that understudy version)",
		e.Path, e.Service, e.Section, e.Running, writerName(e.Writer))
}

// EntryError reports an archive entry that cannot be read or written. The
// archive is refused as a whole.
type EntryError struct {
	Name   string
	Defect EntryDefect
	// Err is the underlying cause, if any.
	Err error
}

func (e *EntryError) Error() string {
	msg := fmt.Sprintf("snapshot entry %q %s; the archive was refused and nothing was loaded", e.Name, e.Defect)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause.
func (e *EntryError) Unwrap() error { return e.Err }

// EntryDefect says what is wrong with an archive entry.
type EntryDefect uint8

// EntryDefect values.
const (
	// NonLocalName: the name is absolute, climbs out with "..", or is not
	// a clean slash-separated path.
	NonLocalName EntryDefect = iota + 1
	// NotRegular: the entry is a directory, link or other non-file entry.
	NotRegular
	// DuplicateEntry: another entry has the same name.
	DuplicateEntry
	// UnexpectedEntry: the name is not part of the archive layout.
	UnexpectedEntry
	// MissingEntry: the manifest names the entry but the archive lacks it,
	// or the archive lacks manifest.json.
	MissingEntry
	// UnlistedEntry: a section entry the manifest does not list, or a blob
	// no section pins.
	UnlistedEntry
	// DigestMismatch: the bytes do not hash to the digest naming them.
	DigestMismatch
	// BadSectionPath: a section path is not "services/<svc>/<path>" with
	// local, clean elements.
	BadSectionPath
	// UnreadableBlob: the blob source cannot size or open a pinned blob,
	// or returned a different number of bytes than it reported.
	UnreadableBlob
	// ExternalBlobEmbedded: a blob entry in an archive whose manifest
	// places the blobs in the blob store.
	ExternalBlobEmbedded
)

// String describes the defect.
func (d EntryDefect) String() string {
	switch d {
	case NonLocalName:
		return "is not a local path: entry names must be relative, clean and stay inside the archive"
	case NotRegular:
		return "is not a regular file"
	case DuplicateEntry:
		return "appears more than once"
	case UnexpectedEntry:
		return "is not part of the snapshot layout (manifest.json, faults.json, journal.ndjson, services/, blobs/sha256/)"
	case MissingEntry:
		return "is missing from the archive"
	case UnlistedEntry:
		return "is not listed in the manifest or pinned by any section"
	case DigestMismatch:
		return "does not match its sha256 digest; the archive is corrupt"
	case BadSectionPath:
		return `is not a section path of the form "services/<service>/<path>"`
	case UnreadableBlob:
		return "cannot be read from the blob store"
	case ExternalBlobEmbedded:
		return `is a blob entry, but the manifest says the blobs live in the blob store ("blobs": "external")`
	default:
		return fmt.Sprintf("has unknown defect %d", uint8(d))
	}
}

// ManifestError reports a manifest.json that does not describe a loadable
// archive, or a Manifest that cannot be written.
type ManifestError struct {
	Reason string
	// Err is the underlying cause, if any.
	Err error
}

func (e *ManifestError) Error() string {
	msg := "snapshot manifest is invalid: " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause.
func (e *ManifestError) Unwrap() error { return e.Err }

// SizeError reports an archive larger than ReadOptions.MaxBytes once
// decompressed. Nothing was loaded.
type SizeError struct {
	Max int64
}

func (e *SizeError) Error() string {
	return fmt.Sprintf("snapshot archive is larger than the %d byte limit once decompressed; nothing was loaded (raise the limit if the archive is trusted)", e.Max)
}

// StreamError reports an archive stream that is damaged outside its
// entries' contents: it is not a readable tar, its gzip header, checksum or
// length trailer is bad, or bytes other than zero padding follow the end of
// the tar. Nothing was loaded.
type StreamError struct {
	Reason string
	// Err is the underlying cause, if any.
	Err error
}

func (e *StreamError) Error() string {
	msg := "snapshot archive " + e.Reason + "; the file is corrupt, truncated or not a snapshot, and nothing was loaded"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause.
func (e *StreamError) Unwrap() error { return e.Err }

// CutError reports a Cut that WriteAfterCut refuses. Nothing was written.
type CutError struct {
	Defect CutDefect
}

func (e *CutError) Error() string {
	switch e.Defect {
	case CutReleased:
		return "snapshot cut was already written or released, so its blobs are no longer pinned; nothing was written (take a new cut)"
	case CutForeign:
		return "snapshot cut was not taken by this Coordinator; nothing was written (write it with the Coordinator whose Cut returned it)"
	default:
		return fmt.Sprintf("snapshot cut has unknown defect %d; nothing was written", uint8(e.Defect))
	}
}

// CutDefect says why WriteAfterCut refuses a Cut.
type CutDefect uint8

// CutDefect values.
const (
	// CutReleased: Release or an earlier WriteAfterCut already freed it.
	CutReleased CutDefect = iota + 1
	// CutForeign: another Coordinator took it, or it is nil.
	CutForeign
)

// ConfigError reports a Coordinator configuration or read option that
// cannot work, such as two services with the same name.
type ConfigError struct {
	Reason string
}

func (e *ConfigError) Error() string {
	return "snapshot: " + e.Reason
}

// LoadError reports a load that failed after it began replacing the
// emulator state. Services listed in Loaded imported their sections; the
// rest of the state is as the failed step left it.
type LoadError struct {
	// Step names what failed, such as `importing service "sqs"`.
	Step   string
	Loaded []ref.ServiceID
	Err    error
}

func (e *LoadError) Error() string {
	return fmt.Sprintf("snapshot load failed %s; the state is partially loaded (services loaded: %v), so reset or load a snapshot again: %v",
		e.Step, e.Loaded, e.Err)
}

// Unwrap returns the underlying cause.
func (e *LoadError) Unwrap() error { return e.Err }

// BlobReleaseError reports a load that replaced the state and rearmed every
// service, but could not free the blobs only the replaced state used. The
// loaded state is complete; the unfreed blobs stay in the blob store until
// the emulator restarts.
type BlobReleaseError struct {
	Err error
}

func (e *BlobReleaseError) Error() string {
	return "snapshot loaded, but freeing the blobs it no longer needs failed; some may stay in the blob store until the emulator restarts: " + e.Err.Error()
}

// Unwrap returns the underlying cause.
func (e *BlobReleaseError) Unwrap() error { return e.Err }

func writerName(w string) string {
	if w == "" {
		return "an unnamed understudy build"
	}
	return w
}
