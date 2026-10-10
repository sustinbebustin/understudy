package blob

import "fmt"

// InvalidRefError reports a string that is not a lowercase hex sha256 digest.
type InvalidRefError struct {
	Value string
}

// Error names the refused string and the expected form.
func (e *InvalidRefError) Error() string {
	return fmt.Sprintf("blob ref %q is not a lowercase hex sha256 digest (%d characters of 0-9a-f)", e.Value, refLen)
}

// ZeroRefError reports an attempt to persist the zero Ref, which names no
// blob and could not be loaded back.
type ZeroRefError struct{}

// Error explains that the state being saved holds an unset Ref.
func (*ZeroRefError) Error() string {
	return "blob ref is unset (the zero Ref) and cannot be persisted; the state being saved holds a Ref that was never set from a stored blob"
}

// SpillAboveError reports a memory-mode spill threshold above MaxSpillAbove.
// No store was created.
type SpillAboveError struct {
	Value int64
}

// Error names the refused threshold and the cap.
func (e *SpillAboveError) Error() string {
	return fmt.Sprintf("blob spill threshold %d bytes is above the maximum of %d; configure a smaller in-memory blob size", e.Value, MaxSpillAbove)
}

// NotFoundError reports a Ref the store does not hold.
type NotFoundError struct {
	Ref Ref
}

// Error names the missing blob.
func (e *NotFoundError) Error() string {
	return fmt.Sprintf("blob %s is not in the store; it was never stored or its last reference was released", e.Ref)
}

// NotRetainedError reports a Release of a blob with no outstanding
// references, which means a caller released more often than it retained.
type NotRetainedError struct {
	Ref Ref
}

// Error names the over-released blob.
func (e *NotRetainedError) Error() string {
	return fmt.Sprintf("blob %s released with no outstanding references; the store is unchanged", e.Ref)
}

// RangeError reports a byte range outside a blob.
type RangeError struct {
	Ref    Ref
	Offset int64
	Length int64
	Size   int64
}

// Error names the blob, the requested range and the blob's size.
func (e *RangeError) Error() string {
	return fmt.Sprintf("blob %s: range offset %d length %d is outside its %d bytes", e.Ref, e.Offset, e.Length, e.Size)
}

// CorruptBlobError reports an imported blob whose bytes do not hash to its
// file name. Nothing from that file was kept.
type CorruptBlobError struct {
	Name string
	Got  Ref
}

// Error names the file and the digest its bytes actually have.
func (e *CorruptBlobError) Error() string {
	return fmt.Sprintf("imported blob %q hashes to %s; the file is corrupt or misnamed and was not imported", e.Name, e.Got)
}

// UnexpectedFileError reports a file in a blob directory whose name is not a
// blob ref, which means the directory is not a blob store or was tampered with.
type UnexpectedFileError struct {
	Name string
}

// Error names the unexpected file and how to recover.
func (e *UnexpectedFileError) Error() string {
	return fmt.Sprintf("blob directory holds %q, which is not a sha256 hex name; remove it or point understudy at a different directory", e.Name)
}
