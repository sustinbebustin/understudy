package aws

import "fmt"

// Partition is an AWS partition. Phase 1 has one, aws.
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

// RegionSource says where a request's region came from (fidelity review F9).
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
