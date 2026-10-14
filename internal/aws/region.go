package aws

import (
	"fmt"
	"maps"
)

// Region is an AWS region name accepted by a Regions table. Regions.Parse is
// the only validating constructor; the zero value is the absent region.
type Region struct{ name string }

// DefaultRegion is us-east-1, the region non-S3 calls use when the request
// asserts none (ARCHITECTURE.md section 7).
func DefaultRegion() Region { return Region{name: "us-east-1"} }

// String returns the region name.
func (r Region) String() string { return r.name }

// IsZero reports whether r is the absent region.
func (r Region) IsZero() bool { return r.name == "" }

// MarshalText encodes the region name so persisted state keeps it.
func (r Region) MarshalText() ([]byte, error) { return []byte(r.name), nil }

// UnmarshalText restores a persisted region. It checks the name's syntax only:
// the name was validated against the table, which may hold configured extras,
// when it was first admitted.
func (r *Region) UnmarshalText(text []byte) error {
	name := string(text)
	if name != "" && !validRegionSyntax(name) {
		return &RegionError{Name: name, Malformed: true}
	}
	*r = Region{name: name}
	return nil
}

// RegionError reports a region name the table does not accept.
type RegionError struct {
	Name string
	// Malformed is set when the name is not lowercase letters, digits and
	// inner hyphens, so no configuration could make it valid.
	Malformed bool
}

func (e *RegionError) Error() string {
	if e.Malformed {
		return fmt.Sprintf("region %q is malformed: a region name is lowercase letters, digits and inner hyphens, such as us-east-1", e.Name)
	}
	return fmt.Sprintf("region %q is not an AWS region name; to use it, list it in the regions array of the configuration file (understudy.json by default), such as {\"regions\": [%q]}", e.Name, e.Name)
}

// Regions is the table of region names understudy accepts: the real AWS
// commercial-partition regions plus configured extras.
type Regions struct{ extra map[string]struct{} }

// KnownRegions is the table of real AWS region names with no extras.
func KnownRegions() Regions { return Regions{} }

// WithExtra returns a table that also accepts names. Each name must be
// lowercase letters, digits and inner hyphens.
func (t Regions) WithExtra(names ...string) (Regions, error) {
	extra := maps.Clone(t.extra)
	if extra == nil {
		extra = make(map[string]struct{}, len(names))
	}
	for _, n := range names {
		if !validRegionSyntax(n) {
			return t, &RegionError{Name: n, Malformed: true}
		}
		extra[n] = struct{}{}
	}
	return Regions{extra: extra}, nil
}

// Parse returns the region named name when the table accepts it.
func (t Regions) Parse(name string) (Region, error) {
	if isAWSRegion(name) {
		return Region{name: name}, nil
	}
	if _, ok := t.extra[name]; ok {
		return Region{name: name}, nil
	}
	return Region{}, &RegionError{Name: name, Malformed: !validRegionSyntax(name)}
}

func validRegionSyntax(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' || name[len(name)-1] == '-' {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// isAWSRegion lists the regions of the aws partition, the only partition in
// phase 1. Terraform's aws_region data source rejects names it does not know
// (tf-remaining.md 10), so understudy accepts the same set by default.
func isAWSRegion(name string) bool {
	switch name {
	case "af-south-1",
		"ap-east-1", "ap-east-2",
		"ap-northeast-1", "ap-northeast-2", "ap-northeast-3",
		"ap-south-1", "ap-south-2",
		"ap-southeast-1", "ap-southeast-2", "ap-southeast-3", "ap-southeast-4",
		"ap-southeast-5", "ap-southeast-6", "ap-southeast-7",
		"ca-central-1", "ca-west-1",
		"eu-central-1", "eu-central-2",
		"eu-north-1",
		"eu-south-1", "eu-south-2",
		"eu-west-1", "eu-west-2", "eu-west-3",
		"il-central-1",
		"me-central-1", "me-south-1",
		"mx-central-1",
		"sa-east-1",
		"us-east-1", "us-east-2",
		"us-west-1", "us-west-2":
		return true
	default:
		return false
	}
}
