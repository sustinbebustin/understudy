// Package codestore stores function code packages and extracts zip archives
// safely, rejecting entries that would escape the target directory.
//
// A Store extracts each zip once per CodeSha256 into trees/<hex digest>
// under its directory, through an os.Root, and counts references: PutZip
// adds one, Release removes one and the last Release deletes the tree. The
// tree is the read-only /var/task bind mount of every environment running
// that code (ARCHITECTURE.md 14.3, containers.md 5.2).
//
// Safety, checked for every entry before anything is written: names must
// satisfy filepath.IsLocal, symbolic links and special files are refused
// (no link is ever created from an archive, because a bind mount follows it
// on the host), and uncompressed sizes are capped per entry and in total
// (262,144,000 bytes by default, Lambda's unzipped limit). The package
// itself is spooled to disk only up to Options.MaxZipped, and an archive
// with more central directory headers than Options.MaxEntries is refused
// before archive/zip parses them, since empty entries pass every size
// limit yet cost memory and an inode each. Extraction
// writes to tmp/ and renames the finished tree into place, so a tree under
// trees/ is always complete. `//go:debug zipinsecurepath=0` in main is a
// second layer; this package does not rely on it.
//
// Decisions the architecture leaves open:
//
//   - Modes are normalized so the function's user can read everything:
//     directories 0755, files 0644, and 0755 for files whose zip mode has
//     any execute bit and for the top-level bootstrap (a zip built on
//     Windows carries no modes, and provided runtimes exec bootstrap).
//   - Open removes trees an earlier process left. Trees are derived from
//     packages the Lambda state keeps; on restart the state re-adds them,
//     and nothing else could hold a reference to an old one.
//   - A file entry whose name repeats an earlier one overwrites it, as
//     unzip does; an entry that needs a directory where a file was written
//     (or the reverse) makes the archive invalid.
//   - A header that understates an entry's size fails the archive:
//     archive/zip refuses the extra bytes and the copy is bounded by the
//     declared size anyway.
package codestore
