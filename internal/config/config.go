package config

import (
	"log/slog"
	"net/netip"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/fault"
)

// Config is the whole serve configuration, parsed and validated: every mode
// is a sum, so a combination Load refuses cannot be built from it
// (ARCHITECTURE.md 18).
type Config struct {
	Listen      Listen
	TLS         TLS
	Persistence Persistence
	Snapshot    Snapshot
	SigV4       SigV4Mode
	// Seed makes every generated id reproducible; nil draws from
	// crypto/rand.
	Seed     *uint64
	Clock    ClockStart
	Accounts Accounts
	// Regions are region names accepted beyond AWS's own.
	Regions  []aws.Region
	Docker   Docker
	S3       S3
	Lambda   Lambda
	SQS      SQS
	IAM      IAM
	DynamoDB DynamoDB
	Journal  Journal
	// Faults are fault rules active from the start.
	Faults []fault.Rule
	Log    Log
	// ReadyFile, when set, receives {"endpoint", "pid"} once serve listens.
	ReadyFile string
	// LoadSnapshot, when set, is an archive loaded at start.
	LoadSnapshot string
	// Warnings are the settings Load accepted with a fallback; serve logs
	// them and doctor reports them.
	Warnings []Warning
}

// Warning is a setting Load accepted with a fallback rather than refused.
type Warning struct {
	// Setting names the setting as its source spells it, such as
	// DOCKER_HOST.
	Setting string
	// Message says what the setting holds and what understudy does
	// instead.
	Message string
	// Fix is how to act on the warning or silence it.
	Fix string
}

// Host is the part of the configuration about this machine rather than the
// listener: what doctor and cleanup read.
type Host struct {
	Persistence Persistence
	Docker      Docker
	HotReload   []HotReload
	DynamoDB    DynamoDBMode
	Warnings    []Warning
}

// Host returns the machine settings of c.
func (c Config) Host() Host {
	return Host{Persistence: c.Persistence, Docker: c.Docker, HotReload: c.Lambda.HotReload, DynamoDB: c.DynamoDB.Mode, Warnings: c.Warnings}
}

// Listen is the main listener: on loopback, or beyond it with what makes
// that safe.
//
//sumtype:decl
type Listen interface {
	// AddrPort is the address to bind; port 0 picks a free one.
	AddrPort() netip.AddrPort
	isListen()
}

// Loopback listens on a loopback address, where the admin API needs no
// token.
type Loopback struct{ Addr netip.AddrPort }

// Exposed listens beyond loopback; Auth is what keeps the admin API from
// the network.
type Exposed struct {
	Addr netip.AddrPort
	Auth Auth
}

// AddrPort returns the loopback address.
func (l Loopback) AddrPort() netip.AddrPort { return l.Addr }

// AddrPort returns the exposed address.
func (e Exposed) AddrPort() netip.AddrPort { return e.Addr }

func (Loopback) isListen() {}
func (Exposed) isListen()  {}

// Auth guards the admin API of an Exposed listener.
//
//sumtype:decl
type Auth interface{ isAuth() }

// Token requires Authorization: Bearer <Secret> on every admin call. It
// guards only the admin API, so Load builds it only with
// --allow-unauthenticated-aws-api, which accepts the AWS API as open to
// every peer that reaches the listener.
type Token struct{ Secret Secret }

// ContainerPublished trusts Docker's port publishing as the exposure
// control; Load accepts it only inside a container.
type ContainerPublished struct{}

func (Token) isAuth()              {}
func (ContainerPublished) isAuth() {}

// Secret is a credential that never appears in logs, errors or printed
// configuration.
type Secret string

// String redacts the secret.
func (Secret) String() string { return "[redacted]" }

// GoString redacts the secret.
func (Secret) GoString() string { return "[redacted]" }

