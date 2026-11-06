//go:build !linux && !darwin

package understudy

import "io/fs"

// fileGroup reports no group: this platform's stat carries none the module
// reads, so WithDockerSocketGroup must name it.
func fileGroup(fs.FileInfo) (uint32, bool) { return 0, false }
