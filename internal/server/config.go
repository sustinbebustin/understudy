package server

import (
	"io"
	"log/slog"
	"net/netip"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/s3"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
	"github.com/sustinbebustin/understudy/internal/service/sts"
)

// Config is what serve needs: the CLI maps config.Config onto it. The zero
// Config listens on an ephemeral loopback port with a running clock, random
// ids, text logs at info level, no persistence, no TLS listener, Docker at
// its default socket and DynamoDB Local awaiting its licence.
type Config struct {
	// Listen is the address to bind; port 0 picks a free one. An address
	// beyond loopback needs the ContainerPublished exposure, or AdminToken
	// and AWSAPIUnauthenticated.
	Listen netip.AddrPort
	// AdminToken, when set, is the bearer token every admin call on the
	// main listener must carry. It guards the admin API only.
	AdminToken string
	// Exposure is what makes a non-loopback Listen safe without a token;
	// nil is TokenRequired.
	Exposure Exposure
	// AWSAPI is whether a non-loopback Listen on the host may serve the
	// AWS API to every peer that reaches it; nil is AWSAPILoopbackOnly.
	AWSAPI AWSAPIExposure
	// Persist is where state persists; nil is Ephemeral.
	Persist Persistence
	// TLS is the TLS listener setting; nil is TLSOff.
	TLS TLS
	// ClockStart is how the emulator clock starts; nil is ClockRunning.
	ClockStart ClockStart
	// Seed makes every generated id reproducible; nil draws from crypto/rand.
	Seed *uint64
	// DefaultAccount is the account of a call whose access key understudy
	// does not know (accounts.default), and of a hot-reload binding or an
	// admin call that names none; the zero value is 000000000000.
	DefaultAccount aws.AccountID
	// Journal bounds the event journal and names its file (journal.*).
	Journal Journal
	// LoadSnapshot, when set, is an archive loaded at start, over the
	// persisted state.
	LoadSnapshot string
	// Regions are region names accepted beyond AWS's own (the top-level
	// regions array of the configuration file).
	Regions []string
	// Faults are fault rules active from the start.
	Faults []fault.Rule
	// Docker is where the Docker Engine is; nil is DockerSocket at the
	// default socket.
	Docker Docker
	// DynamoDB is the DynamoDB Local licence setting; nil is
	// DynamoDBAwaiting.
	DynamoDB DynamoDB
	// HotReload binds directories as functions' code from the start.
	HotReload []HotReload
	// Lambda bounds the execution environments; zero values take the
	// pool's defaults.
	Lambda LambdaLimits
	// LambdaNetwork is how function containers reach understudy
	// (lambda.network); nil is NetworkAuto.
	LambdaNetwork LambdaNetwork
	// DynamoDBMemory is DynamoDB Local's memory limit in bytes; 0 is 1 GiB.
	DynamoDBMemory int64
	// Switches are the fidelity switches the configuration sets; the zero
	// value is every service's default.
	Switches Switches
	// S3BaseDomains are the hosts virtual-hosted S3 addresses end in; nil
	// is localhost and s3.localhost.
	S3BaseDomains []string
	LogFormat     LogFormat
	LogLevel      slog.Level
	// ReadyFile, when set, receives {"endpoint", "pid"} once the listeners
	// accept, plus "tlsEndpoint" with the TLS listener, and is removed at
	// shutdown.
	ReadyFile string
	// OnReady, when set, is called once with the endpoints when the
	// listeners accept, after the ready file is written. It runs on Serve's
	// goroutine and must not block.
	OnReady func(Endpoints)
	// Version is reported by the health endpoint and names the snapshot
	// writer.
	Version string
	// LogOutput receives the logs and the banner; nil means stderr.
	LogOutput io.Writer
}

// SettingError is a setting New refuses as it applies it, after the
// configuration accepted its form: a fault rule the fault store rejects, a
// hot-reload binding whose scope does not parse or whose directory cannot
// be watched. It renders as config.Error does, so the caller, which knows
// the file that set the setting, can name it the same way. Nothing was
// served.
type SettingError struct {
	// Setting names the setting as the configuration file spells it:
	// "faults[0].trigger.n", "lambda.hotReload[1].dir".
	Setting string
	Reason  string
	// Hint is the next thing to try; empty when Reason says it.
	Hint string
	// Err is the refusal Reason describes.
	Err error
}

