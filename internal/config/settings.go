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
	// Value is the placeholder help shows for the setting's value, such as
	// ADDR; empty for a switch, a flag that takes no value.
	Value string
	// Usage says what the setting does, its values and its default.
	Usage string
}

// Switch reports whether the setting's flag takes no value.
func (s Setting) Switch() bool { return s.Value == "" }

// Names of the settings outside the table: the config file's flag and
// variable, the CLI's endpoint variable, and the Docker variable whose
// unix socket is the default Docker setting. ListenEnv is the listen
// setting's variable, which the CLI also reads to find a server beside it.
const (
	ConfigFlag        = "config"
	ConfigEnv         = "UNDERSTUDY_CONFIG"
	DefaultConfigFile = "understudy.json"
	EndpointEnv       = "UNDERSTUDY_ENDPOINT"
	ListenEnv         = "UNDERSTUDY_LISTEN"
	DockerHostEnv     = "DOCKER_HOST"
)

// The DynamoDB Local licence acceptance, as dynamodb-local-terms.md 5.1 and
// ADR 0014 name it. Only the literal value "1" of AcceptLicenceEnv accepts.
const (
	AcceptLicenceFlag = "accept-dynamodb-local-license"
	AcceptLicenceEnv  = "UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE"
)

// AWSAPIFlag accepts serving the AWS API beyond loopback behind the admin
// token, which guards only the admin API.
const AWSAPIFlag = "allow-unauthenticated-aws-api"

// Setting keys.
const (
	keyListen       = "listen"
	keyAdminToken   = "adminToken"
	keyListenAuth   = "listenAuth"
	keyAWSAPI       = "allowUnauthenticatedAwsApi"
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
		{Key: keyListen, Flag: "listen", Env: ListenEnv, Value: "ADDR", Usage: "address to listen on; port 0 picks a free port (default 127.0.0.1:4566, or 0.0.0.0:4566 inside a container with its own network, outside Kubernetes)"},
		{Key: keyAdminToken, Env: "UNDERSTUDY_ADMIN_TOKEN", Value: "TOKEN", Usage: "bearer token every admin call must carry; required to listen beyond loopback on a host, with host networking or in a Kubernetes pod, where it must be at least 16 characters. It guards the admin API under /_understudy/ only"},
		{Key: keyListenAuth, Flag: "listen-auth", Env: "UNDERSTUDY_LISTEN_AUTH", Value: "MODE", Usage: "what guards a listener beyond loopback: token, or container-published inside a container with its own network, outside Kubernetes (default container-published there, else token)"},
		{Key: keyAWSAPI, Flag: AWSAPIFlag, Env: "UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API", Usage: "serve the AWS API to every peer that reaches a listener guarded by the admin token; this build checks no request signature, so such a peer can read every secret and object and run any image as a Lambda function. Required with the token beyond loopback"},
		{Key: keyTLS, Flag: "tls", Env: "UNDERSTUDY_TLS", Usage: "also serve HTTPS with a self-signed certificate for localhost, *.localhost and 127.0.0.1"},
		{Key: keyTLSListen, Flag: "tls-listen", Env: "UNDERSTUDY_TLS_LISTEN", Value: "ADDR", Usage: "address of the TLS listener; implies --tls (default the listen address at port 4567, or port 0 when listening on port 0)"},
		{Key: keyPersist, Flag: "persist", Env: "UNDERSTUDY_PERSIST", Value: "DIR", Usage: "directory state persists in across restarts (default none: state is in memory only). A running clock resumes at wall time, or at its saved instant when that is later; a frozen or scaled one resumes from its saved instant"},
		{Key: keyPersistEvery, Flag: "persist-every", Env: "UNDERSTUDY_PERSIST_EVERY", Value: "DURATION", Usage: "wall time between checkpoints while state changed, with --persist (default 30s)"},
		{Key: keyLoadSnapshot, Flag: "load-snapshot", Env: "UNDERSTUDY_LOAD_SNAPSHOT", Value: "FILE", Usage: "snapshot archive to load at start, over any state --persist restored"},
		{Key: keySeed, Flag: "seed", Env: "UNDERSTUDY_SEED", Value: "N", Usage: "seed for reproducible ids, an unsigned integer (default random)"},
		{Key: keyClock, Flag: "clock", Env: "UNDERSTUDY_CLOCK", Value: "MODE", Usage: "how the emulator clock starts: running, frozen, or frozen@<RFC 3339 time> (default running)"},
		{Key: keyClockScale, Flag: "clock-scale", Env: "UNDERSTUDY_CLOCK_SCALE", Value: "FACTOR", Usage: "run the clock this many emulator seconds per wall second from start, above 0 and at most 10000; not with a frozen --clock (default 1)"},
		{Key: keySigV4, Flag: "sigv4", Env: "UNDERSTUDY_SIGV4", Value: "MODE", Usage: "signature checking: parse (default). verify, which refuses a bad signature, is not applied yet, and serve refuses it"},
		{Key: keyDocker, Flag: "docker", Env: "UNDERSTUDY_DOCKER", Value: "URL", Usage: "Docker Engine for Lambda and DynamoDB Local: unix://<socket> or disabled (default DOCKER_HOST, else unix:///var/run/docker.sock)"},
		{Key: keyAccept, Flag: AcceptLicenceFlag, Env: AcceptLicenceEnv, Usage: "accept the Amazon DynamoDB Local License Agreement so DynamoDB Local is pulled and run; the variable accepts only the value 1"},
		{Key: keyReadyFile, Flag: "ready-file", Env: "UNDERSTUDY_READY_FILE", Value: "FILE", Usage: "file to write {\"endpoint\", \"pid\"} to once listening"},
		{Key: keyLogFormat, Flag: "log-format", Env: "UNDERSTUDY_LOG_FORMAT", Value: "FORMAT", Usage: "log format: text or json (default text)"},
		{Key: keyLogLevel, Flag: "log-level", Env: "UNDERSTUDY_LOG_LEVEL", Value: "LEVEL", Usage: "log level: debug, info, warn or error (default info)"},
	}
}
