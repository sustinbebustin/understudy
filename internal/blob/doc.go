// Package blob stores content-addressed bytes in memory or on disk under
// os.Root, with reference counts. Keys never map to paths outside the root.
//
// A blob is named by the lowercase hex sha256 of its bytes (Ref) and lives at
// sha256/<hex> under the store's root; Put writes to tmp/<random> first and
// renames into place once the digest is known. Ref is the only name that
// becomes a path component, so no caller-supplied string (an S3 key, a file
// name in a zip) ever reaches the file system.
//
// Reference counts: Put and Retain add one, Release removes one and the last
// Release deletes the blob. A snapshot section lists one entry per reference
// its state holds, so on load the snapshot layer calls Import (which adds
// blobs with no references) and then Retains each listed entry, rebuilding
// the live counts. NewDisk indexes the blobs already under its root the same
// way, without re-hashing them. After that retain pass the snapshot load and
// the --persist startup call Prune, which deletes every blob still at zero
// references: blobs no loaded state names, such as those Put after the last
// checkpoint before a crash, or those a failed Import left behind.
//
// A disk store syncs each blob's bytes before naming it, and Sync makes the
// names durable; whoever persists state that names blobs calls Sync before
// making that state durable.
//
// A memory store keeps blobs of at most its spill threshold in memory and
// streams larger ones to a private temporary directory that Close removes.
// Put never buffers more than the threshold, whatever the body's size, except
// that a body of unknown length within the threshold briefly holds up to
// twice its size while its chunks are joined; a Put given its exact size
// reads the body into one buffer and never copies it.
package blob
