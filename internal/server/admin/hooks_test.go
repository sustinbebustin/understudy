package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
)

// reloads records the directories hot reload binds.
type reloads struct{ dirs []string }

func (r *reloads) HotReload(_ context.Context, _ aws.Scope, _, dir string) *awserr.Error {
	r.dirs = append(r.dirs, dir)
	return nil
}

// hotReload sends POST hot-reload for function f with dir.
func hotReload(k *kerneltest.K, h http.Handler, dir string) *httptest.ResponseRecorder {
	k.Helper()
	body, err := json.Marshal(admin.HotReloadRequest{Dir: dir})
	if err != nil {
		k.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(k.Context(), http.MethodPost, admin.Prefix+"/lambda/functions/f/hot-reload", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

// Hot reload binds a directory the user understudy runs as owns, by its
// path with every link resolved, so Docker never follows a link the
// caller can change. A directory another user owns, or a link to one, is
// refused naming dir and binds nothing: the container reads the directory
// as root, so binding it would hand any local user who reaches the admin
// API files only that owner may read.
func TestHotReloadBindsOnlyDirectoriesTheServerUserOwns(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		hook := &reloads{}
		h := handlerWith(k, func(o *serveradmin.Options) { o.HotReloader = hook })
		own, err := filepath.EvalSymlinks(k.TempDir())
		if err != nil {
			k.Fatal(err)
		}
		link := filepath.Join(k.TempDir(), "build")
		if err := os.Symlink(own, link); err != nil {
			k.Fatal(err)
		}
		if rec := hotReload(k, h, link); rec.Code != http.StatusNoContent || len(hook.dirs) != 1 || hook.dirs[0] != own {
			k.Fatalf("hot reload of a link to an owned directory = %d %s, bound %q; want 204 binding %s", rec.Code, rec.Body, hook.dirs, own)
		}
		file := filepath.Join(own, "bootstrap")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			k.Fatal(err)
		}
		refused := []string{"build", filepath.Join(own, "missing"), file}
		if os.Getuid() != 0 {
			rootLink := filepath.Join(k.TempDir(), "root")
			if err := os.Symlink("/", rootLink); err != nil {
				k.Fatal(err)
			}
			refused = append(refused, "/", rootLink)
		}
		for _, dir := range refused {
			rec := hotReload(k, h, dir)
			if e := envelope(k, rec); rec.Code != http.StatusBadRequest || e.Code != admin.InvalidRequest || e.Field != "dir" || !strings.Contains(e.Message, "nothing was bound") {
				k.Errorf("hot reload of %s = %d %+v; want 400 on dir saying nothing was bound", dir, rec.Code, e)
			}
		}
		if len(hook.dirs) != 1 {
			k.Errorf("bound %q; want only the owned directory", hook.dirs)
		}
	})
}

// With lambda.hotReloadRoots set, hot reload binds only a directory that,
// with every link resolved, lies under one of the roots, a root that is
// itself a link judged by where it leads. A directory elsewhere, or a link
// inside a root that leads out of it, is refused naming the setting and the
// resolved path, even though the user understudy runs as owns it: on
// loopback any local user can call the admin API, so without roots one
// could bind that user's home directory and read it from a function
// (SECURITY.md, "Hot-reload bindings").
func TestHotReloadRootsConfineBindings(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		resolved := func(dir string) string {
			k.Helper()
			r, err := filepath.EvalSymlinks(dir)
			if err != nil {
				k.Fatal(err)
			}
			return r
		}
		symlink := func(target, link string) string {
			k.Helper()
			if err := os.Symlink(target, link); err != nil {
				k.Fatal(err)
			}
			return link
		}
		root := resolved(k.TempDir())
		outside := resolved(k.TempDir())
		under := filepath.Join(root, "worker")
		if err := os.Mkdir(under, 0o700); err != nil {
			k.Fatal(err)
		}
		rootLink := symlink(root, filepath.Join(k.TempDir(), "builds"))
		hook := &reloads{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.HotReloader = hook
			o.HotReloadRoots = []string{rootLink}
		})

		bound := []struct{ dir, want string }{
			{under, under},
			{root, root},
			{filepath.Join(rootLink, "worker"), under},
			{symlink(under, filepath.Join(outside, "into-root")), under},
		}
		for _, c := range bound {
			hook.dirs = nil
			if rec := hotReload(k, h, c.dir); rec.Code != http.StatusNoContent || len(hook.dirs) != 1 || hook.dirs[0] != c.want {
				k.Errorf("hot reload of %s under a root = %d %s, bound %q; want 204 binding %s", c.dir, rec.Code, rec.Body, hook.dirs, c.want)
			}
		}

		hook.dirs = nil
		refused := []string{outside, symlink(outside, filepath.Join(root, "escape"))}
		for _, dir := range refused {
			rec := hotReload(k, h, dir)
			e := envelope(k, rec)
			if rec.Code != http.StatusBadRequest || e.Code != admin.InvalidRequest || e.Field != "dir" {
				k.Errorf("hot reload of %s outside the roots = %d %+v; want 400 on dir", dir, rec.Code, e)
			}
			for _, part := range []string{"lambda.hotReloadRoots", outside, "nothing was bound"} {
				if !strings.Contains(e.Message, part) {
					k.Errorf("refusal of %s = %q; want it to name %q", dir, e.Message, part)
				}
			}
		}
		if len(hook.dirs) != 0 {
			k.Errorf("bound %q; want nothing outside the roots", hook.dirs)
		}
	})
}
