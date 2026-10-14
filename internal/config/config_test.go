package config_test

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/config"
)

// files is a fake filesystem for Load: the config file and the container
// markers.
type files map[string]string

func (f files) read(path string) ([]byte, error) {
	data, ok := f[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return []byte(data), nil
}

// The network sysctls /proc/sys/net shows: somaxconn in every network
// namespace, netdev_max_backlog only in the host's.
const (
	somaxconn        = "/proc/sys/net/core/somaxconn"
	netdevMaxBacklog = "/proc/sys/net/core/netdev_max_backlog"
)

// inContainer holds the marker Docker puts in every container, in a network
// namespace of its own.
func inContainer() files { return files{"/.dockerenv": "", somaxconn: "4096"} }

// token is an admin token long enough to guard a listener beyond loopback.
const token = "s3cret-s3cret-s3cret"

func load(t *testing.T, args, env []string, f files) config.Config {
	t.Helper()
	cfg, err := config.Load(args, env, f.read)
	if err != nil {
		t.Fatalf("Load(%q, %q) = %v", args, env, err)
	}
	return cfg
}

func loadErr(t *testing.T, args, env []string, f files) *config.Error {
	t.Helper()
	_, err := config.Load(args, env, f.read)
	if err == nil {
		t.Fatalf("Load(%q, %q) succeeded, want an error", args, env)
	}
	return err
}

// Each of three settings takes its value from the highest source that sets
// it: flag over UNDERSTUDY_* over the file over the default.
func TestPrecedence(t *testing.T) {
	file := `{"listen": "127.0.0.1:3000", "seed": 3, "logLevel": "warn"}`
	cases := []struct {
		name   string
		args   []string
		env    []string
		file   string
		listen string
		seed   *uint64
		level  slog.Level
	}{
		{name: "default", listen: "127.0.0.1:4566", level: slog.LevelInfo},
		{name: "file", file: file, listen: "127.0.0.1:3000", seed: new(uint64(3)), level: slog.LevelWarn},
		{
			name: "env beats file", file: file,
			env:    []string{"UNDERSTUDY_LISTEN=127.0.0.1:2000", "UNDERSTUDY_SEED=2", "UNDERSTUDY_LOG_LEVEL=error"},
			listen: "127.0.0.1:2000", seed: new(uint64(2)), level: slog.LevelError,
		},
		{
			name: "flag beats env", file: file,
			env:    []string{"UNDERSTUDY_LISTEN=127.0.0.1:2000", "UNDERSTUDY_SEED=2", "UNDERSTUDY_LOG_LEVEL=error"},
			args:   []string{"--listen", "127.0.0.1:1000", "--seed=1", "--log-level", "debug"},
			listen: "127.0.0.1:1000", seed: new(uint64(1)), level: slog.LevelDebug,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := files{}
			if c.file != "" {
				f[config.DefaultConfigFile] = c.file
			}
			cfg := load(t, c.args, c.env, f)
			if got := cfg.Listen.AddrPort().String(); got != c.listen {
				t.Errorf("listen = %s, want %s", got, c.listen)
			}
			if !reflect.DeepEqual(cfg.Seed, c.seed) {
				t.Errorf("seed = %v, want %v", cfg.Seed, c.seed)
			}
			if cfg.Log.Level != c.level {
				t.Errorf("log level = %v, want %v", cfg.Log.Level, c.level)
			}
		})
	}
}

// --config and UNDERSTUDY_CONFIG name the file; a named file that is
// missing is an error, while a missing understudy.json is no file.
func TestConfigFileLocation(t *testing.T) {
	f := files{"/etc/u.json": `{"seed": 7}`, "/etc/v.json": `{"seed": 8}`}
	if cfg := load(t, []string{"--config", "/etc/u.json"}, []string{"UNDERSTUDY_CONFIG=/etc/v.json"}, f); *cfg.Seed != 7 {
		t.Errorf("--config over UNDERSTUDY_CONFIG: seed = %d, want 7", *cfg.Seed)
	}
	if cfg := load(t, nil, []string{"UNDERSTUDY_CONFIG=/etc/v.json"}, f); *cfg.Seed != 8 {
		t.Errorf("UNDERSTUDY_CONFIG: seed = %d, want 8", *cfg.Seed)
	}
	err := loadErr(t, []string{"--config", "/missing.json"}, nil, f)
	if err.Where != "--config" || !strings.Contains(err.Reason, "/missing.json") {
		t.Errorf("missing --config file = %v", err)
	}
}

