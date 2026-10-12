package config_test

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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

// inContainer holds the marker Docker puts in every container.
func inContainer() files { return files{"/.dockerenv": ""} }

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

// A listener beyond loopback without a token is refused on a host, with
// both ways out and the loopback fix named.
func TestNonLoopbackNeedsAToken(t *testing.T) {
	err := loadErr(t, []string{"--listen", "0.0.0.0:4566"}, nil, files{})
	for _, want := range []string{"UNDERSTUDY_ADMIN_TOKEN", "container-published", "--listen 127.0.0.1:4566"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	cfg := load(t, []string{"--listen", "0.0.0.0:4566"}, []string{"UNDERSTUDY_ADMIN_TOKEN=s3cret"}, files{})
	exposed, ok := cfg.Listen.(config.Exposed)
	if !ok {
		t.Fatalf("listen = %#v, want Exposed", cfg.Listen)
	}
	if tok, ok := exposed.Auth.(config.Token); !ok || tok.Secret != "s3cret" {
		t.Errorf("auth = %#v, want the token", exposed.Auth)
	}
	if strings.Contains(fmtGo(cfg.Listen), "s3cret") {
		t.Errorf("the token leaks through %%#v: %s", fmtGo(cfg.Listen))
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
	if cfg := load(t, nil, nil, files{"/proc/1/cgroup": "0::/system.slice/containerd.service"}); cfg.Listen != want {
		t.Errorf("default listener under a containerd cgroup = %#v", cfg.Listen)
	}
	if cfg := load(t, []string{"--listen", "127.0.0.1:0"}, nil, inContainer()); cfg.Listen != (config.Loopback{Addr: netip.MustParseAddrPort("127.0.0.1:0")}) {
		t.Errorf("loopback in a container = %#v", cfg.Listen)
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
		{env: []string{"UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=yes"}, where: "UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE"},
		{file: `{"s3": {"baseDomains": ["example.com"]}}`, where: "understudy.json: s3.baseDomains[0]"},
		{file: `{"s3": {"maxObjectBytes": "big"}}`, where: "understudy.json: s3.maxObjectBytes"},
		{file: `{"lambda": {"network": "bridge"}}`, where: "understudy.json: lambda.bridgeGateway"},
		{file: `{"lambda": {"hotReload": [{"function": "f", "dir": "relative"}]}}`, where: "understudy.json: lambda.hotReload[0].dir"},
		{file: `{"lambda": {"hotReload": [{"function": "f", "dir": "/src", "account": "12"}]}}`, where: "understudy.json: lambda.hotReload[0].account"},
		{file: `{"lambda": {"hotReload": [{"function": "f", "dir": "/src", "region": "Nope"}]}}`, where: "understudy.json: lambda.hotReload[0].region"},
		{file: `{"lambda": {"hotReload": [{"function": "f", "dir": "/src", "region": "local-2"}]}, "regions": ["local-1"]}`, where: "understudy.json: lambda.hotReload[0].region"},
		{file: `{"dynamodb": {"enabled": false}, "acceptDynamodbLocalLicense": true}`, where: "understudy.json: acceptDynamodbLocalLicense"},
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
	cfg := load(t, nil, []string{"UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1"}, files{config.DefaultConfigFile: file})
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
		{"licence", cfg.DynamoDB.Licence, config.LicenceAccepted{}},
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
	want := config.Host{Persistence: config.Dir{Path: "/data", SaveEvery: config.DefaultSaveEvery}, Docker: config.DockerDisabled{}, Licence: config.AwaitingLicence{}}
	if !reflect.DeepEqual(host, want) {
		t.Errorf("LoadHost = %#v, want %#v", host, want)
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

// An unknown key always names a closest key that is valid at its depth.
func TestClosestIsAlwaysValid(t *testing.T) {
	valid := map[string]bool{}
	for _, s := range config.Settings() {
		valid[s.Key] = true
	}
	for _, k := range []string{"accounts", "regions", "s3", "lambda", "sqs", "iam", "dynamodb", "journal", "faults"} {
		valid[k] = true
	}
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringMatching(`[a-zA-Z]{1,20}`).Filter(func(k string) bool { return !valid[k] }).Draw(t, "key")
		_, err := config.Load(nil, nil, files{config.DefaultConfigFile: `{"` + key + `": 1}`}.read)
		if err == nil || !valid[err.Closest] {
			t.Fatalf("unknown key %q: %v, closest %q is not a valid key", key, err, closestOf(err))
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
