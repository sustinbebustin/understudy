package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"syscall"

	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/server"
)

func serveCmd() *command {
	return raw("serve", "Run the emulator until interrupted", "serve [flags]",
		[]string{
			"understudy serve",
			"understudy serve --listen 127.0.0.1:0 --ready-file /tmp/understudy.json",
			"understudy serve --clock frozen@2026-01-01T00:00:00Z --seed 42 --log-format json",
			"understudy serve --persist ./.understudy --accept-dynamodb-local-license",
			"UNDERSTUDY_ADMIN_TOKEN=$(openssl rand -hex 32) understudy serve --listen 0.0.0.0:4566 --allow-unauthenticated-aws-api",
		},
		runServe, serveFlags)
}

// serveFlags prints the settings serve reads, with their variables and
// file keys.
func serveFlags(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "  --%s FILE\n      JSON configuration file (%s; default ./%s when it exists)\n", config.ConfigFlag, config.ConfigEnv, config.DefaultConfigFile)
	for _, s := range config.Settings() {
		// A setting with no flag, the admin token, is headed by its
		// variable, so a scan for its name finds it.
		name := s.Env + "=" + s.Value + " (no flag)"
		switch {
		case s.Flag != "" && s.Switch():
			name = "--" + s.Flag
		case s.Flag != "":
			name = "--" + s.Flag + " " + s.Value
		}
		fmt.Fprintf(&b, "  %s\n      %s\n      variable %s, file key %q\n", name, s.Usage, s.Env, s.Key)
	}
	fmt.Fprintf(&b, "  --%s text|json\n      another name for --log-format\n", config.OutputFlag)
	b.WriteString("\nPrecedence: flags, then UNDERSTUDY_* variables, then the file, then the defaults.\n" +
		"\nThe file also holds sections no flag sets. This file shows each, with the defaults where a\n" +
		"setting has one; any key may be left out:\n\n" + exampleFile + "\n" + sectionChoices)
	unsupported := unappliedSettings()
	refused := make([]string, len(unsupported))
	for i, s := range unsupported {
		refused[i] = s.name
	}
	b.WriteString("\nNot supported yet: this build refuses " + strings.Join(refused, ", ") + " rather than ignore them.\n" +
		"\nAccounts: a request signed with a 12-digit access key id is in that account; an accounts.keys\n" +
		"entry is in its account; any other key not minted by understudy, test among them, is in the\n" +
		"default account, 000000000000 unless accounts.default sets another. Under --sigv4 verify, test\n" +
		"and every 12-digit key sign with the secret test, an accounts.keys entry with its own secret,\n" +
		"and any other key understudy did not mint is refused.\n" +
		"\nReference: docs/configuration.md in the understudy repository.\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// exampleFile is the configuration file serve --help shows: every section
// config reads, with its defaults where a setting has one and sample
// values elsewhere. It leaves out the settings serve refuses.
const exampleFile = `  {
    "listen": "127.0.0.1:4566",
    "accounts": {"default": "000000000000"},
    "regions": ["local-1"],
    "s3": {"baseDomains": ["s3.dev.localhost"], "crossRegion": "aws"},
    "lambda": {
      "network": "auto", "startConcurrency": 16, "maxEnvironments": 64, "idleTimeout": "10m",
      "stateTransitions": "final",
      "hotReload": [{"function": "worker", "dir": "/abs/path/to/build", "account": "000000000000", "region": "us-east-1"}]
    },
    "sqs": {"queueDeletedRecently": "ignore"},
    "iam": {"assumeRole": "strict"},
    "dynamodb": {"enabled": true, "memoryLimit": 1073741824},
    "journal": {"capacity": 100000, "maxBytes": 134217728, "file": "/abs/path/to/journal.ndjson"},
    "faults": [{"id": "orders-throttle", "phase": "request", "match": {"operations": ["sqs.SendMessage"]},
                "action": {"kind": "throttle"}, "trigger": {"kind": "next-n", "n": 3}}]
  }`

// sectionChoices says what the example's sections take beyond its values.
const sectionChoices = `
  regions                    region names understudy accepts beyond AWS's own
  s3.baseDomains             names added to localhost and s3.localhost; each must resolve to
                             loopback by itself (a name under localhost)
  s3.crossRegion             aws or lenient
  lambda.network             auto, disabled, or bridge with "bridgeGateway": "172.17.0.1"
  lambda.stateTransitions    final or clock
  lambda.hotReload           functions whose code is read from a host directory; account and
                             region may be left out
  sqs.queueDeletedRecently   ignore or enforce
  iam.assumeRole             strict or lenient
  journal.file               NDJSON file every event is appended to (default none)
  faults                     rules as 'understudy faults list --output json' prints each "rule"
`

// runServe loads the configuration and serves until ctx ends.
func runServe(ctx context.Context, r *runner, args []string) error {
	if asksForJSON(args, r.env("UNDERSTUDY_LOG_FORMAT")) {
		// Errors follow --output json even when Load refuses the
		// configuration that says so.
		r.output = outputJSON
	}
	cfg, cerr := config.Load(args, r.o.Env, r.o.ReadFile)
	if cerr != nil {
		return configRefusal(cerr)
	}
	r.output = outputText
	if cfg.Log.Format == config.LogJSON {
		r.output = outputJSON
	}
	scfg, err := serverConfig(cfg, r.o.Version)
	if err != nil {
		return err
	}
	scfg.LogOutput = r.o.Stderr
	logWarnings(ctx, cfg, r.o.Stderr)
	srv, err := server.New(scfg) //nolint:contextcheck // server.New takes no context: wiring does no I/O a caller could cancel
	if refused, ok := errors.AsType[*server.ListenError](err); ok {
		return &usageError{msg: refused.Error()}
	}
	if refused, ok := errors.AsType[*server.SettingError](err); ok {
		return settingRefusal(refused, config.FileName(args, r.o.Env))
	}
	if err != nil {
		return err
	}
	if err := srv.Serve(ctx); err != nil {
		// A Serve that could not listen leaves the Server built; Close
		// releases what New opened, and does nothing after a shutdown.
		cerr := srv.Close() //nolint:contextcheck // Close takes no context: it releases files and memory
		if inUse := addressInUse(err); inUse != nil {
			return errors.Join(inUse, cerr)
		}
		return errors.Join(err, cerr)
	}
	return nil
}

// settingRefusal is the usage error for a setting of the configuration
// file named file that the server refused once it applied it, such as a
// fault rule or a hot-reload directory: every SettingError's setting comes
// from the file, so it is named as config names the file's settings.
func settingRefusal(refused *server.SettingError, file string) error {
	return &usageError{msg: (&config.Error{Where: file + ": " + refused.Setting, Reason: refused.Reason, Hint: refused.Hint}).Error()}
}

// configRefusal is the usage error for err, a configuration Load refused.
// A refused flag or argument reads like any command's ("--bogus is not a
// serve flag; the closest valid name is --listen"), without config's
// prefix or a second pointer at --help.
func configRefusal(err error) error {
	refused, ok := errors.AsType[*config.Error](err)
	if !ok || (!strings.HasPrefix(refused.Where, "--") && refused.Where != "flags" && refused.Where != "arguments") {
		return &usageError{msg: err.Error()}
	}
	sep := ": "
	if strings.HasPrefix(refused.Reason, "is ") {
		sep = " "
	}
	msg := refused.Where + sep + refused.Reason
	if refused.Closest != "" {
		msg += "; the closest valid name is " + refused.Closest
	}
	if refused.Hint != "" && refused.Hint != config.HelpHint {
		msg += "; " + refused.Hint
	}
	return &usageError{msg: msg}
}

// addressInUse is the failure naming the address another process holds
// and the flag that moves serve off it, when err is a bind that failed for
// that reason; nil otherwise. The command line was valid, so it exits 1,
// not 2.
func addressInUse(err error) error {
	bind, ok := errors.AsType[*server.BindError](err)
	if !ok || !errors.Is(bind.Err, syscall.EADDRINUSE) {
		return nil
	}
	flag := "--listen"
	if bind.TLS {
		flag = "--tls-listen"
	}
	return &localError{
		op:   "listening on " + bind.Addr.String(),
		err:  errors.New("the address is in use by another process"),
		next: fmt.Sprintf("stop the process on %s, or run 'understudy serve %s 127.0.0.1:0' for a free port", bind.Addr, flag),
	}
}

// asksForJSON reports whether serve's arguments, else the
// UNDERSTUDY_LOG_FORMAT value env, ask for JSON through --output or
// --log-format; the last such flag wins, as in Load.
func asksForJSON(args []string, env string) bool {
	format := env
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || (name != config.OutputFlag && name != "log-format") {
			continue
		}
		if !hasValue && i+1 < len(args) {
			i++
			value = args[i]
		}
		format = value
	}
	return format == "json"
}