func (e *SettingError) Error() string {
	msg := "config: " + e.Setting + ": " + e.Reason
	if e.Hint != "" {
		msg += "; " + e.Hint
	}
	return msg
}

func (e *SettingError) Unwrap() error { return e.Err }

// ClockStart is how the emulator clock starts.
//
//sumtype:decl
type ClockStart interface{ isClockStart() }

// ClockRunning follows wall time.
type ClockRunning struct{}

// ClockFrozen holds emulator time still until it is advanced; a zero At
// freezes at wall time now.
type ClockFrozen struct{ At time.Time }

// ClockScaled runs Factor emulator seconds per wall second from wall time
// now (clockScale); Factor is above 0 and at most 10000, and New refuses
// any other.
type ClockScaled struct{ Factor float64 }

func (ClockRunning) isClockStart() {}
func (ClockFrozen) isClockStart()  {}
func (ClockScaled) isClockStart()  {}

// Journal is the event journal setting; a zero field takes the journal's
// default.
type Journal struct {
	// Capacity is the most events kept in memory.
	Capacity int
	// MaxBytes bounds the estimated bytes of the events kept in memory.
	MaxBytes int
	// File, when set, is an NDJSON file every event is appended to.
	File string
}

// Persistence is where emulator state persists.
//
//sumtype:decl
type Persistence interface{ isPersistence() }

// Ephemeral keeps state in memory only.
type Ephemeral struct{}

// PersistDir keeps state under Path (ARCHITECTURE.md 13): the directory is
// locked for this process, state is loaded from Path/state.snapshot at
// start, checkpointed there every SaveEvery of wall time while it changed
// and at shutdown, and blobs live under Path/blobs. Path/snapshots roots
// the admin snapshot path form. A zero SaveEvery is DefaultSaveEvery.
type PersistDir struct {
	Path      string
	SaveEvery time.Duration
}

func (Ephemeral) isPersistence()  {}
func (PersistDir) isPersistence() {}

// DefaultSaveEvery is the checkpoint interval of PersistDir.
const DefaultSaveEvery = 30 * time.Second

// TLS is the TLS listener setting.
//
//sumtype:decl
type TLS interface{ isTLS() }

// TLSOff serves plain HTTP only.
type TLSOff struct{}

// TLSSelfSigned also serves HTTPS on Listen with a certificate generated
// at start for localhost, *.localhost and 127.0.0.1; port 0 picks a free
// one. The listener serves exactly what the main listener serves.
type TLSSelfSigned struct{ Listen netip.AddrPort }

func (TLSOff) isTLS()        {}
func (TLSSelfSigned) isTLS() {}

// Exposure is what makes a listener beyond loopback safe.
//
//sumtype:decl
type Exposure interface{ isExposure() }

// TokenRequired exposes a non-loopback listener only with an admin token.
type TokenRequired struct{}

// ContainerPublished trusts the container's published port as the exposure
// control, so a non-loopback listener needs no token; New accepts it only
// inside a container (ARCHITECTURE.md 18, 20).
type ContainerPublished struct{}

func (TokenRequired) isExposure()      {}
func (ContainerPublished) isExposure() {}

// AWSAPIExposure is whether a main listener beyond loopback on the host may
// serve the AWS API. The admin token guards only /_understudy/, and this
// build verifies no request signature, so such a listener lets every peer
// that reaches it read every secret and object and run any container image
// as a Lambda function. Inside a container, ContainerPublished needs no
// AWSAPIExposure: the published port guards both APIs.
//
//sumtype:decl
type AWSAPIExposure interface{ isAWSAPIExposure() }

// AWSAPILoopbackOnly refuses a main listener beyond loopback on the host.
type AWSAPILoopbackOnly struct{}

// AWSAPIUnauthenticated accepts one with an admin token, for a network
// whose every peer is trusted (--allow-unauthenticated-aws-api).
type AWSAPIUnauthenticated struct{}

func (AWSAPILoopbackOnly) isAWSAPIExposure()    {}
func (AWSAPIUnauthenticated) isAWSAPIExposure() {}

// Docker is where the Docker Engine Lambda and DynamoDB Local run on is.
//
//sumtype:decl
type Docker interface{ isDocker() }

