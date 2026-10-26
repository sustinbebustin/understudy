package codestore

import (
	"context"
	"fmt"
)

// Merged is the tree a function's layers fill /opt with: each layer's tree
// copied in the order the function names them, a later file replacing an
// earlier one at the same path (lambda-destinations-layers.md 8.1, 11.5).
type Merged struct {
	// Key is the SHA-256 over the layers' digests concatenated in order,
	// so the same layers in the same order share one tree.
	Key Digest
	// Dir is the absolute path of the merged tree, the source of the
	// read-only /opt mount.
	Dir string
}

// Merge builds the merged tree of layers, whose trees PutZip must hold, or
// finds the one already built, and adds a reference to it. Not implemented
// yet: it always returns a *MergeError.
func (*Store) Merge(_ context.Context, _ []Digest) (Merged, error) {
	return Merged{}, &MergeError{Reason: "merging layer trees is not implemented yet"}
}

// ReleaseMerged drops one reference to the merged tree key names and
// removes the tree with the last one. Not implemented yet: it always
// returns a *MergeError.
func (*Store) ReleaseMerged(_ Digest) error {
	return &MergeError{Reason: "releasing merged layer trees is not implemented yet"}
}

// MergeError reports layers Merge cannot merge for a reason other than a
// conflict between them, such as an empty list.
type MergeError struct {
	Reason string
}

func (e *MergeError) Error() string {
	return "codestore: cannot merge the layers into /opt: " + e.Reason
}

// MergeConflictError reports a layer with a file where an earlier layer
// has a directory, or the reverse; nothing is published.
type MergeConflictError struct {
	// Path is slash-separated and relative to /opt.
	Path string
	// Layer is the index, in Merge's argument, of the layer that conflicts.
	Layer int
	// File is true when that layer has a file at Path and the earlier one
	// a directory.
	File bool
}

func (e *MergeConflictError) Error() string {
	has, earlier := "a directory", "a file"
	if e.File {
		has, earlier = earlier, has
	}
	return fmt.Sprintf("codestore: layer %d has %s at %s where an earlier layer has %s; the layers cannot be merged into /opt", e.Layer, has, e.Path, earlier)
}