// logWarnings logs the settings Load accepted with a fallback, in the
// configured log format, ahead of the server's own logs.
func logWarnings(ctx context.Context, cfg config.Config, w io.Writer) {
	opts := &slog.HandlerOptions{Level: cfg.Log.Level}
	var h slog.Handler = slog.NewTextHandler(w, opts)
	if cfg.Log.Format == config.LogJSON {
		h = slog.NewJSONHandler(w, opts)
	}
	log := slog.New(h)
	for _, w := range cfg.Warnings {
		log.LogAttrs(ctx, slog.LevelWarn, "a setting was accepted with a fallback",
			slog.String("setting", w.Setting), slog.String("warning", w.Message), slog.String("fix", w.Fix))
	}
}

// serverConfig maps the configuration onto what this build's server
// applies. A setting the server does not apply yet is refused by name
// rather than ignored, so nothing configured is silently dropped.
func serverConfig(cfg config.Config, version string) (server.Config, error) {
	if err := unapplied(cfg); err != nil {
		return server.Config{}, err
	}
	s := server.Config{
		Listen:         cfg.Listen.AddrPort(),
		Seed:           cfg.Seed,
		DefaultAccount: cfg.Accounts.Default,
		Journal:        server.Journal{Capacity: cfg.Journal.Capacity, MaxBytes: cfg.Journal.MaxBytes, File: cfg.Journal.File},
		LoadSnapshot:   cfg.LoadSnapshot,
		Faults:         cfg.Faults,
		S3BaseDomains:  cfg.S3.BaseDomains,
		LogLevel:       cfg.Log.Level,
		ReadyFile:      cfg.ReadyFile,
		Version:        version,
		Lambda: server.LambdaLimits{
			StartConcurrency: cfg.Lambda.StartConcurrency,
			IdleTimeout:      cfg.Lambda.IdleTimeout,
			MaxEnvironments:  cfg.Lambda.MaxEnvironments,
		},
		LambdaNetwork:  mapNetwork(cfg.Lambda.Network),
		DynamoDBMemory: cfg.DynamoDB.MemoryLimit,
		Switches: server.Switches{
			LambdaTransitions:  server.LambdaTransitionsFinal,
			SQSDeletedRecently: server.SQSDeletedRecentlyIgnore,
			S3CrossRegion:      server.S3CrossRegionAWS,
			STSAssumeRole:      server.STSAssumeRoleMustExist,
		},
	}
	mapListen(cfg.Listen, &s)
	for _, r := range cfg.Regions {
		s.Regions = append(s.Regions, r.String())
	}
	for _, h := range cfg.Lambda.HotReload {
		s.HotReload = append(s.HotReload, server.HotReload{Account: h.Account.String(), Region: h.Region.String(), Function: h.Function, Dir: h.Dir})
	}
	if cfg.Log.Format == config.LogJSON {
		s.LogFormat = server.LogJSON
	}
	if cfg.Lambda.StateTransitions == config.TransitionsClock {
		s.Switches.LambdaTransitions = server.LambdaTransitionsClock
	}
	if cfg.SQS.DeletedRecently == config.DeletedRecentlyEnforce {
		s.Switches.SQSDeletedRecently = server.SQSDeletedRecentlyEnforce
	}
	if cfg.S3.CrossRegion == config.CrossRegionLenient {
		s.Switches.S3CrossRegion = server.S3CrossRegionLenient
	}
	if cfg.IAM.AssumeRole == config.AssumeRoleLenient {
		s.Switches.STSAssumeRole = server.STSAssumeRoleLenient
	}
	if cfg.SigV4 == config.Verify {
		s.SigV4 = server.SigV4Verify
	}
	s.StaticKeys = mapStaticKeys(cfg.Accounts.Keys)
	s.TLS, s.Persist, s.ClockStart, s.Docker, s.DynamoDB = mapTLS(cfg.TLS), mapPersistence(cfg.Persistence), mapClock(cfg.Clock), mapDocker(cfg.Docker), mapLicence(cfg.DynamoDB.Licence)
	return s, nil
}