// DockerSocket reaches the Engine at the unix socket Path; empty is
// /var/run/docker.sock. Nothing is dialed until Lambda or DynamoDB needs
// it.
type DockerSocket struct{ Path string }

// DockerDisabled runs no container: Invoke and DynamoDB answer 501
// NotImplemented, while every control plane keeps working.
type DockerDisabled struct{}

func (DockerSocket) isDocker()   {}
func (DockerDisabled) isDocker() {}

// DynamoDB is the DynamoDB Local licence setting (ADR 0014).
//
//sumtype:decl
type DynamoDB interface{ isDynamoDB() }

// DynamoDBAwaiting runs DynamoDB Local only when an acceptance is recorded
// in the data directory; otherwise DynamoDB calls answer 501 with the
// notice.
type DynamoDBAwaiting struct{}

// DynamoDBAccepted is the licence accepted now, with
// --accept-dynamodb-local-license or UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1;
// with persistence the acceptance is recorded for later runs.
type DynamoDBAccepted struct{}

// DynamoDBDisabled turns the DynamoDB service off by configuration.
type DynamoDBDisabled struct{}

func (DynamoDBAwaiting) isDynamoDB() {}
func (DynamoDBAccepted) isDynamoDB() {}
func (DynamoDBDisabled) isDynamoDB() {}

// HotReload binds Dir as Function's code (lambda.hotReload). Account and
// Region are the function's; empty means Config.DefaultAccount and
// us-east-1.
type HotReload struct {
	Account  string
	Region   string
	Function string
	Dir      string
}

// LambdaLimits bound the Lambda execution environments (lambda.*); a zero
// field takes the default: 16 starts at once, 10 minutes idle, 64
// environments.
type LambdaLimits struct {
	StartConcurrency int
	IdleTimeout      time.Duration
	MaxEnvironments  int
}

// LambdaNetwork is how function containers reach understudy (ADR 0011).
//
//sumtype:decl
type LambdaNetwork interface{ isLambdaNetwork() }

// NetworkAuto detects the container topology at first use.
type NetworkAuto struct{}

// NetworkBridge skips detection: function containers run on a Linux bridge
// whose gateway is Gateway, where the container listener binds.
type NetworkBridge struct{ Gateway netip.Addr }

// NetworkDisabled runs no function container: Invoke answers
// ServiceException while every Lambda control plane operation and
// DynamoDB Local keep working.
type NetworkDisabled struct{}

func (NetworkAuto) isLambdaNetwork()     {}
func (NetworkBridge) isLambdaNetwork()   {}
func (NetworkDisabled) isLambdaNetwork() {}

// Switches are the configurable fidelity switches; the zero value is every
// default.
type Switches struct {
	LambdaTransitions  LambdaTransitions
	SQSDeletedRecently SQSDeletedRecently
	S3CrossRegion      S3CrossRegion
	STSAssumeRole      STSAssumeRole
}

// The switch types are the services' own, re-exported so the CLI, which
// imports no service, can set them.
type (
	// LambdaTransitions is lambda.stateTransitions.
	LambdaTransitions = lambda.StateTransitions
	// SQSDeletedRecently is sqs.queueDeletedRecently.
	SQSDeletedRecently = sqs.DeletedRecently
	// S3CrossRegion is s3.crossRegion.
	S3CrossRegion = s3.CrossRegion
	// STSAssumeRole is iam.assumeRole.
	STSAssumeRole = sts.RoleCheck
)

// Switch values, each the service's own constant.
const (
	LambdaTransitionsFinal    = lambda.TransitionsFinal
	LambdaTransitionsClock    = lambda.TransitionsClock
	SQSDeletedRecentlyIgnore  = sqs.DeletedRecentlyIgnore
	SQSDeletedRecentlyEnforce = sqs.DeletedRecentlyEnforce
	S3CrossRegionAWS          = s3.CrossRegionAWS
	S3CrossRegionLenient      = s3.CrossRegionLenient
	STSAssumeRoleMustExist    = sts.RoleMustExist
	STSAssumeRoleLenient      = sts.RoleLenient
)

// LogFormat is how log records are written.
type LogFormat uint8

// LogFormat values; the zero value is text.
const (
	LogText LogFormat = iota
	LogJSON
)