// A section of the wrong JSON shape says the shape it needs, keys
// included, and a fault rule that does not parse names its member in the
// rule's terms rather than the decoder's Go types.
func TestWrongShapesSayWhatIsExpected(t *testing.T) {
	cases := []struct{ file, where, reason string }{
		{
			`{"lambda": {"hotReload": {"function": "f", "dir": "/x"}}}`, "understudy.json: lambda.hotReload",
			`is a JSON object; expected an array of {"function": ..., "dir": ..., "account": ..., "region": ...}`,
		},
		{`{"regions": "eu-west-9"}`, "understudy.json: regions", "is a JSON string; expected an array of strings"},
		{
			`{"faults": [{"phase": "request", "match": {"operations": "sqs.SendMessage"}, "action": {"kind": "throttle"}, "trigger": {"kind": "always"}}]}`,
			"understudy.json: faults[0]", "match.operations is a JSON string; expected an array",
		},
	}
	for _, c := range cases {
		err := loadErr(t, nil, nil, files{config.DefaultConfigFile: c.file})
		if err.Where != c.where || err.Reason != c.reason {
			t.Errorf("%s: error at %q: %q, want at %q: %q", c.file, err.Where, err.Reason, c.where, c.reason)
		}
	}
}

// An unknown key, at the top or inside a section, is refused with the
// closest valid key at that depth.
func TestUnknownKeysNameTheClosest(t *testing.T) {
	cases := []struct{ file, where, closest string }{
		{`{"lisen": "127.0.0.1:1"}`, "understudy.json: lisen", "listen"},
		{`{"s3": {"crossRegon": "aws"}}`, "understudy.json: s3.crossRegon", "crossRegion"},
		{`{"lambda": {"hotReload": [{"function": "f", "dirr": "/x"}]}}`, "understudy.json: lambda.hotReload[0].dirr", "dir"},
	}
	for _, c := range cases {
		err := loadErr(t, nil, nil, files{config.DefaultConfigFile: c.file})
		if err.Where != c.where || err.Closest != c.closest {
			t.Errorf("%s: error at %q closest %q, want %q closest %q (%v)", c.file, err.Where, err.Closest, c.where, c.closest, err)
		}
	}
	err := loadErr(t, []string{"--lissen", "127.0.0.1:1"}, nil, files{})
	if err.Closest != "--listen" {
		t.Errorf("--lissen: closest %q, want --listen (%v)", err.Closest, err)
	}
	err = loadErr(t, nil, []string{"UNDERSTUDY_LISTN=127.0.0.1:1"}, files{})
	if err.Closest != "UNDERSTUDY_LISTEN" {
		t.Errorf("UNDERSTUDY_LISTN: closest %q, want UNDERSTUDY_LISTEN (%v)", err.Closest, err)
	}
}

// A malformed flag is refused in words that name the fix, not in the flag
// package's ("flag needs an argument: -listen", "bad flag syntax"), with
// serve's help as the hint.
func TestMalformedFlagsSayWhatToFix(t *testing.T) {
	cases := []struct {
		args          []string
		where, reason string
	}{
		{[]string{"--listen"}, "--listen", "needs a value, as in --listen=VALUE or --listen VALUE"},
		{[]string{"--tls", "-seed"}, "--seed", "needs a value, as in --seed=VALUE or --seed VALUE"},
		{[]string{"---listen", "127.0.0.1:1"}, "flags", `"---listen" is not a flag: a flag starts with - or --, such as --listen`},
	}
	for _, c := range cases {
		err := loadErr(t, c.args, nil, files{})
		if err.Where != c.where || err.Reason != c.reason || err.Hint != config.HelpHint {
			t.Errorf("Load(%q) = %q at %q hint %q, want %q at %q hint %q", c.args, err.Reason, err.Where, err.Hint, c.reason, c.where, config.HelpHint)
		}
	}
	// A switch needs no value, and a value may follow = or the flag.
	load(t, []string{"--tls"}, nil, files{})
	load(t, []string{"--seed=1", "--listen", "127.0.0.1:1"}, nil, files{})
}

// A name too far from every valid one to be a typo of it is refused without
// a suggestion, rather than pointed at an unrelated setting; a value still
// names the values it could be.
func TestUnrelatedNamesSuggestNothing(t *testing.T) {
	cases := []struct {
		args []string
		env  []string
		f    files
	}{
		{args: []string{"--bogus"}, f: files{}},
		{env: []string{"UNDERSTUDY_X=1"}, f: files{}},
		{f: files{config.DefaultConfigFile: `{"verbosity": 1}`}},
		{f: files{config.DefaultConfigFile: `{"s3": {"zzz": 1}}`}},
	}
	for _, c := range cases {
		err := loadErr(t, c.args, c.env, c.f)
		if err.Closest != "" || strings.Contains(err.Error(), "closest") {
			t.Errorf("Load(%q, %q) = %v, want no suggestion", c.args, c.env, err)
		}
	}
	err := loadErr(t, nil, nil, files{config.DefaultConfigFile: `{"s3": {"crossRegion": "nowhere"}}`})
	if err.Closest != "" || !strings.Contains(err.Hint, "aws or lenient") {
		t.Errorf("s3.crossRegion nowhere = %v, want no suggestion and the values named", err)
	}
}

