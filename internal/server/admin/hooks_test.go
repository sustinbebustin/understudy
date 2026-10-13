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
