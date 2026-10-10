package snapshot

import (
	"io"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/blob"
)

// blobDirName is the directory blob.Store.Import reads blobs from.
const blobDirName = "sha256"

// blobFS presents the pinned blobs of a BlobSource in the sha256/<hex>
// layout blob.Store.Import reads.
type blobFS struct {
	src  BlobSource
	refs []blob.Ref
}

func (f blobFS) Open(name string) (fs.File, error) {
	h, ok := strings.CutPrefix(name, blobDirName+"/")
	if r, err := blob.ParseRef(h); ok && err == nil && slices.Contains(f.refs, r) {
		size, err := f.src.Size(r)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		rc, err := f.src.Export(r)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return blobFile{ReadCloser: rc, info: blobInfo{name: h, size: size}}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (f blobFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != blobDirName {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	out := make([]fs.DirEntry, 0, len(f.refs))
	for _, r := range f.refs {
		size, err := f.src.Size(r)
		if err != nil {
			return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
		}
		out = append(out, fs.FileInfoToDirEntry(blobInfo{name: r.String(), size: size}))
	}
	return out, nil
}

type blobFile struct {
	io.ReadCloser
	info blobInfo
}

func (f blobFile) Stat() (fs.FileInfo, error) { return f.info, nil }

type blobInfo struct {
	name string
	size int64
}

func (i blobInfo) Name() string     { return i.name }
func (i blobInfo) Size() int64      { return i.size }
func (blobInfo) Mode() fs.FileMode  { return 0o444 }
func (blobInfo) ModTime() time.Time { return time.Time{} }
func (blobInfo) IsDir() bool        { return false }
func (blobInfo) Sys() any           { return nil }
