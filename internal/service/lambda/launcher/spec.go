package launcher

import (
	"fmt"
	"maps"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// Lambda's resource ranges (lambda-api.md 4.1) and quotas (containers.md
// 3.2).
const (
	MinMemoryMB           = 128
	MaxMemoryMB           = 10_240
	MinEphemeralStorageMB = 512
	MaxEphemeralStorageMB = 10_240

	pidsLimit   = 1024
	nofileLimit = 1024
	taskRoot    = "/var/task"
)

// Architecture is a Lambda instruction set architecture.
type Architecture string

// The architectures Lambda supports, spelled as Lambda's Architectures
// values.
const (
	X86   Architecture = "x86_64"
	ARM64 Architecture = "arm64"
)

// ParseArchitecture reads a Lambda Architectures value.
func ParseArchitecture(s string) (Architecture, error) {
	switch a := Architecture(s); a {
	case X86, ARM64:
		return a, nil
	}
	return "", &SpecError{Field: "Architecture", Reason: fmt.Sprintf("%q is not x86_64 or arm64", s)}
}

// Platform is the OCI platform a container of this architecture runs on.
func (a Architecture) Platform() docker.Platform {
	if a == ARM64 {
		return docker.Platform{OS: "linux", Architecture: "arm64"}
	}
	return docker.Platform{OS: "linux", Architecture: "amd64"}
}

// machineArchitecture maps the daemon's Info.Architecture (uname -m) or an
// image's OCI architecture to a Lambda architecture. Any other machine
// (s390x, ppc64le, riscv64, or an empty value) is not one Lambda runs, and
// known is false.
func machineArchitecture(machine string) (arch Architecture, known bool) {
	switch machine {
	case "x86_64", "amd64":
		return X86, true
	case "aarch64", "arm64":
		return ARM64, true
	default:
		return "", false
	}
}

// Function identifies the function version an environment runs.
type Function struct {
	Name string
	// Version is "$LATEST" or a published version number.
	Version string
	Account aws.AccountID
	Region  aws.Region
}

// ARN is the function's unqualified ARN.
func (f Function) ARN() aws.ARN {
	return aws.ARN{Partition: "aws", Service: "lambda", Region: f.Region.String(), Account: f.Account.String(), Resource: "function:" + f.Name}
}

// Code is how the function's code reaches the container.
//
//sumtype:decl
type Code interface{ isCode() }

// ZipCode is an extracted zip package (a codestore tree) mounted read-only
// at /var/task, run by the runtime base image with Handler as its one
// argument.
type ZipCode struct {
	// Dir is the absolute host path of the extracted tree.
	Dir     string
	Handler string
}

// ImageCode is a container-image function; the overrides replace the
// image's own settings when set (ImageConfig, containers.md 5.3).
type ImageCode struct {
	EntryPoint       []string
	Command          []string
	WorkingDirectory string
}

func (ZipCode) isCode()   {}
func (ImageCode) isCode() {}

// Credentials are the execution role's credentials, set as the AWS SDK
// environment variables.
type Credentials struct {
	AccessKeyID     ids.AccessKeyID
	SecretAccessKey ids.SecretAccessKey
	SessionToken    ids.SessionToken
}

// Spec is everything about a function version that shapes its containers.
// Two invocations whose Specs are equal can share an environment.
type Spec struct {
	Function   Function
	Generation uint64
	// Image is the reference to run, as EnsureImage resolved it.
	Image        string
	Architecture Architecture
	Code         Code
	MemoryMB     int
	// EphemeralStorageMB is validated against Lambda's range but does not
	// cap /tmp (see the package documentation).
	EphemeralStorageMB int
	// Environment is the function's Environment.Variables.
	Environment map[string]string
	Credentials Credentials
	LogGroup    string
}

// Slot is what differs between environments of one Spec.
type Slot struct {
	// ID names the environment: the container name and the log stream end
	// in it.
	ID string
	// RuntimeAPI is the environment's Runtime API listener as the container
	// reaches it.
	RuntimeAPI netip.AddrPort
	LogStream  string
}

func (s Spec) validate() error {
	switch {
	case s.Function.Name == "" || s.Function.Version == "":
		return &SpecError{Field: "Function", Reason: "needs a name and a version"}
	case s.Function.Account.IsZero() || s.Function.Region.IsZero():
		return &SpecError{Field: "Function", Reason: "needs an account and a region"}
	case s.Image == "":
		return &SpecError{Field: "Image", Reason: "is empty; resolve it with EnsureImage first"}
	case s.MemoryMB < MinMemoryMB || s.MemoryMB > MaxMemoryMB:
		return &SpecError{Field: "MemoryMB", Reason: fmt.Sprintf("%d is outside %d to %d", s.MemoryMB, MinMemoryMB, MaxMemoryMB)}
	case s.EphemeralStorageMB < MinEphemeralStorageMB || s.EphemeralStorageMB > MaxEphemeralStorageMB:
		return &SpecError{Field: "EphemeralStorageMB", Reason: fmt.Sprintf("%d is outside %d to %d", s.EphemeralStorageMB, MinEphemeralStorageMB, MaxEphemeralStorageMB)}
	case s.Credentials.AccessKeyID == "":
		return &SpecError{Field: "Credentials", Reason: "has no access key id"}
	}
	if _, err := ParseArchitecture(string(s.Architecture)); err != nil {
		return err
	}
	switch c := s.Code.(type) {
	case ZipCode:
		if !filepath.IsAbs(c.Dir) {
			return &SpecError{Field: "Code", Reason: fmt.Sprintf("zip code directory %q is not absolute", c.Dir)}
		}
		if c.Handler == "" {
			return &SpecError{Field: "Code", Reason: "zip code needs a handler: the runtime image's entrypoint requires exactly one argument"}
		}
	case ImageCode:
	case nil:
		return &SpecError{Field: "Code", Reason: "is unset"}
	}
	return nil
}

func (s Slot) validate() error {
	if len(s.ID) < 12 {
		return &SpecError{Field: "Slot.ID", Reason: fmt.Sprintf("%q is shorter than 12 characters", s.ID)}
	}
	if !s.RuntimeAPI.IsValid() {
		return &SpecError{Field: "Slot.RuntimeAPI", Reason: "is not a valid address"}
	}
	return nil
}

// delivery is how zip code reaches /var/task.
//
//sumtype:decl
type delivery interface{ isDelivery() }

// bindMount mounts the host tree read-only; the root file system is
// read-only too.
type bindMount struct{}

// tarCopy copies the tree into the container before it starts, for when
// understudy's paths are not the daemon's: understudy runs in a container,
// or the daemon on another host. The copy needs a writable root file
// system (containers.md finding 6).
type tarCopy struct{}

func (bindMount) isDelivery() {}
func (tarCopy) isDelivery()   {}

// deliveryFor picks tar copy when understudy runs inside a container or
// the daemon on another host, and bind mounts otherwise (containers.md
// 5.1).
func deliveryFor(t topology.Topology) delivery {
	switch t.(type) {
	case topology.InContainer, topology.Remote:
		return tarCopy{}
	case topology.LinuxBridge, topology.DockerDesktop, topology.RootlessDocker, topology.Podman:
	}
	return bindMount{}
}

// containerName is unique per environment and valid as a Docker name:
// understudy-<instance>-<function>-<slot id prefix>.
func containerName(instance string, s Spec, slot Slot) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, s.Function.Name)
	return "understudy-" + instance + "-" + name + "-" + slot.ID[:12]
}

