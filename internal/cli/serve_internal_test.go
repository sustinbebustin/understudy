package cli

import (
	"io/fs"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/server"
)

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
		{"journal", nil, file, func(s server.Config) any { return s.Journal }, server.Journal{Capacity: 10, MaxBytes: 2048, File: "/tmp/j.ndjson"}},
		{"lambda limits", nil, file, func(s server.Config) any { return s.Lambda }, server.LambdaLimits{StartConcurrency: 4, IdleTimeout: time.Minute, MaxEnvironments: 8}},
		{"dynamodb memory", nil, file, func(s server.Config) any { return s.DynamoDBMemory }, int64(512 << 20)},
		{"bridge network", nil, file, func(s server.Config) any { return s.LambdaNetwork }, server.NetworkBridge{Gateway: netip.MustParseAddr("172.17.0.1")}},
		{"disabled network", nil, `{"lambda": {"network": "disabled"}}`, func(s server.Config) any { return s.LambdaNetwork }, server.NetworkDisabled{}},
		{"auto network", nil, `{}`, func(s server.Config) any { return s.LambdaNetwork }, server.NetworkAuto{}},
		{"running clock", nil, `{}`, func(s server.Config) any { return s.ClockStart }, server.ClockRunning{}},
		{"frozen clock", []string{"--clock", "frozen@2026-01-01T00:00:00Z"}, `{}`, func(s server.Config) any { return s.ClockStart }, server.ClockFrozen{At: at}},
		{"scaled clock", []string{"--clock-scale", "60"}, `{}`, func(s server.Config) any { return s.ClockStart }, server.ClockScaled{Factor: 60}},
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

// serverConfig refuses, by name, each setting this build's server does
// not apply.
func TestServerConfigRefusesUnappliedSettings(t *testing.T) {
	cases := map[string]string{
		"sigv4 verify":      `{"sigv4": "verify"}`,
		"accounts.keys":     `{"accounts": {"keys": [{"accessKeyId": "LOCALKEY00000001", "secretAccessKey": "sk", "account": "222222222222"}]}}`,
		"s3.maxObjectBytes": `{"s3": {"maxObjectBytes": 1024}}`,
		"dynamodb.registry": `{"dynamodb": {"registry": "ecr-public"}}`,
	}
	for name, file := range cases {
		_, err := serverConfig(loadFile(t, nil, file), "test")
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("serverConfig with %s = %v, want it refused by name", file, err)
		}
	}
}
