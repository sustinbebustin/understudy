package config

// Setting is one scalar setting serve reads from a flag, the environment
// and the file, in that order of precedence. The file key is the flag name
// in camelCase.
type Setting struct {
	// Key is the setting's file key.
	Key string
	// Flag is the flag name without dashes; empty when the setting has no
	// flag, as for the admin token, which a command line would leak.
	Flag string
	// Env is the environment variable.
	Env string
	// Bool marks a flag that takes no value.
	Bool bool
	// Usage says what the setting does, its values and its default.
	Usage string
}

// Names of the settings outside the table: the config file's flag and
// variable, the CLI's endpoint variable, and the Docker variable whose
// unix socket is the default Docker setting.
const (
	ConfigFlag        = "config"
	ConfigEnv         = "UNDERSTUDY_CONFIG"
	DefaultConfigFile = "understudy.json"
	EndpointEnv       = "UNDERSTUDY_ENDPOINT"
	DockerHostEnv     = "DOCKER_HOST"
)

// The DynamoDB Local licence acceptance, as dynamodb-local-terms.md 5.1 and
// ADR 0014 name it. Only the literal value "1" of AcceptLicenceEnv accepts.
const (
	AcceptLicenceFlag = "accept-dynamodb-local-license"
	AcceptLicenceEnv  = "UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE"
)

// Setting keys.
const (
	keyListen       = "listen"
	keyAdminToken   = "adminToken"
	keyListenAuth   = "listenAuth"
	keyTLS          = "tls"
	keyTLSListen    = "tlsListen"
	keyPersist      = "persist"
	keyPersistEvery = "persistEvery"
	keyLoadSnapshot = "loadSnapshot"
	keySeed         = "seed"
	keyClock        = "clock"
	keyClockScale   = "clockScale"
	keySigV4        = "sigv4"
	keyDocker       = "docker"
	keyAccept       = "acceptDynamodbLocalLicense"
	keyReadyFile    = "readyFile"
	keyLogFormat    = "logFormat"
	keyLogLevel     = "logLevel"
)

// Settings lists every scalar setting in the order help shows them.
func Settings() []Setting {
	return []Setting{
		{Key: keyListen, Flag: "listen", Env: "UNDERSTUDY_LISTEN", Usage: "address to listen on; port 0 picks a free port (default 127.0.0.1:4566, or 0.0.0.0:4566 inside a container)"},
		{Key: keyAdminToken, Env: "UNDERSTUDY_ADMIN_TOKEN", Usage: "bearer token every admin call must carry; required to listen beyond loopback on a host"},
		{Key: keyListenAuth, Flag: "listen-auth", Env: "UNDERSTUDY_LISTEN_AUTH", Usage: "what guards a listener beyond loopback: token, or container-published inside a container (default container-published inside a container, else token)"},
		{Key: keyTLS, Flag: "tls", Env: "UNDERSTUDY_TLS", Bool: true, Usage: "also serve HTTPS with a self-signed certificate for localhost, *.localhost and 127.0.0.1"},
		{Key: keyTLSListen, Flag: "tls-listen", Env: "UNDERSTUDY_TLS_LISTEN", Usage: "address of the TLS listener; implies --tls (default the listen address at port 4567, or port 0 when listening on port 0)"},
		{Key: keyPersist, Flag: "persist", Env: "UNDERSTUDY_PERSIST", Usage: "directory state persists in across restarts (default none: state is in memory only)"},
		{Key: keyPersistEvery, Flag: "persist-every", Env: "UNDERSTUDY_PERSIST_EVERY", Usage: "wall time between checkpoints while state changed, with --persist (default 30s)"},
		{Key: keyLoadSnapshot, Flag: "load-snapshot", Env: "UNDERSTUDY_LOAD_SNAPSHOT", Usage: "snapshot archive to load at start, over any state --persist restored"},
		{Key: keySeed, Flag: "seed", Env: "UNDERSTUDY_SEED", Usage: "seed for reproducible ids, an unsigned integer (default random)"},
		{Key: keyClock, Flag: "clock", Env: "UNDERSTUDY_CLOCK", Usage: "how the emulator clock starts: running, frozen, or frozen@<RFC 3339 time> (default running)"},
		{Key: keyClockScale, Flag: "clock-scale", Env: "UNDERSTUDY_CLOCK_SCALE", Usage: "run the clock this many emulator seconds per wall second from start, above 0 and at most 10000; not with a frozen --clock (default 1)"},
		{Key: keySigV4, Flag: "sigv4", Env: "UNDERSTUDY_SIGV4", Usage: "signature checking: parse, or verify to refuse a bad signature (default parse)"},
		{Key: keyDocker, Flag: "docker", Env: "UNDERSTUDY_DOCKER", Usage: "Docker Engine for Lambda and DynamoDB Local: unix://<socket> or disabled (default DOCKER_HOST, else unix:///var/run/docker.sock)"},
		{Key: keyAccept, Flag: AcceptLicenceFlag, Env: AcceptLicenceEnv, Bool: true, Usage: "accept the Amazon DynamoDB Local License Agreement so DynamoDB Local is pulled and run; the variable accepts only the value 1"},
		{Key: keyReadyFile, Flag: "ready-file", Env: "UNDERSTUDY_READY_FILE", Usage: "file to write {\"endpoint\", \"pid\"} to once listening"},
		{Key: keyLogFormat, Flag: "log-format", Env: "UNDERSTUDY_LOG_FORMAT", Usage: "log format: text or json (default text)"},
		{Key: keyLogLevel, Flag: "log-level", Env: "UNDERSTUDY_LOG_LEVEL", Usage: "log level: debug, info, warn or error (default info)"},
	}
}