// environment is the container's Env: understudy's defaults, then the
// function's variables, then the reserved variables Lambda sets itself
// (containers.md 3.4, lambda-api.md 9.4). A function may override
// AWS_ENDPOINT_URL and TZ; it cannot override a reserved name. TZ is
// Lambda's :UTC, not the base images' :/etc/localtime (lambda-api.md 12).
func environment(s Spec, slot Slot, endpoint netip.AddrPort) []string {
	env := map[string]string{
		"AWS_ENDPOINT_URL":          "http://" + endpoint.String(),
		"AWS_EC2_METADATA_DISABLED": "true",
		"TZ":                        ":UTC",
	}
	maps.Copy(env, s.Environment)
	maps.Copy(env, map[string]string{
		"AWS_LAMBDA_RUNTIME_API":          slot.RuntimeAPI.String(),
		"AWS_LAMBDA_FUNCTION_NAME":        s.Function.Name,
		"AWS_LAMBDA_FUNCTION_VERSION":     s.Function.Version,
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": strconv.Itoa(s.MemoryMB),
		"AWS_LAMBDA_INITIALIZATION_TYPE":  "on-demand",
		"AWS_LAMBDA_LOG_GROUP_NAME":       s.LogGroup,
		"AWS_LAMBDA_LOG_STREAM_NAME":      slot.LogStream,
		"AWS_REGION":                      s.Function.Region.String(),
		"AWS_DEFAULT_REGION":              s.Function.Region.String(),
		"AWS_ACCESS_KEY_ID":               string(s.Credentials.AccessKeyID),
		"AWS_SECRET_ACCESS_KEY":           string(s.Credentials.SecretAccessKey),
		"AWS_SESSION_TOKEN":               string(s.Credentials.SessionToken),
	})
	out := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		out = append(out, k+"="+env[k])
	}
	return out
}

// containerSpec builds the create request of ARCHITECTURE.md 14.3: Mounts
// never Binds, memory equal to memory plus swap, PidsLimit, nofile,
// json-file logs with rotation, every capability dropped,
// no-new-privileges, read-only code, and a writable /tmp on disk.
func containerSpec(s Spec, slot Slot, labels map[string]string, addrs topology.Addresses, endpointPort uint16, d delivery) (docker.Config, docker.HostConfig) {
	cfg := docker.Config{
		Platform: s.Architecture.Platform(),
		Image:    s.Image,
		Env:      environment(s, slot, netip.AddrPortFrom(addrs.FunctionEndpointHost, endpointPort)),
		Labels:   labels,
	}
	memory := int64(s.MemoryMB) << 20
	host := docker.HostConfig{
		ExtraHosts:  slices.Clone(addrs.ExtraHosts),
		LogConfig:   docker.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "2"}},
		NetworkMode: addrs.FunctionNetwork,
		Memory:      memory,
		MemorySwap:  memory,
		PidsLimit:   pidsLimit,
		Ulimits:     []docker.Ulimit{{Name: "nofile", Soft: nofileLimit, Hard: nofileLimit}},
		// /tmp is an anonymous volume: disk, as on Lambda, so writes to it
		// are not charged to the memory limit the way tmpfs pages are.
		// Remove's v=1 deletes it with the container.
		Mounts:      []docker.Mount{{Type: "volume", Target: "/tmp"}},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
	}
	switch c := s.Code.(type) {
	case ZipCode:
		cfg.Cmd = []string{c.Handler}
		switch d.(type) {
		case bindMount:
			host.Mounts = append(host.Mounts, docker.Mount{Type: "bind", Source: c.Dir, Target: taskRoot, ReadOnly: true})
			host.ReadonlyRootfs = true
		case tarCopy:
		}
	case ImageCode:
		cfg.Entrypoint = slices.Clone(c.EntryPoint)
		cfg.Cmd = slices.Clone(c.Command)
		cfg.WorkingDir = c.WorkingDirectory
		host.ReadonlyRootfs = true
	}
	return cfg, host
}
