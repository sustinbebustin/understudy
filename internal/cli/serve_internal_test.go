package cli

import (
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/server"
)

func mustAccount(t *testing.T, s string) aws.AccountID {
	t.Helper()
	a, err := aws.ParseAccountID(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// loadFile loads a configuration whose understudy.json is file, on a host.
func loadFile(t *testing.T, args []string, file string) config.Config {
	t.Helper()
	cfg, err := config.Load(args, nil, func(path string) ([]byte, error) {
		if path == config.DefaultConfigFile {
			return []byte(file), nil
		}
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	})
	if err != nil {
		t.Fatalf("Load(%q, %s) = %v", args, file, err)
	}
	return cfg
}

// serverConfig carries every setting the server applies onto server.Config.
func TestServerConfigMapsAppliedSettings(t *testing.T) {
	file := `{
		"accounts": {"default": "111111111111"},
		"loadSnapshot": "/fixtures/a.tar",
		"lambda": {"network": "bridge", "bridgeGateway": "172.17.0.1", "startConcurrency": 4, "idleTimeout": "1m", "maxEnvironments": 8},
		"dynamodb": {"memoryLimit": 536870912},
		"journal": {"capacity": 10, "maxBytes": 2048, "file": "/tmp/j.ndjson"}
	}`
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		args []string
		file string
		get  func(server.Config) any
		want any
	}{
		{"default account", nil, file, func(s server.Config) any { return s.DefaultAccount.String() }, "111111111111"},
		{"load snapshot", nil, file, func(s server.Config) any { return s.LoadSnapshot }, "/fixtures/a.tar"},
		{"snapshot max bytes", []string{"--snapshot-max-bytes", "2097152"}, `{}`, func(s server.Config) any { return s.SnapshotMaxBytes }, int64(2 << 20)},
		{"default snapshot max bytes", nil, `{}`, func(s server.Config) any { return s.SnapshotMaxBytes }, int64(1 << 30)},
		{"journal", nil, file, func(s server.Config) any { return s.Journal }, server.Journal{Capacity: 10, MaxBytes: 2048, File: "/tmp/j.ndjson"}},
		{"lambda limits", nil, file, func(s server.Config) any { return s.Lambda }, server.LambdaLimits{StartConcurrency: 4, IdleTimeout: time.Minute, MaxEnvironments: 8}},
		{"dynamodb memory", nil, file, func(s server.Config) any { return s.DynamoDBMemory }, int64(512 << 20)},
		{"bridge network", nil, file, func(s server.Config) any { return s.LambdaNetwork }, server.NetworkBridge{Gateway: netip.MustParseAddr("172.17.0.1")}},
		{"disabled network", nil, `{"lambda": {"network": "disabled"}}`, func(s server.Config) any { return s.LambdaNetwork }, server.NetworkDisabled{}},
		{"auto network", nil, `{}`, func(s server.Config) any { return s.LambdaNetwork }, server.NetworkAuto{}},
		{"running clock", nil, `{}`, func(s server.Config) any { return s.ClockStart }, server.ClockRunning{}},
		{"frozen clock", []string{"--clock", "frozen@2026-01-01T00:00:00Z"}, `{}`, func(s server.Config) any { return s.ClockStart }, server.ClockFrozen{At: at}},
		{"scaled clock", []string{"--clock-scale", "60"}, `{}`, func(s server.Config) any { return s.ClockStart }, server.ClockScaled{Factor: 60}},
		{"sigv4 parse", nil, `{}`, func(s server.Config) any { return s.SigV4 }, server.SigV4ParseOnly},
		{"sigv4 verify", []string{"--sigv4", "verify"}, `{}`, func(s server.Config) any { return s.SigV4 }, server.SigV4Verify},
		{
			"static keys", nil, `{"accounts": {"keys": [{"accessKeyId": "LOCALKEY00000001", "secretAccessKey": "sk", "account": "222222222222"}]}}`,
			func(s server.Config) any { return s.StaticKeys },
			[]server.StaticKey{{AccessKeyID: "LOCALKEY00000001", Secret: "sk", Account: mustAccount(t, "222222222222")}},
		},
	}
	for _, c := range cases {
		s, err := serverConfig(loadFile(t, c.args, c.file), "test")
		if err != nil {
			t.Errorf("%s: serverConfig = %v", c.name, err)
			continue
		}
		if got := c.get(s); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.name, got, c.want)
		}
	}
}