// mapStaticKeys maps accounts.keys onto the server's static keys.
func mapStaticKeys(keys []config.StaticKey) []server.StaticKey {
	out := make([]server.StaticKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, server.StaticKey{AccessKeyID: k.AccessKeyID, Secret: string(k.Secret), Account: k.Account})
	}
	return out
}

func mapListen(l config.Listen, s *server.Config) {
	switch l := l.(type) {
	case config.Loopback:
	case config.Exposed:
		switch a := l.Auth.(type) {
		case config.Token:
			s.AdminToken, s.AWSAPI = string(a.Secret), server.AWSAPIUnauthenticated{}
		case config.ContainerPublished:
			s.Exposure = server.ContainerPublished{}
		}
	}
}

func mapTLS(t config.TLS) server.TLS {
	switch t := t.(type) {
	case config.SelfSigned:
		return server.TLSSelfSigned{Listen: t.Listen}
	case config.TLSOff:
	}
	return server.TLSOff{}
}

func mapPersistence(p config.Persistence) server.Persistence {
	switch p := p.(type) {
	case config.Dir:
		return server.PersistDir{Path: p.Path, SaveEvery: p.SaveEvery}
	case config.Ephemeral:
	}
	return server.Ephemeral{}
}

func mapClock(c config.ClockStart) server.ClockStart {
	switch c := c.(type) {
	case config.Frozen:
		return server.ClockFrozen{At: c.At}
	case config.Scaled:
		return server.ClockScaled{Factor: c.Factor}
	case config.Running:
	}
	return server.ClockRunning{}
}

