package aws

import "fmt"

// Partition is an AWS partition. understudy serves only aws.
type Partition struct{ name string }

// PartitionAWS is the commercial partition, aws.
func PartitionAWS() Partition { return Partition{name: "aws"} }

// String returns the partition name.
func (p Partition) String() string { return p.name }

// MarshalText encodes the partition name so persisted state keeps it.
func (p Partition) MarshalText() ([]byte, error) { return []byte(p.name), nil }

// PartitionError reports a partition understudy does not serve.
type PartitionError struct {
	Name string
}

func (e *PartitionError) Error() string {
	return fmt.Sprintf("partition %q is not served: understudy serves only the aws partition", e.Name)
}

// UnmarshalText accepts the names of the partitions understudy serves; the
// empty text decodes to the zero value.
func (p *Partition) UnmarshalText(text []byte) error {
	switch name := string(text); name {
	case "":
		*p = Partition{}
	case PartitionAWS().name:
		*p = PartitionAWS()
	default:
		return &PartitionError{Name: name}
	}
	return nil
}

// Scope is where a resource lives: the unit of state partitioning and of
// fault and journal filtering.
type Scope struct {
	Partition Partition
	Account   AccountID
	// Region is ignored by IAM and STS, whose state is per account; S3 buckets
	// have a home region.
	Region Region
}

// ScopePart is the part of a scope a ScopeError is about.
type ScopePart uint8

// ScopePart values.
const (
	ScopeAccount ScopePart = iota + 1
	ScopeRegion
	ScopePartition
)

// String returns the part's name: "account", "region" or "partition".
func (p ScopePart) String() string {
	switch p {
	case ScopeRegion:
		return "region"
	case ScopePartition:
		return "partition"
	case ScopeAccount:
	}
	return "account"
}

// ScopeError is a partition, account or region ParseScope or ScopeOf
// refused.
type ScopeError struct {
	Part ScopePart
	Err  error
}

func (e *ScopeError) Error() string { return e.Err.Error() }

func (e *ScopeError) Unwrap() error { return e.Err }

// ParseScope reads an account and a region given as text, each empty for
// its value in defaults: the account as 12 digits, the region as a name
// regions accepts. A refused part is a *ScopeError naming it.
func ParseScope(account, region string, defaults Scope, regions Regions) (Scope, error) {
	s := defaults
	if account != "" {
		a, err := ParseAccountID(account)
		if err != nil {
			return defaults, &ScopeError{Part: ScopeAccount, Err: err}
		}
		s.Account = a
	}
	if region != "" {
		r, err := regions.Parse(region)
		if err != nil {
			return defaults, &ScopeError{Part: ScopeRegion, Err: err}
		}
		s.Region = r
	}
	return s, nil
}

// ScopeOf is the scope a regional ARN names: a partition understudy
// serves, a 12-digit account and a region of valid syntax. The region need
// not be one the configuration accepts, since an ARN may name a resource in
// a region no request reached. An ARN without a partition, account or
// region (an S3 or IAM ARN) names no regional scope; a refused part is a
// *ScopeError naming it.
func ScopeOf(a ARN) (Scope, error) {
	var s Scope
	if err := s.Partition.UnmarshalText([]byte(a.Partition)); err != nil || s.Partition == (Partition{}) {
		return Scope{}, &ScopeError{Part: ScopePartition, Err: &PartitionError{Name: a.Partition}}
	}
	account, err := ParseAccountID(a.Account)
	if err != nil {
		return Scope{}, &ScopeError{Part: ScopeAccount, Err: err}
	}
	s.Account = account
	if err := s.Region.UnmarshalText([]byte(a.Region)); err != nil {
		return Scope{}, &ScopeError{Part: ScopeRegion, Err: err}
	}
	if s.Region.IsZero() {
		return Scope{}, &ScopeError{Part: ScopeRegion, Err: &RegionError{Name: a.Region, Malformed: true}}
	}
	return s, nil
}

// RegionSource says where a request's region came from.
//
//sumtype:decl
type RegionSource interface{ isRegionSource() }

// CredentialScope is the region of a SigV4 credential scope.
type CredentialScope struct{ Region Region }

// HostRegion is the region of a <svc>.<region>.<base> host.
type HostRegion struct{ Region Region }

// None is a request that asserts no region: SigV2, presigned SigV2, unsigned
// and anonymous requests. S3 then serves the bucket's home region; other
// services use DefaultRegion.
type None struct{}

func (CredentialScope) isRegionSource() {}
func (HostRegion) isRegionSource()      {}
func (None) isRegionSource()            {}