// The example file serve --help shows loads, serve applies every setting
// in it, and the values it calls defaults are the defaults.
func TestServeHelpExampleFileLoads(t *testing.T) {
	cfg := loadFile(t, nil, exampleFile)
	if _, err := serverConfig(cfg, "test"); err != nil {
		t.Fatalf("serverConfig of the example file = %v", err)
	}
	def := config.Defaults(config.OnHost{})
	cases := map[string]struct{ got, want any }{
		"listen":                    {cfg.Listen, def.Listen},
		"accounts.default":          {cfg.Accounts.Default, def.Accounts.Default},
		"s3.crossRegion":            {cfg.S3.CrossRegion, def.S3.CrossRegion},
		"lambda.network":            {cfg.Lambda.Network, def.Lambda.Network},
		"lambda.startConcurrency":   {cfg.Lambda.StartConcurrency, def.Lambda.StartConcurrency},
		"lambda.maxEnvironments":    {cfg.Lambda.MaxEnvironments, def.Lambda.MaxEnvironments},
		"lambda.idleTimeout":        {cfg.Lambda.IdleTimeout, def.Lambda.IdleTimeout},
		"lambda.stateTransitions":   {cfg.Lambda.StateTransitions, def.Lambda.StateTransitions},
		"sqs.queueDeletedRecently":  {cfg.SQS.DeletedRecently, def.SQS.DeletedRecently},
		"iam.assumeRole":            {cfg.IAM.AssumeRole, def.IAM.AssumeRole},
		"dynamodb.memoryLimit":      {cfg.DynamoDB.MemoryLimit, def.DynamoDB.MemoryLimit},
		"dynamodb.enabled":          {cfg.DynamoDB.Licence, def.DynamoDB.Licence},
		"journal.capacity":          {cfg.Journal.Capacity, def.Journal.Capacity},
		"journal.maxBytes":          {cfg.Journal.MaxBytes, def.Journal.MaxBytes},
		"regions (count)":           {len(cfg.Regions), 1},
		"lambda.hotReload (count)":  {len(cfg.Lambda.HotReload), 1},
		"faults (count)":            {len(cfg.Faults), 1},
		"s3.baseDomains (appended)": {len(cfg.S3.BaseDomains), len(def.S3.BaseDomains) + 1},
	}
	for name, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("example file %s = %#v, want %#v", name, c.got, c.want)
		}
	}
	var help strings.Builder
	if err := serveFlags(&help); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{exampleFile, "\n  UNDERSTUDY_ADMIN_TOKEN=TOKEN (no flag)\n"} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("serve --help lacks %q:\n%s", want, help.String())
		}
	}
}

// docs/configuration.md carries serve --help's settings verbatim between
// its markers, so the published reference never drifts from the binary.
func TestConfigurationReferenceMatchesServeHelp(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	const begin, end = "<!-- BEGIN serve --help -->\n```text\n", "```\n<!-- END serve --help -->"
	_, rest, ok := strings.Cut(string(doc), begin)
	block, _, closed := strings.Cut(rest, end)
	if !ok || !closed {
		t.Fatalf("docs/configuration.md has no %q ... %q block", begin, end)
	}
	var help strings.Builder
	if err := serveFlags(&help); err != nil {
		t.Fatal(err)
	}
	if block != help.String() {
		t.Errorf("docs/configuration.md is out of date; replace its block with what serveFlags prints:\n%s", help.String())
	}
}

// serverConfig refuses, by name and where it may be set, each setting
// this build's server does not apply; serve's help lists them.
func TestServerConfigRefusesUnappliedSettings(t *testing.T) {
	cases := map[string]struct{ file, where string }{
		"s3.maxObjectBytes": {`{"s3": {"maxObjectBytes": 1024}}`, "the configuration file"},
		"dynamodb.registry": {`{"dynamodb": {"registry": "ecr-public"}}`, "the configuration file"},
	}
	var help strings.Builder
	if err := serveFlags(&help); err != nil {
		t.Fatal(err)
	}
	for name, c := range cases {
		_, err := serverConfig(loadFile(t, nil, c.file), "test")
		if err == nil || !strings.Contains(err.Error(), name+" (from "+c.where+")") {
			t.Errorf("serverConfig with %s = %v, want it refused by name, from %s", c.file, err, c.where)
		}
		if !strings.Contains(help.String(), name) {
			t.Errorf("serve --help does not say %s is not supported yet:\n%s", name, help.String())
		}
	}
}
