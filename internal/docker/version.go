package docker

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// APIVersion is a Docker Engine API version such as 1.44.
type APIVersion struct {
	Major int
	Minor int
}

// MaxAPIVersion is the highest API version the client requests. It is the
// version Podman's compatibility layer reports as current, and it carries
// recursive read-only binds, multiple endpoints on create and DNSNames
// (containers.md 2.2).
func MaxAPIVersion() APIVersion { return APIVersion{Major: 1, Minor: 44} }

// DefaultMinAPIVersion is the lowest API version the client accepts unless
// Options says otherwise: 1.41 adds the platform query on container create.
func DefaultMinAPIVersion() APIVersion { return APIVersion{Major: 1, Minor: 41} }

// ParseAPIVersion parses the "major.minor" form the daemon reports in the
// Api-Version header.
func ParseAPIVersion(s string) (APIVersion, error) {
	major, minor, ok := strings.Cut(s, ".")
	if !ok {
		return APIVersion{}, fmt.Errorf("docker API version %q is not of the form major.minor", s)
	}
	ma, err := parseVersionPart(major)
	if err != nil {
		return APIVersion{}, fmt.Errorf("docker API version %q: major part: %w", s, err)
	}
	mi, err := parseVersionPart(minor)
	if err != nil {
		return APIVersion{}, fmt.Errorf("docker API version %q: minor part: %w", s, err)
	}
	return APIVersion{Major: ma, Minor: mi}, nil
}

func parseVersionPart(s string) (int, error) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, fmt.Errorf("%q is not a decimal number", s)
	}
	return strconv.Atoi(s)
}

// String formats the version as "major.minor".
func (v APIVersion) String() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor)
}

// Compare returns -1, 0 or +1 as v is older than, equal to or newer than w.
func (v APIVersion) Compare(w APIVersion) int {
	if c := cmp.Compare(v.Major, w.Major); c != 0 {
		return c
	}
	return cmp.Compare(v.Minor, w.Minor)
}

// negotiate picks the version to request from a daemon that serves up to
// server: the lower of the client's maximum and the server's, refused when it
// falls below minimum (containers.md 2.2).
func negotiate(server, minimum APIVersion) (APIVersion, error) {
	v := server
	if MaxAPIVersion().Compare(v) < 0 {
		v = MaxAPIVersion()
	}
	if v.Compare(minimum) < 0 {
		return APIVersion{}, &VersionError{Server: server, Minimum: minimum}
	}
	return v, nil
}
