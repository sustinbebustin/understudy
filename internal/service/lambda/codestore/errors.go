package codestore

import (
	"fmt"
	"strconv"
)

// Problem says why an archive entry is refused.
type Problem uint8

// The reasons an entry is unsafe to extract.
const (
	// NonLocalName is a name that is absolute, empty, or climbs out of the
	// tree with "..": the zip-slip shape.
	NonLocalName Problem = iota + 1
	// Symlink is a symbolic link entry; no link is ever created from an
	// archive, because a bind mount would follow it on the host.
	Symlink
	// SpecialFile is a device, named pipe, socket or other irregular entry.
	SpecialFile
	// EntryTooLarge is one entry above Options.MaxEntry uncompressed.
	EntryTooLarge
	// UnzippedTooLarge is an archive whose entries add up to more than
	// Options.MaxUnzipped uncompressed.
	UnzippedTooLarge
	// ZippedTooLarge is a package longer than Options.MaxZipped; it is
	// refused while being read, before its entries are looked at.
	ZippedTooLarge
)

func (p Problem) String() string {
	switch p {
	case NonLocalName:
		return "non-local name"
	case Symlink:
		return "symbolic link"
	case SpecialFile:
		return "special file"
	case EntryTooLarge:
		return "entry too large"
	case UnzippedTooLarge:
		return "unzipped size too large"
	case ZippedTooLarge:
		return "zipped size too large"
	}
	return "Problem(" + strconv.Itoa(int(p)) + ")"
}

// UnsafeZipError reports an archive refused before anything was written to
// the tree, because one entry is unsafe or the archive is too large.
type UnsafeZipError struct {
	// Entry is the name as the archive spells it; empty for
	// ZippedTooLarge, which concerns the whole package.
	Entry   string
	Problem Problem
	// Size and Limit are the offending size in bytes and the limit it
	// exceeds: uncompressed for the entry problems, and for ZippedTooLarge
	// the bytes read when reading stopped, one past Limit. Both are zero for
	// the name and type problems.
	Size, Limit int64
}

func (e *UnsafeZipError) Error() string {
	switch e.Problem {
	case NonLocalName:
		return fmt.Sprintf("function code archive refused: entry %q is absolute, empty or leaves the code directory through \"..\"; rebuild the zip with relative paths", e.Entry)
	case Symlink:
		return fmt.Sprintf("function code archive refused: entry %q is a symbolic link, which understudy never extracts; replace the link with the file it points to", e.Entry)
	case SpecialFile:
		return fmt.Sprintf("function code archive refused: entry %q is not a regular file or directory", e.Entry)
	case EntryTooLarge:
		return fmt.Sprintf("function code archive refused: entry %q is %d bytes unzipped, above the %d byte limit per entry", e.Entry, e.Size, e.Limit)
	case UnzippedTooLarge:
		return fmt.Sprintf("function code archive refused: unzipped size reaches %d bytes at entry %q, above the %d byte limit (Lambda allows 262144000 bytes unzipped)", e.Size, e.Entry, e.Limit)
	case ZippedTooLarge:
		return fmt.Sprintf("function code archive refused: the package is longer than the %d byte limit on its zipped size; reading stopped there and nothing was kept", e.Limit)
	}
	return fmt.Sprintf("function code archive refused: entry %q: %v", e.Entry, e.Problem)
}

// InvalidZipError reports bytes that are not a readable zip archive, or an
// archive whose entries contradict each other (a file where a directory is
// needed, a size or checksum that does not match the data). Nothing is kept.
type InvalidZipError struct {
	Err error
}

func (e *InvalidZipError) Error() string {
	return fmt.Sprintf("function code is not a valid zip archive: %v; upload a zip file built by a standard zip tool", e.Err)
}

func (e *InvalidZipError) Unwrap() error { return e.Err }

// NotFoundError reports a digest the store holds no tree for: it was never
// stored, or its last reference was released.
type NotFoundError struct {
	Sha256 Digest
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("code store holds no tree for CodeSha256 %s; it was never stored or its last reference was released", e.Sha256)
}

// LimitError reports a negative limit in Options.
type LimitError struct {
	Name  string
	Value int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("code store option %s is %d; use a positive byte count, or zero for the default", e.Name, e.Value)
}
