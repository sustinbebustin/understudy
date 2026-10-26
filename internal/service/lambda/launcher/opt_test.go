package launcher_test

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// layerTree is a merged layer tree as codestore builds one: a Python
// module, an executable and their directories, with the modes extraction
// gives them.
func layerTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"python", "bin"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for name, mode := range map[string]fs.FileMode{"python/greet.py": 0o644, "bin/tool": 0o755} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.WriteFile(path, []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{dir, filepath.Join(dir, "python"), filepath.Join(dir, "bin")} {
		if err := os.Chmod(d, 0o755); err != nil { //nolint:gosec // G302: a layer tree's directories are 0755, as codestore extracts them
			t.Fatal(err)
		}
	}
	return dir
}

// withLayers is spec with the merged layer tree opt.
func withLayers(t *testing.T, opt string) launcher.Spec {
	t.Helper()
	s := spec(t)
	code, ok := s.Code.(launcher.ZipCode)
	if !ok {
		t.Fatalf("spec code = %#v, want ZipCode", s.Code)
	}
	code.Opt = opt
	s.Code = code
	return s
}

// Lambda extracts a function's layers into /opt (lambda-destinations-layers.md
// 8.1); understudy mounts the merged tree there read-only beside the code
// at /var/task, under the read-only root (11.5.5, ARCHITECTURE.md 14.3).
func TestStartMountsTheLayerTreeAtOpt(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	opt := layerTree(t)
	s := withLayers(t, opt)
	c, err := l.Start(t.Context(), s, slot())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, _ := d.Container(c.ID)
	want := []docker.Mount{
		tmpVolume,
		{Type: "bind", Source: zipDir(t, s), Target: "/var/task", ReadOnly: true},
		{Type: "bind", Source: opt, Target: "/opt", ReadOnly: true},
	}
	if !slices.Equal(got.HostConfig.Mounts, want) || !got.HostConfig.ReadonlyRootfs {
		t.Errorf("Mounts = %+v, ReadonlyRootfs = %v; want %+v under a read-only root", got.HostConfig.Mounts, got.HostConfig.ReadonlyRootfs, want)
	}
}

// The runtimes search the layer directories themselves: the images put
// /opt/bin on PATH and /opt/lib on LD_LIBRARY_PATH, and the Python runtime
// searches /opt/python (lambda-destinations-layers.md 8.2), so understudy
// sets no search path of its own (11.5.5).
func TestStartWithLayersSetsNoSearchPath(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	s := withLayers(t, layerTree(t))
	s.Environment = map[string]string{"STAGE": "test"}
	c, err := l.Start(t.Context(), s, slot())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, _ := d.Container(c.ID)
	env := envMap(got.Config.Env)
	for _, name := range []string{"PATH", "PYTHONPATH", "NODE_PATH", "LD_LIBRARY_PATH"} {
		if v, set := env[name]; set {
			t.Errorf("Env %s = %q, want it unset: the image and runtime set it", name, v)
		}
	}
}

// tarEntry is one entry of an uploaded archive.
type tarEntry struct {
	typ  byte
	mode int64
	body string
}

// entries reads an uploaded archive, in order, by name without a trailing
// slash, failing on an entry not owned by root.
func entries(t *testing.T, data []byte) (names []string, out map[string]tarEntry) {
	t.Helper()
	out = map[string]tarEntry{}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names, out
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Uid != 0 || hdr.Gid != 0 {
			t.Errorf("%s owned by %d:%d, want root", hdr.Name, hdr.Uid, hdr.Gid)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		names = append(names, name)
		out[name] = tarEntry{typ: hdr.Typeflag, mode: hdr.Mode & 0o7777, body: string(body)}
	}
}

// Where the daemon cannot mount host paths, the code is uploaded to
// /var/task and then the merged layer tree in one archive at /, an opt
// directory first and the tree under it, owned by root with the tree's
// modes, so the image need not have /opt (lambda-destinations-layers.md
// 11.5.5).
func TestStartCopiesTheLayerTreeWhenItCannotMount(t *testing.T) {
	d := newDaemon(t)
	if _, err := d.Client(t).NetworkCreate(t.Context(), "understudy-net", docker.NetworkOptions{}); err != nil {
		t.Fatal(err)
	}
	l := newLauncher(t, d, topology.InContainer{Network: "understudy-net", Self: netip.MustParseAddr("10.9.0.2")})
	c, err := l.Start(t.Context(), withLayers(t, layerTree(t)), slot())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, _ := d.Container(c.ID)
	if !slices.Equal(got.HostConfig.Mounts, []docker.Mount{tmpVolume}) || got.HostConfig.ReadonlyRootfs {
		t.Errorf("Mounts = %+v, ReadonlyRootfs = %v; tar copy needs no bind mounts and a writable root", got.HostConfig.Mounts, got.HostConfig.ReadonlyRootfs)
	}
	uploads := d.Uploads(c.ID)
	paths := make([]string, len(uploads))
	for i, u := range uploads {
		paths[i] = u.Path
	}
	if !slices.Equal(paths, []string{"/var/task", "/"}) {
		t.Fatalf("uploads at %v, want the code at /var/task, then one archive at /", paths)
	}
	names, got2 := entries(t, uploads[1].Data)
	if len(names) == 0 || names[0] != "opt" || got2["opt"].typ != tar.TypeDir {
		t.Fatalf("archive entries = %v, want the opt directory first", names)
	}
	want := map[string]tarEntry{
		"opt":                 {typ: tar.TypeDir, mode: 0o755},
		"opt/python":          {typ: tar.TypeDir, mode: 0o755},
		"opt/python/greet.py": {typ: tar.TypeReg, mode: 0o644, body: "python/greet.py\n"},
		"opt/bin":             {typ: tar.TypeDir, mode: 0o755},
		"opt/bin/tool":        {typ: tar.TypeReg, mode: 0o755, body: "bin/tool\n"},
	}
	if !maps.Equal(got2, want) {
		t.Errorf("archive = %+v, want %+v", got2, want)
	}
}

// Without layers the image's /opt is left alone: no mount and no upload
// for it.
func TestStartWithoutLayersTouchesNoOpt(t *testing.T) {
	d := newDaemon(t)
	if _, err := d.Client(t).NetworkCreate(t.Context(), "understudy-net", docker.NetworkOptions{}); err != nil {
		t.Fatal(err)
	}
	l := newLauncher(t, d, topology.InContainer{Network: "understudy-net", Self: netip.MustParseAddr("10.9.0.2")})
	c, err := l.Start(t.Context(), spec(t), slot())
	if err != nil {
		t.Fatal(err)
	}
	if uploads := d.Uploads(c.ID); len(uploads) != 1 || uploads[0].Path != "/var/task" {
		t.Errorf("%d uploads, want the code alone at /var/task", len(uploads))
	}
}

// A layer tree is a host path the daemon mounts, so it must be absolute,
// as the code directory must.
func TestStartRefusesARelativeLayerDirectory(t *testing.T) {
	d := newDaemon(t)
	l := newLauncher(t, d, bridge())
	_, err := l.Start(t.Context(), withLayers(t, "merged/abc"), slot())
	se, ok := errors.AsType[*launcher.SpecError](err)
	if !ok || se.Field != "Code" || se.Reason != `layer directory "merged/abc" is not absolute` {
		t.Errorf("Start = %v, want a *SpecError for Code saying the layer directory is not absolute", err)
	}
	if len(d.Created()) != 0 {
		t.Errorf("an invalid spec created %d containers", len(d.Created()))
	}
}