func mapNetwork(n config.LambdaNetwork) server.LambdaNetwork {
	switch n := n.(type) {
	case config.NetworkBridge:
		return server.NetworkBridge{Gateway: n.Gateway}
	case config.NetworkDisabled:
		return server.NetworkDisabled{}
	case config.NetworkAuto:
	}
	return server.NetworkAuto{}
}

func mapDocker(d config.Docker) server.Docker {
	switch d := d.(type) {
	case config.DockerDisabled:
		return server.DockerDisabled{}
	case config.DockerSocket:
		return server.DockerSocket{Path: d.Path}
	}
	return server.DockerSocket{}
}

func mapLicence(l config.Licence) server.DynamoDB {
	switch l.(type) {
	case config.DynamoDBDisabled:
		return server.DynamoDBDisabled{}
	case config.LicenceAccepted:
		return server.DynamoDBAccepted{}
	case config.AwaitingLicence:
	}
	return server.DynamoDBAwaiting{}
}

// unappliedSetting is a setting this build's server does not apply yet.
type unappliedSetting struct {
	name string
	// where says where the setting may have been set, for its removal.
	where string
	set   func(cfg, def config.Config) bool
}

// unappliedSettings are the settings serve refuses rather than ignore.
func unappliedSettings() []unappliedSetting {
	const file = "the configuration file"
	return []unappliedSetting{
		{"s3.maxObjectBytes", file, func(cfg, def config.Config) bool { return cfg.S3.MaxObjectBytes != def.S3.MaxObjectBytes }},
		{"dynamodb.registry", file, func(cfg, def config.Config) bool { return cfg.DynamoDB.Registry != def.DynamoDB.Registry }},
	}
}

// unapplied refuses the settings this build's server does not apply yet,
// naming each one set away from its default and where it may be set.
func unapplied(cfg config.Config) error {
	def := config.Defaults(config.OnHost{})
	var names []string
	for _, s := range unappliedSettings() {
		if s.set(cfg, def) {
			names = append(names, s.name+" (from "+s.where+")")
		}
	}
	if len(names) == 0 {
		return nil
	}
	return &usageError{msg: "this build of serve does not apply " + strings.Join(names, ", ") + " yet; remove the setting there to run with its default"}
}