// A listener beyond loopback without a token is refused on a host, with
// both ways out and the loopback fix named.
func TestNonLoopbackNeedsAToken(t *testing.T) {
	err := loadErr(t, []string{"--listen", "0.0.0.0:4566"}, nil, files{})
	for _, want := range []string{"UNDERSTUDY_ADMIN_TOKEN", "container-published", "--listen 127.0.0.1:4566"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	// The token guards only the admin API, so the open AWS API must be
	// accepted as well, and that acceptance alone opens nothing.
	err = loadErr(t, []string{"--listen", "0.0.0.0:4566"}, []string{"UNDERSTUDY_ADMIN_TOKEN=" + token}, files{})
	if err.Where != "UNDERSTUDY_ADMIN_TOKEN" || !strings.Contains(err.Hint, "--allow-unauthenticated-aws-api") {
		t.Errorf("a token without --allow-unauthenticated-aws-api = %v, want it refused naming the flag", err)
	}
	err = loadErr(t, []string{"--listen", "127.0.0.1:4566", "--allow-unauthenticated-aws-api"}, nil, files{})
	if err.Where != "--allow-unauthenticated-aws-api" {
		t.Errorf("--allow-unauthenticated-aws-api without a token = %v, want it refused", err)
	}
	cfg := load(t, []string{"--listen", "0.0.0.0:4566"}, []string{"UNDERSTUDY_ADMIN_TOKEN=" + token, "UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API=true"}, files{})
	exposed, ok := cfg.Listen.(config.Exposed)
	if !ok {
		t.Fatalf("listen = %#v, want Exposed", cfg.Listen)
	}
	if tok, ok := exposed.Auth.(config.Token); !ok || tok.Secret != token {
		t.Errorf("auth = %#v, want the token", exposed.Auth)
	}
	if strings.Contains(fmtGo(cfg.Listen), token) {
		t.Errorf("the token leaks through %%#v: %s", fmtGo(cfg.Listen))
	}
}

// Nothing rate-limits guesses at a listener beyond loopback, so its token
// must be at least 16 characters; the refusal does not echo the token.
func TestExposedTokenNeedsSixteenCharacters(t *testing.T) {
	args := []string{"--listen", "0.0.0.0:4566"}
	for _, short := range []string{"Q7", "s3cret", "0123456789abcde"} {
		env := []string{"UNDERSTUDY_ADMIN_TOKEN=" + short, "UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API=true"}
		err := loadErr(t, args, env, files{})
		if err.Where != "UNDERSTUDY_ADMIN_TOKEN" || !strings.Contains(err.Reason, "16 characters") || !strings.Contains(err.Hint, "openssl rand -hex 32") {
			t.Errorf("a %d-character token beyond loopback = %v, want it refused asking for 16", len(short), err)
		}
		if strings.Contains(err.Error(), short) {
			t.Errorf("the refusal of a short token echoes it: %v", err)
		}
	}
	env := []string{"UNDERSTUDY_ADMIN_TOKEN=0123456789abcdef", "UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API=true"}
	if cfg := load(t, args, env, files{}); cfg.Listen != (config.Exposed{Addr: netip.MustParseAddrPort("0.0.0.0:4566"), Auth: config.Token{Secret: "0123456789abcdef"}}) {
		t.Errorf("a 16-character token = %#v, want it to guard the listener", cfg.Listen)
	}
}

// ContainerPublished is accepted only where the container markers say
// understudy runs in a container, and is the default listener there.
func TestContainerPublishedOnlyInAContainer(t *testing.T) {
	args := []string{"--listen", "0.0.0.0:4566", "--listen-auth", "container-published"}
	err := loadErr(t, args, nil, files{})
	if err.Where != "--listen-auth" || !strings.Contains(err.Reason, "not running in a container") {
		t.Errorf("container-published on a host = %v", err)
	}
	want := config.Exposed{Addr: netip.MustParseAddrPort("0.0.0.0:4566"), Auth: config.ContainerPublished{}}
	if cfg := load(t, args, nil, inContainer()); cfg.Listen != want {
		t.Errorf("container-published in a container = %#v", cfg.Listen)
	}
	if cfg := load(t, nil, nil, inContainer()); cfg.Listen != want {
		t.Errorf("default listener in a container = %#v, want %#v", cfg.Listen, want)
	}
	if cfg := load(t, nil, nil, files{"/proc/1/cgroup": "0::/system.slice/containerd.service", somaxconn: "4096"}); cfg.Listen != want {
		t.Errorf("default listener under a containerd cgroup = %#v", cfg.Listen)
	}
	if cfg := load(t, []string{"--listen", "127.0.0.1:0"}, nil, inContainer()); cfg.Listen != (config.Loopback{Addr: netip.MustParseAddrPort("127.0.0.1:0")}) {
		t.Errorf("loopback in a container = %#v", cfg.Listen)
	}
}

// A container no published port guards (host networking, a namespace
// /proc/sys/net cannot confirm, a Kubernetes pod) defaults to loopback with
// a warning, refuses container-published, and listens beyond loopback only
// with a token.
func TestContainerPublishedNeedsAPublishedPort(t *testing.T) {
	cases := []struct {
		name   string
		env    []string
		f      files
		reason string
	}{
		{"host network", nil, files{"/.dockerenv": "", somaxconn: "4096", netdevMaxBacklog: "1000"}, "host's network namespace"},
		{"unreadable /proc/sys/net", nil, files{"/run/.containerenv": ""}, "host's network namespace"},
		{"kubepods cgroup", nil, files{"/proc/1/cgroup": "0::/kubepods/besteffort/pod1/abc", somaxconn: "4096"}, "Kubernetes pod"},
		{"KUBERNETES_SERVICE_HOST", []string{"KUBERNETES_SERVICE_HOST=10.96.0.1"}, inContainer(), "Kubernetes pod"},
	}
	for _, c := range cases {
		cfg := load(t, nil, c.env, c.f)
		if cfg.Listen != (config.Loopback{Addr: netip.MustParseAddrPort("127.0.0.1:4566")}) {
			t.Errorf("%s: default listener = %#v, want loopback", c.name, cfg.Listen)
		}
		if len(cfg.Warnings) != 1 || cfg.Warnings[0].Setting != "--listen" || !strings.Contains(cfg.Warnings[0].Message, c.reason) ||
			!strings.Contains(cfg.Warnings[0].Fix, "UNDERSTUDY_ADMIN_TOKEN") {
			t.Errorf("%s: warnings %+v, want one on --listen naming %q", c.name, cfg.Warnings, c.reason)
		}
		args := []string{"--listen", "0.0.0.0:4566", "--listen-auth", "container-published"}
		if err := loadErr(t, args, c.env, c.f); err.Where != "--listen-auth" || !strings.Contains(err.Reason, c.reason) {
			t.Errorf("%s: container-published = %v, want it refused naming %q", c.name, err, c.reason)
		}
		if err := loadErr(t, args[:2], c.env, c.f); !strings.Contains(err.Reason, "without an admin token") {
			t.Errorf("%s: 0.0.0.0 without a token = %v, want it refused", c.name, err)
		}
		env := append([]string{"UNDERSTUDY_ADMIN_TOKEN=" + token, "UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API=1"}, c.env...)
		if cfg := load(t, args[:2], env, c.f); cfg.Listen != (config.Exposed{Addr: netip.MustParseAddrPort("0.0.0.0:4566"), Auth: config.Token{Secret: token}}) {
			t.Errorf("%s: 0.0.0.0 with a token = %#v", c.name, cfg.Listen)
		}
	}
	if cfg := load(t, []string{"--listen", "127.0.0.1:4566"}, nil, files{"/.dockerenv": ""}); len(cfg.Warnings) != 0 {
		t.Errorf("an explicit loopback listener warns: %+v", cfg.Warnings)
	}
}

// Every refused value names its setting as its source spells it.
func TestRefusedValues(t *testing.T) {
	cases := []struct {
		args  []string
		env   []string
		file  string
		where string
	}{
		{args: []string{"--clock", "slow"}, where: "--clock"},
		{args: []string{"--clock", "frozen@yesterday"}, where: "--clock"},
		{args: []string{"--seed", "-1"}, where: "--seed"},
		{args: []string{"--clock-scale", "0"}, where: "--clock-scale"},
		{args: []string{"--sigv4", "maybe"}, where: "--sigv4"},
		{args: []string{"--log-format", "xml"}, where: "--log-format"},
		{args: []string{"--log-format", "text", "--output", "json"}, where: "--output"},
		{args: []string{"--persist-every", "1m"}, where: "--persist-every"},
		{args: []string{"--docker", "tcp://1.2.3.4:2375"}, where: "--docker"},
		{args: []string{"--listen", "127.0.0.1:0"}, env: []string{"UNDERSTUDY_ADMIN_TOKEN=x"}, where: "UNDERSTUDY_ADMIN_TOKEN"},
		{args: []string{"extra"}, where: "arguments"},
		// The DynamoDB Local licence acceptance was removed before any
		// release; like any unknown setting, it is refused, not ignored.
		{env: []string{"UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1"}, where: "UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE"},
		{args: []string{"--accept-dynamodb-local-license"}, where: "--accept-dynamodb-local-license"},
		{file: `{"s3": {"baseDomains": ["example.com"]}}`, where: "understudy.json: s3.baseDomains[0]"},
		{file: `{"s3": {"maxObjectBytes": "big"}}`, where: "understudy.json: s3.maxObjectBytes"},
		{file: `{"lambda": {"network": "bridge"}}`, where: "understudy.json: lambda.bridgeGateway"},
		{file: `{"lambda": {"network": "bridge", "bridgeGateway": "fd00::1"}}`, where: "understudy.json: lambda.bridgeGateway"},
		{file: `{"lambda": {"hotReload": [{"function": "f", "dir": "relative"}]}}`, where: "understudy.json: lambda.hotReload[0].dir"},
		{file: `{"lambda": {"hotReload": [{"function": "f", "dir": "/src", "account": "12"}]}}`, where: "understudy.json: lambda.hotReload[0].account"},
		{file: `{"lambda": {"hotReload": [{"function": "f", "dir": "/src", "region": "Nope"}]}}`, where: "understudy.json: lambda.hotReload[0].region"},
		{file: `{"lambda": {"hotReload": [{"function": "f", "dir": "/src", "region": "local-2"}]}, "regions": ["local-1"]}`, where: "understudy.json: lambda.hotReload[0].region"},
		{file: `{"acceptDynamodbLocalLicense": true}`, where: "understudy.json: acceptDynamodbLocalLicense"},
		{file: `{"faults": [{"phase": "request", "action": {"kind": "explode"}, "trigger": {"kind": "always"}}]}`, where: "understudy.json: faults[0]"},
		{file: `{"accounts": {"default": "12"}}`, where: "understudy.json: accounts.default"},
		{file: `{"seed": {"n": 1}}`, where: "understudy.json: seed"},
		{file: `[1]`, where: "understudy.json"},
	}
	for _, c := range cases {
		f := files{}
		if c.file != "" {
			f[config.DefaultConfigFile] = c.file
		}
		_, err := config.Load(c.args, c.env, f.read)
		if err == nil || err.Where != c.where {
			t.Errorf("Load(%q, %q, %s) = %v, want an error at %q", c.args, c.env, c.file, err, c.where)
		}
	}
}

// snapshotMaxBytes caps every snapshot archive at 1 GiB by default; the
// flag, the variable and the file each set it, from 1 MiB to 4 GiB
// inclusive, as a base-10 count of bytes. Anything else is refused naming
// the setting, the value and the range.
func TestSnapshotMaxBytes(t *testing.T) {
	if got := load(t, nil, nil, files{}).Snapshot.MaxBytes; got != 1<<30 {
		t.Errorf("default snapshotMaxBytes = %d, want 1073741824", got)
	}
	sources := map[string]func(text string) ([]string, []string, files){
		"--snapshot-max-bytes": func(text string) ([]string, []string, files) {
			return []string{"--snapshot-max-bytes", text}, nil, files{}
		},
		"UNDERSTUDY_SNAPSHOT_MAX_BYTES": func(text string) ([]string, []string, files) {
			return nil, []string{"UNDERSTUDY_SNAPSHOT_MAX_BYTES=" + text}, files{}
		},
		"understudy.json: snapshotMaxBytes": func(text string) ([]string, []string, files) {
			return nil, nil, files{config.DefaultConfigFile: `{"snapshotMaxBytes": ` + text + `}`}
		},
	}
	for where, source := range sources {
		for _, accepted := range []int64{1 << 20, 64 << 20, config.SnapshotMaxBytesCeiling} {
			args, env, f := source(strconv.FormatInt(accepted, 10))
			if got := load(t, args, env, f).Snapshot.MaxBytes; got != accepted {
				t.Errorf("%s %d: snapshotMaxBytes = %d", where, accepted, got)
			}
		}
		for _, refused := range []string{"1048575", "4294967297", "0", "-1", "1e9", `"1GiB"`} {
			args, env, f := source(refused)
			err := loadErr(t, args, env, f)
			if err.Where != where || !strings.Contains(err.Reason, strings.Trim(refused, `"`)) ||
				!strings.Contains(err.Reason, "1048576 (1 MiB)") || !strings.Contains(err.Reason, "4294967296 (4 GiB)") {
				t.Errorf("%s %s = %v; want it refused at %s naming the value and the range", where, refused, err, where)
			}
		}
	}
}

// The container listener binds lambda.bridgeGateway and serves the AWS API
// unchecked, so only a private IPv4 address, where Docker puts its bridges,
// is accepted: never every interface, loopback or a routable address.
func TestBridgeGatewayMustBePrivate(t *testing.T) {
	refused := map[string]string{
		"0.0.0.0":     "every interface",
		"127.0.0.1":   "loopback",
		"203.0.113.7": "private",
		"100.64.0.1":  "private",
		"169.254.0.1": "private",
	}
	for gw, reason := range refused {
		f := files{config.DefaultConfigFile: `{"lambda": {"network": "bridge", "bridgeGateway": "` + gw + `"}}`}
		err := loadErr(t, nil, nil, f)
		if err.Where != "understudy.json: lambda.bridgeGateway" || !strings.Contains(err.Reason, reason) || !strings.Contains(err.Hint, "network\": \"auto") {
			t.Errorf("bridgeGateway %s = %v, want it refused naming %q", gw, err, reason)
		}
	}
	for _, gw := range []string{"172.17.0.1", "10.89.0.1", "192.168.240.1"} {
		f := files{config.DefaultConfigFile: `{"lambda": {"network": "bridge", "bridgeGateway": "` + gw + `"}}`}
		if cfg := load(t, nil, nil, f); cfg.Lambda.Network != (config.NetworkBridge{Gateway: netip.MustParseAddr(gw)}) {
			t.Errorf("bridgeGateway %s = %#v, want it accepted", gw, cfg.Lambda.Network)
		}
	}
}

// A file can set every section; the result is typed.
func TestFileSections(t *testing.T) {
	file := `{
		"persist": "/data", "persistEvery": "1m", "tls": true,
		"clock": "frozen@2026-01-01T00:00:00Z",
		"accounts": {"default": "111111111111", "keys": [{"accessKeyId": "LOCALKEY00000001", "secretAccessKey": "sk", "account": "222222222222"}]},
		"regions": ["local-1"],
		"s3": {"baseDomains": ["localhost.localstack.cloud", "s3.dev.localhost"], "crossRegion": "lenient"},
		"lambda": {"network": "bridge", "bridgeGateway": "172.17.0.1", "idleTimeout": "2m", "stateTransitions": "clock",
		           "hotReload": [{"function": "worker", "dir": "/src/worker"}, {"function": "edge", "dir": "/src/edge", "account": "333333333333", "region": "local-1"}]},
		"sqs": {"queueDeletedRecently": "enforce"},
		"iam": {"assumeRole": "lenient"},
		"dynamodb": {"registry": "ecr-public", "memoryLimit": 536870912},
		"journal": {"capacity": 10, "file": "/tmp/j.ndjson"},
		"faults": [{"id": "slow", "phase": "request", "match": {"operations": ["sqs.SendMessage"]}, "action": {"kind": "throttle"}, "trigger": {"kind": "always"}}]
	}`
	cfg := load(t, nil, nil, files{config.DefaultConfigFile: file})
	checks := []struct {
		name      string
		got, want any
	}{
		{"persistence", cfg.Persistence, config.Dir{Path: "/data", SaveEvery: time.Minute}},
		{"tls", cfg.TLS, config.SelfSigned{Listen: netip.MustParseAddrPort("127.0.0.1:4567")}},
		{"clock", cfg.Clock, config.Frozen{At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"default account", cfg.Accounts.Default.String(), "111111111111"},
		{"key account", cfg.Accounts.Keys[0].Account.String(), "222222222222"},
		{"regions", cfg.Regions[0].String(), "local-1"},
		{"base domains", cfg.S3.BaseDomains, []string{"localhost", "s3.localhost", "localhost.localstack.cloud", "s3.dev.localhost"}},
		{"cross region", cfg.S3.CrossRegion, config.CrossRegionLenient},
		{"network", cfg.Lambda.Network, config.NetworkBridge{Gateway: netip.MustParseAddr("172.17.0.1")}},
		{"idle", cfg.Lambda.IdleTimeout, 2 * time.Minute},
		{"transitions", cfg.Lambda.StateTransitions, config.TransitionsClock},
		{"hot reload", cfg.Lambda.HotReload, []config.HotReload{
			{Function: "worker", Dir: "/src/worker"},
			{Function: "edge", Dir: "/src/edge", Account: mustAccount(t, "333333333333"), Region: cfg.Regions[0]},
		}},
		{"sqs", cfg.SQS.DeletedRecently, config.DeletedRecentlyEnforce},
		{"iam", cfg.IAM.AssumeRole, config.AssumeRoleLenient},
		{"dynamodb", cfg.DynamoDB.Mode, config.DynamoDBEnabled{}},
		{"registry", cfg.DynamoDB.Registry, config.RegistryECRPublic},
		{"memory", cfg.DynamoDB.MemoryLimit, int64(512 << 20)},
		{"journal", cfg.Journal, config.Journal{Capacity: 10, MaxBytes: config.DefaultJournalMaxBytes, File: "/tmp/j.ndjson"}},
		{"faults", string(cfg.Faults[0].ID), "slow"},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
		}
	}
}

func mustAccount(t *testing.T, s string) aws.AccountID {
	t.Helper()
	a, err := aws.ParseAccountID(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// The TLS listener follows an ephemeral main port, and --tls-listen
// implies --tls.
func TestTLSAddress(t *testing.T) {
	cfg := load(t, []string{"--listen", "127.0.0.1:0", "--tls"}, nil, files{})
	if cfg.TLS != (config.SelfSigned{Listen: netip.MustParseAddrPort("127.0.0.1:0")}) {
		t.Errorf("tls with an ephemeral main port = %#v", cfg.TLS)
	}
	cfg = load(t, []string{"--tls-listen", "127.0.0.1:9443"}, nil, files{})
	if cfg.TLS != (config.SelfSigned{Listen: netip.MustParseAddrPort("127.0.0.1:9443")}) {
		t.Errorf("--tls-listen = %#v", cfg.TLS)
	}
	if cfg := load(t, nil, nil, files{}); cfg.TLS != (config.TLSOff{}) {
		t.Errorf("default TLS = %#v", cfg.TLS)
	}
}

// Docker comes from --docker, else DOCKER_HOST, else the default socket.
func TestDocker(t *testing.T) {
	if cfg := load(t, nil, nil, files{}); cfg.Docker != (config.DockerSocket{Path: config.DefaultDockerSocket}) {
		t.Errorf("default docker = %#v", cfg.Docker)
	}
	if cfg := load(t, nil, []string{"DOCKER_HOST=unix:///run/user/1000/docker.sock"}, files{}); cfg.Docker != (config.DockerSocket{Path: "/run/user/1000/docker.sock"}) {
		t.Errorf("DOCKER_HOST = %#v", cfg.Docker)
	}
	if cfg := load(t, []string{"--docker", "disabled"}, []string{"DOCKER_HOST=tcp://x:1"}, files{}); cfg.Docker != (config.DockerDisabled{}) || len(cfg.Warnings) != 0 {
		t.Errorf("--docker disabled = %#v, warnings %q", cfg.Docker, cfg.Warnings)
	}
}

// DynamoDB runs by default; dynamodb.enabled false in the file turns it
// off.
func TestDynamoDBMode(t *testing.T) {
	for _, tc := range []struct {
		file string
		want config.DynamoDBMode
	}{
		{"", config.DynamoDBEnabled{}},
		{`{"dynamodb": {"enabled": true}}`, config.DynamoDBEnabled{}},
		{`{"dynamodb": {"enabled": false}}`, config.DynamoDBDisabled{}},
	} {
		f := files{}
		if tc.file != "" {
			f[config.DefaultConfigFile] = tc.file
		}
		if got := load(t, nil, nil, f).DynamoDB.Mode; got != tc.want {
			t.Errorf("DynamoDB mode with file %q = %#v, want %#v", tc.file, got, tc.want)
		}
	}
}

// A DOCKER_HOST understudy cannot speak to, such as docker-in-docker's
// tcp://, turns containers off with a warning naming UNDERSTUDY_DOCKER
// instead of refusing to serve.
func TestUnusableDockerHostWarns(t *testing.T) {
	for _, host := range []string{"tcp://docker:2375", "ssh://user@remote", "unix://relative.sock"} {
		cfg := load(t, nil, []string{"DOCKER_HOST=" + host}, files{})
		if cfg.Docker != (config.DockerDisabled{}) || len(cfg.Warnings) != 1 || cfg.Warnings[0].Setting != config.DockerHostEnv ||
			!strings.Contains(cfg.Warnings[0].Message, host) || !strings.Contains(cfg.Warnings[0].Fix, "UNDERSTUDY_DOCKER") {
			t.Errorf("DOCKER_HOST=%s: docker %#v, warnings %+v", host, cfg.Docker, cfg.Warnings)
		}
	}
}

// --clock and --clock-scale combine into one start mode: a scale alone runs
// the clock that fast, and a scale with a frozen clock is refused naming
// both settings.
func TestClockStart(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		args []string
		env  []string
		want config.ClockStart
	}{
		{want: config.Running{}},
		{args: []string{"--clock", "frozen"}, want: config.Frozen{}},
		{args: []string{"--clock", "frozen@2026-01-01T00:00:00Z"}, want: config.Frozen{At: at}},
		{args: []string{"--clock-scale", "60"}, want: config.Scaled{Factor: 60}},
		{args: []string{"--clock", "running", "--clock-scale", "2.5"}, want: config.Scaled{Factor: 2.5}},
		{args: []string{"--clock", "running"}, env: []string{"UNDERSTUDY_CLOCK_SCALE=60"}, want: config.Scaled{Factor: 60}},
	}
	for _, c := range cases {
		if cfg := load(t, c.args, c.env, files{}); cfg.Clock != c.want {
			t.Errorf("Load(%q, %q) clock = %#v, want %#v", c.args, c.env, cfg.Clock, c.want)
		}
	}
	err := loadErr(t, []string{"--clock", "frozen", "--clock-scale", "60"}, nil, files{})
	if err.Where != "--clock-scale" || !strings.Contains(err.Reason, "--clock") {
		t.Errorf("frozen and scaled = %v, want the scale refused naming --clock", err)
	}
	err = loadErr(t, nil, []string{"UNDERSTUDY_CLOCK=frozen"}, files{config.DefaultConfigFile: `{"clockScale": 60}`})
	if !strings.Contains(err.Error(), "UNDERSTUDY_CLOCK") || !strings.Contains(err.Error(), "clockScale") {
		t.Errorf("frozen by variable, scaled by file = %v, want both named", err)
	}
}

// LoadHost resolves the machine settings without serve's listener rules: an
// admin token, the CLI's own credential, does not make it refuse.
func TestLoadHostIgnoresListenerRules(t *testing.T) {
	env := []string{"UNDERSTUDY_ADMIN_TOKEN=s3cret", "UNDERSTUDY_PERSIST=/data", "UNDERSTUDY_DOCKER=disabled"}
	if _, err := config.Load(nil, env, files{}.read); err == nil || err.Where != "UNDERSTUDY_ADMIN_TOKEN" {
		t.Fatalf("Load with a token on loopback = %v, want the token refused", err)
	}
	host, err := config.LoadHost(nil, env, files{}.read)
	if err != nil {
		t.Fatalf("LoadHost = %v", err)
	}
	want := config.Host{Persistence: config.Dir{Path: "/data", SaveEvery: config.DefaultSaveEvery}, Docker: config.DockerDisabled{}, DynamoDB: config.DynamoDBEnabled{}}
	if diff := cmp.Diff(want, host); diff != "" {
		t.Errorf("LoadHost mismatch (-want +got):\n%s", diff)
	}
	if _, err := config.LoadHost(nil, []string{"UNDERSTUDY_PERSIST_EVERY=1m"}, files{}.read); err == nil || err.Where != "UNDERSTUDY_PERSIST_EVERY" {
		t.Errorf("LoadHost with an interval and no directory = %v, want it refused", err)
	}
}

// Whichever single source carries a setting, the configuration is the
// same: the sources differ only in precedence.
func TestSourcesAgree(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Uint64().Draw(t, "seed")
		level := rapid.SampledFrom([]string{"debug", "info", "warn", "error"}).Draw(t, "level")
		clock := rapid.SampledFrom([]string{"running", "frozen", "frozen@2030-05-06T07:08:09Z"}).Draw(t, "clock")
		port := rapid.IntRange(0, 65535).Draw(t, "port")
		values := map[string]string{
			"seed": strconv.FormatUint(seed, 10), "log-level": level, "clock": clock,
			"listen": "127.0.0.1:" + strconv.Itoa(port),
		}
		envNames := map[string]string{"seed": "UNDERSTUDY_SEED", "log-level": "UNDERSTUDY_LOG_LEVEL", "clock": "UNDERSTUDY_CLOCK", "listen": "UNDERSTUDY_LISTEN"}
		fileKeys := map[string]string{"seed": "seed", "log-level": "logLevel", "clock": "clock", "listen": "listen"}
		flagArgs := make([]string, 0, 2*len(values))
		var args, env, members []string
		for _, name := range []string{"seed", "log-level", "clock", "listen"} {
			v := values[name]
			flagArgs = append(flagArgs, "--"+name, v)
			switch rapid.IntRange(0, 2).Draw(t, name+" source") {
			case 0:
				args = append(args, "--"+name+"="+v)
			case 1:
				env = append(env, envNames[name]+"="+v)
			default:
				members = append(members, strconv.Quote(fileKeys[name])+": "+strconv.Quote(v))
			}
		}
		f := files{config.DefaultConfigFile: "{" + strings.Join(members, ", ") + "}"}
		want, err := config.Load(flagArgs, nil, files{}.read)
		if err != nil {
			t.Fatalf("flags only: %v", err)
		}
		got, err := config.Load(args, env, f.read)
		if err != nil {
			t.Fatalf("mixed sources: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mixed sources = %#v, want %#v", got, want)
		}
	})
}

