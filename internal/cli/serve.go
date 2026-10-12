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
	return &command{
		name:    "serve",
		summary: "Run the emulator until interrupted",
		usage:   "serve [flags]",
		examples: []string{
			"understudy serve",
			"understudy serve --listen 127.0.0.1:0 --ready-file /tmp/understudy.json",
			"understudy serve --clock frozen@2026-01-01T00:00:00Z --seed 42 --log-format json",
			"understudy serve --persist ./.understudy --accept-dynamodb-local-license",
			"UNDERSTUDY_ADMIN_TOKEN=$(openssl rand -hex 32) understudy serve --listen 0.0.0.0:4566",
		},
		action:   true,
		raw:      runServe,
		rawFlags: serveFlags,
	}
}

// serveFlags prints the settings serve reads, with their variables and
// file keys.
func serveFlags(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "  --%s FILE\n      JSON configuration file (%s; default ./%s when it exists)\n", config.ConfigFlag, config.ConfigEnv, config.DefaultConfigFile)
	for _, s := range config.Settings() {
		name := "(no flag)"
		if s.Flag != "" {
			name = "--" + s.Flag
		}
		fmt.Fprintf(&b, "  %s\n      %s\n      variable %s, file key %q\n", name, s.Usage, s.Env, s.Key)
	}
	fmt.Fprintf(&b, "  --%s text|json\n      another name for --log-format\n", config.OutputFlag)
	b.WriteString("\nPrecedence: flags, then UNDERSTUDY_* variables, then the file, then the defaults. The file also\n" +
		"holds the sections accounts, regions, s3, lambda, sqs, iam, dynamodb, journal and faults.\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// runServe loads the configuration and serves until ctx ends.
func runServe(ctx context.Context, r *runner, args []string) error {
	if asksForJSON(args, r.env("UNDERSTUDY_LOG_FORMAT")) {
		// Errors follow --output json even when Load refuses the
		// configuration that says so.
		r.output = outputJSON
	}
	cfg, cerr := config.Load(args, r.o.Env, r.o.ReadFile)
	if cerr != nil {
		return &usageError{msg: cerr.Error()}
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

// addressInUse is a usage error naming the address another process holds
// and the flag that moves serve off it, when err is a bind that failed for
// that reason; nil otherwise.
func addressInUse(err error) error {
	bind, ok := errors.AsType[*server.BindError](err)
	if !ok || !errors.Is(bind.Err, syscall.EADDRINUSE) {
		return nil
	}
	flag := "--listen"
	if bind.TLS {
		flag = "--tls-listen"
	}
	return &usageError{msg: fmt.Sprintf("%s is in use by another process, so serve cannot listen there; stop the process on %s, or run 'understudy serve %s 127.0.0.1:0' for a free port", bind.Addr, bind.Addr, flag)}
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
	s.TLS, s.Persist, s.ClockStart, s.Docker, s.DynamoDB = mapTLS(cfg.TLS), mapPersistence(cfg.Persistence), mapClock(cfg.Clock), mapDocker(cfg.Docker), mapLicence(cfg.DynamoDB.Licence)
	return s, nil
}

func mapListen(l config.Listen, s *server.Config) {
	switch l := l.(type) {
	case config.Loopback:
	case config.Exposed:
		switch a := l.Auth.(type) {
		case config.Token:
			s.AdminToken = string(a.Secret)
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

// unapplied refuses the settings this build's server does not apply yet,
// naming each one set away from its default.
func unapplied(cfg config.Config) error {
	def := config.Defaults(config.OnHost{})
	checks := []struct {
		name string
		set  bool
	}{
		{"sigv4 verify", cfg.SigV4 != def.SigV4},
		{"accounts.keys", len(cfg.Accounts.Keys) > 0},
		{"s3.maxObjectBytes", cfg.S3.MaxObjectBytes != def.S3.MaxObjectBytes},
		{"dynamodb.registry", cfg.DynamoDB.Registry != def.DynamoDB.Registry},
	}
	var names []string
	for _, c := range checks {
		if c.set {
			names = append(names, c.name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return &usageError{msg: "this build of serve does not apply " + strings.Join(names, ", ") + " yet; remove the setting to run with its default"}
}
