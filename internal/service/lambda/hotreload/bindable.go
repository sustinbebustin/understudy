package hotreload

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// BindError is why Bindable will not bind a directory: Message names the
// check and the path, Hint the next step. The caller says what was not
// done, in its own error vocabulary.
type BindError struct {
	Message string
	Hint    string
}

func (e *BindError) Error() string { return e.Message }

// Bindable resolves dir, the directory a hot-reload binding made through an
// API names, to the one the function container binds: an absolute path with
// every symbolic link resolved, to a directory the user understudy runs as
// owns and, when roots is not empty (lambda.hotReloadRoots), under one of
// roots, each root judged by where its own links lead.
//
// On loopback the admin API answers any local user without a token, and the
// Docker daemon binds the directory into a container that reads it as root,
// so without the owner check any local user could read a directory only
// root may, such as /root or /etc, through a function; the roots narrow that
// to directories the operator chose, keeping the understudy user's own home
// out of reach. The resolved path is what is bound, so Docker does not
// follow a link the caller controls. Bindings declared in the configuration
// file are the operator's and are not checked.
func Bindable(dir string, roots []string) (string, *BindError) {
	refuse := func(msg, hint string) (string, *BindError) {
		return "", &BindError{Message: msg, Hint: hint}
	}
	if !filepath.IsAbs(dir) {
		return refuse(fmt.Sprintf("%q is not an absolute path", dir), "give the directory's absolute host path, such as $PWD/build")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return refuse(fmt.Sprintf("%s cannot be resolved on the host: %v", dir, err), "check that the directory exists and that the user understudy runs as can reach it")
	}
	named := resolved
	if resolved != filepath.Clean(dir) {
		named = fmt.Sprintf("%s (where %s leads)", resolved, dir)
	}
	if len(roots) > 0 && !underAny(resolved, roots) {
		return refuse(
			fmt.Sprintf("%s is not under a directory lambda.hotReloadRoots lists (%s)", named, strings.Join(roots, ", ")),
			"build under one of those directories, add the directory to lambda.hotReloadRoots, or declare the binding under lambda.hotReload in the configuration file",
		)
	}
	info, err := os.Lstat(resolved)
	switch {
	case err != nil:
		return refuse(fmt.Sprintf("%s cannot be read on the host: %v", named, err), "check that the user understudy runs as can reach the directory")
	case !info.IsDir():
		return refuse(named+" is not a directory", "give the directory the function's build writes its code to")
	}
	if owner, ok := ownerUID(info); !ok || owner != os.Getuid() {
		return refuse(
			fmt.Sprintf("%s is not owned by the user understudy runs as (uid %d): the function container reads the directory as root, so hot reload binds only directories that user owns", named, os.Getuid()),
			"build into a directory the user running understudy owns, or declare the binding under lambda.hotReload in the configuration file",
		)
	}
	return resolved, nil
}

// underAny reports whether resolved, a path with every link resolved, is
// one of roots or below one. A root that cannot be resolved, such as one
// not created yet, holds nothing that exists.
func underAny(resolved string, roots []string) bool {
	for _, root := range roots {
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(r, resolved); err == nil && filepath.IsLocal(rel) {
			return true
		}
	}
	return false
}

// ownerUID is the uid that owns the file info describes; false where the
// platform reports none.
func ownerUID(info fs.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