// topKeys are the valid top-level keys of the configuration file.
func topKeys() []string {
	sections := []string{"accounts", "regions", "s3", "lambda", "sqs", "iam", "dynamodb", "journal", "faults"}
	keys := make([]string, 0, len(sections)+len(config.Settings()))
	keys = append(keys, sections...)
	for _, s := range config.Settings() {
		keys = append(keys, s.Key)
	}
	return keys
}

// An unknown key is refused, and any closest key it names is valid at its
// depth.
func TestClosestIsAlwaysValid(t *testing.T) {
	keys := topKeys()
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringMatching(`[a-zA-Z]{1,20}`).Filter(func(k string) bool { return !slices.Contains(keys, k) }).Draw(t, "key")
		_, err := config.Load(nil, nil, files{config.DefaultConfigFile: `{"` + key + `": 1}`}.read)
		if err == nil || (err.Closest != "" && !slices.Contains(keys, err.Closest)) {
			t.Fatalf("unknown key %q: %v, closest %q is not a valid key", key, err, closestOf(err))
		}
	})
}

// A one-letter typo of a valid key names a valid key as the closest.
func TestTyposNameAValidKey(t *testing.T) {
	keys := topKeys()
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.SampledFrom(keys).Draw(t, "key")
		at := rapid.IntRange(0, len(key)).Draw(t, "at")
		letter := rapid.StringMatching(`[a-z]`).Draw(t, "letter")
		typo := key[:at] + letter + key[at:]
		if slices.Contains(keys, typo) {
			t.Skip("the typo is itself a valid key")
		}
		_, err := config.Load(nil, nil, files{config.DefaultConfigFile: `{"` + typo + `": 1}`}.read)
		if err == nil || !slices.Contains(keys, err.Closest) {
			t.Fatalf("typo %q of %q: %v, want a valid closest key", typo, key, err)
		}
	})
}

func closestOf(err *config.Error) string {
	if err == nil {
		return ""
	}
	return err.Closest
}

func fmtGo(v any) string { return fmt.Sprintf("%#v %v %+v", v, v, v) }
