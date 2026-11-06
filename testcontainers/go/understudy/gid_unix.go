//go:build linux || darwin

package understudy

import (
	"io/fs"
	"syscall"
)

// fileGroup is the numeric group owning the file fi describes.
func fileGroup(fi fs.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Gid, true
}
