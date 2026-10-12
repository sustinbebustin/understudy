package config

import (
	"log/slog"
	"net/netip"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// Defaults worth defending (ARCHITECTURE.md 18): port 4566 and account
// 000000000000, so existing AWS_ENDPOINT_URL=http://localhost:4566 setups
// work unchanged. The component limits repeat the defaults of the packages
// that apply them (lambda/pool, journal, dynamodb/ddblocal).
const (
	DefaultPort             = 4566
	DefaultTLSPort          = 4567
	DefaultSaveEvery        = 30 * time.Second
	DefaultMaxObjectBytes   = 5 << 30
	DefaultStartConcurrency = 16
	DefaultMaxEnvironments  = 64
	DefaultIdleTimeout      = 10 * time.Minute
	DefaultJournalCapacity  = 100_000
	DefaultJournalMaxBytes  = 128 << 20
	DefaultDynamoDBMemory   = 1 << 30
	DefaultDockerSocket     = "/var/run/docker.sock"
	// MaxClockScale bounds Scaled.Factor, as the admin clock op does.
	MaxClockScale = 10000
)

// DefaultBaseDomains are the S3 base domains every configuration has.
func DefaultBaseDomains() []string { return []string{"localhost", "s3.localhost"} }

// Defaults is the configuration with nothing set: loopback 127.0.0.1:4566
// on a host; inside a container 0.0.0.0:4566 with the published port as
// the exposure control (ARCHITECTURE.md 24).
func Defaults(p Placement) Config {
	var listen Listen = Loopback{Addr: netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), DefaultPort)}
	if _, ok := p.(InContainer); ok {
		listen = Exposed{Addr: netip.AddrPortFrom(netip.IPv4Unspecified(), DefaultPort), Auth: ContainerPublished{}}
	}
	return Config{
		Listen:      listen,
		TLS:         TLSOff{},
		Persistence: Ephemeral{},
		SigV4:       ParseOnly,
		Clock:       Running{},
		Accounts:    Accounts{Default: aws.DefaultAccount()},
		Docker:      DockerSocket{Path: DefaultDockerSocket},
		S3: S3{
			BaseDomains:    DefaultBaseDomains(),
			CrossRegion:    CrossRegionAWS,
			MaxObjectBytes: DefaultMaxObjectBytes,
		},
		Lambda: Lambda{
			Network:          NetworkAuto{},
			StartConcurrency: DefaultStartConcurrency,
			IdleTimeout:      DefaultIdleTimeout,
			MaxEnvironments:  DefaultMaxEnvironments,
			StateTransitions: TransitionsFinal,
		},
		SQS:      SQS{DeletedRecently: DeletedRecentlyIgnore},
		IAM:      IAM{AssumeRole: AssumeRoleStrict},
		DynamoDB: DynamoDB{Licence: AwaitingLicence{}, Registry: RegistryDockerHub, MemoryLimit: DefaultDynamoDBMemory},
		Journal:  Journal{Capacity: DefaultJournalCapacity, MaxBytes: DefaultJournalMaxBytes},
		Log:      Log{Level: slog.LevelInfo, Format: LogText},
	}
}
