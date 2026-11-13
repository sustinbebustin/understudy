package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/config"
)

// lambda.hotReloadRoots lists the directories a hot-reload binding made
// through the admin API must resolve under. Unset, it confines nothing, as
// before the setting existed; each entry is an absolute path, kept cleaned.
func TestHotReloadRoots(t *testing.T) {
	if got := load(t, nil, nil, files{}).Lambda.HotReloadRoots; got != nil {
		t.Errorf("unset lambda.hotReloadRoots = %q, want nil", got)
	}
	cfg := load(t, nil, nil, files{config.DefaultConfigFile: `{"lambda": {"hotReloadRoots": ["/srv/builds/", "/work/./fn"]}}`})
	if got, want := cfg.Lambda.HotReloadRoots, []string{"/srv/builds", "/work/fn"}; !slices.Equal(got, want) {
		t.Errorf("lambda.hotReloadRoots = %q, want %q", got, want)
	}
	refused := []struct{ file, where, says string }{
		{`{"lambda": {"hotReloadRoots": ["/srv/builds", "relative"]}}`, "understudy.json: lambda.hotReloadRoots[1]", "not an absolute path"},
		// An empty list would confine every binding to nowhere, which is
		// not what leaving the key out means; it is refused rather than
		// read either way.
		{`{"lambda": {"hotReloadRoots": []}}`, "understudy.json: lambda.hotReloadRoots", "no directory"},
	}
	for _, c := range refused {
		_, err := config.Load(nil, nil, files{config.DefaultConfigFile: c.file}.read)
		if err == nil || err.Where != c.where || !strings.Contains(err.Reason, c.says) {
			t.Errorf("Load(%s) = %v, want an error at %q saying %q", c.file, err, c.where, c.says)
		}
	}
}