// LogValue redacts the secret.
func (Secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// TLS is the self-signed TLS listener setting.
//
//sumtype:decl
type TLS interface{ isTLS() }

// TLSOff serves plain HTTP only.
type TLSOff struct{}

// SelfSigned also serves HTTPS on Listen with a generated certificate.
type SelfSigned struct{ Listen netip.AddrPort }

func (TLSOff) isTLS()     {}
func (SelfSigned) isTLS() {}

// Persistence is where state survives a restart.
//
//sumtype:decl
type Persistence interface{ isPersistence() }

// Ephemeral keeps state in memory only.
type Ephemeral struct{}

// Dir keeps state under Path, checkpointed every SaveEvery of wall time
// while it changed.
type Dir struct {
	Path      string
	SaveEvery time.Duration
}

func (Ephemeral) isPersistence() {}
func (Dir) isPersistence()       {}

// Snapshot bounds snapshot archives (snapshotMaxBytes).
type Snapshot struct {
	// MaxBytes caps every archive, counted as the uncompressed tar: save,
	// upload, --load-snapshot, and the --persist load and checkpoint. It is
	// from 1 MiB to SnapshotMaxBytesCeiling.
	MaxBytes int64
}

// SigV4Mode is how request signatures are checked.
type SigV4Mode uint8

// SigV4Mode values; parsing always runs.
const (
	// ParseOnly reads the signature for the account and region and does
	// not verify it.
	ParseOnly SigV4Mode = iota
	// Verify also refuses a request whose signature does not match.
	Verify
)

// ClockStart is the emulator clock's mode at start.
//
//sumtype:decl
type ClockStart interface{ isClockStart() }

// Running follows wall time.
type Running struct{}

// Frozen holds emulator time still until it is advanced; a zero At freezes
// at wall time at start.
type Frozen struct{ At time.Time }

// Scaled runs Factor emulator seconds per wall second from wall time at
// start; Factor is above 0 and at most MaxClockScale.
type Scaled struct{ Factor float64 }

func (Running) isClockStart() {}
func (Frozen) isClockStart()  {}
func (Scaled) isClockStart()  {}

// Accounts are the default account and the configured static keys.
type Accounts struct {
	// Default is the account of a call whose key understudy does not know.
	Default aws.AccountID
	Keys    []StaticKey
}

// StaticKey maps an access key to an account's root.
type StaticKey struct {
	AccessKeyID string
	Secret      Secret
	Account     aws.AccountID
}

// Docker is where the Docker Engine is.
//
//sumtype:decl
type Docker interface{ isDocker() }

// DockerSocket reaches the Engine over the unix socket at Path.
type DockerSocket struct{ Path string }

// DockerDisabled runs no container; the control planes keep working.
type DockerDisabled struct{}

func (DockerSocket) isDocker()   {}
func (DockerDisabled) isDocker() {}

// S3 holds the S3 settings.
type S3 struct {
	// BaseDomains are the hosts virtual-hosted addresses end in.
	BaseDomains    []string
	CrossRegion    CrossRegion
	MaxObjectBytes int64
}

// CrossRegion is the s3.crossRegion switch.
type CrossRegion uint8

// CrossRegion values.
const (
	// CrossRegionAWS answers a request for another region as AWS does.
	CrossRegionAWS CrossRegion = iota
	// CrossRegionLenient serves the bucket whatever region is asserted.
	CrossRegionLenient
)

// Lambda holds the Lambda settings.
type Lambda struct {
	Network LambdaNetwork
	// StartConcurrency bounds containers starting at once.
	StartConcurrency int
	// IdleTimeout removes an environment idle that long.
	IdleTimeout time.Duration
	// MaxEnvironments bounds all environments.
	MaxEnvironments  int
	StateTransitions StateTransitions
	HotReload        []HotReload
}

// LambdaNetwork is how function containers reach understudy.
//
//sumtype:decl
type LambdaNetwork interface{ isLambdaNetwork() }

// NetworkAuto detects the topology at first use.
type NetworkAuto struct{}

// NetworkBridge binds the container listener on Gateway, a private IPv4
// address.
type NetworkBridge struct{ Gateway netip.Addr }

// NetworkDisabled runs no function.
type NetworkDisabled struct{}

func (NetworkAuto) isLambdaNetwork()     {}
func (NetworkBridge) isLambdaNetwork()   {}
func (NetworkDisabled) isLambdaNetwork() {}

// StateTransitions is the lambda.stateTransitions switch.
type StateTransitions uint8

// StateTransitions values.
const (
	// TransitionsFinal settles every provisioning state before the call
	// returns.
	TransitionsFinal StateTransitions = iota
	// TransitionsClock keeps the intermediate states on the clock.
	TransitionsClock
)

// HotReload binds Dir, an absolute host path, as Function's code. A zero
// Account is 000000000000 and a zero Region us-east-1.
type HotReload struct {
	Function string
	Dir      string
	Account  aws.AccountID
	Region   aws.Region
}

// SQS holds the SQS settings.
type SQS struct{ DeletedRecently DeletedRecently }

// DeletedRecently is the sqs.queueDeletedRecently switch.
type DeletedRecently uint8

// DeletedRecently values.
const (
	// DeletedRecentlyIgnore reuses a deleted name at once.
	DeletedRecentlyIgnore DeletedRecently = iota
	// DeletedRecentlyEnforce refuses it for 60 seconds, as AWS does.
	DeletedRecentlyEnforce
)

// IAM holds the IAM and STS settings.
type IAM struct{ AssumeRole AssumeRole }

// AssumeRole is the iam.assumeRole switch.
type AssumeRole uint8

// AssumeRole values.
const (
	// AssumeRoleStrict refuses a role IAM does not hold, as AWS does.
	AssumeRoleStrict AssumeRole = iota
	// AssumeRoleLenient mints a session for any well-formed role ARN.
	AssumeRoleLenient
)

// DynamoDB holds the DynamoDB Local settings.
type DynamoDB struct {
	Mode     DynamoDBMode
	Registry Registry
	// MemoryLimit is the container's memory limit in bytes.
	MemoryLimit int64
}

// DynamoDBMode is whether the DynamoDB service runs (ADR 0014): the
// dynamodb.enabled file key.
//
//sumtype:decl
type DynamoDBMode interface{ isDynamoDBMode() }

// DynamoDBEnabled serves DynamoDB through DynamoDB Local, pulled and
// started on first use.
type DynamoDBEnabled struct{}

// DynamoDBDisabled turns the DynamoDB service off; DynamoDB Local is never
// pulled.
type DynamoDBDisabled struct{}

func (DynamoDBEnabled) isDynamoDBMode()  {}
func (DynamoDBDisabled) isDynamoDBMode() {}

// Registry is where DynamoDB Local is pulled from first.
type Registry uint8

// Registry values.
const (
	RegistryDockerHub Registry = iota
	RegistryECRPublic
)

// Journal holds the event journal settings.
type Journal struct {
	// Capacity is the most events kept in memory.
	Capacity int
	// MaxBytes bounds the estimated bytes of the events kept, which caps
	// the call bodies recorded with them.
	MaxBytes int
	// File, when set, appends every event to an NDJSON file.
	File string
}

// Log holds the log settings.
type Log struct {
	Level  slog.Level
	Format LogFormat
}

// LogFormat is how log records are written.
type LogFormat uint8

// LogFormat values.
const (
	LogText LogFormat = iota
	LogJSON
)
