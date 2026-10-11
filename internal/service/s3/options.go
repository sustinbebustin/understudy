package s3

import "github.com/sustinbebustin/understudy/internal/aws"

// Options are the S3 settings wire.go fills from the configuration. The
// zero value is AWS's behavior over the real AWS region names.
type Options struct {
	// CrossRegion is how a request that asserts a region other than the
	// bucket's home region is answered (s3.crossRegion, ADR 0013).
	CrossRegion CrossRegion
	// Regions is the table location constraints and host regions are read
	// against; the zero value holds the real AWS region names.
	Regions aws.Regions
}

// CrossRegion is the s3.crossRegion setting.
type CrossRegion uint8

// CrossRegion values.
const (
	// CrossRegionAWS answers as AWS does: AuthorizationHeaderMalformed for a
	// request signed for another region, PermanentRedirect for an unsigned
	// one addressed to another region's host.
	CrossRegionAWS CrossRegion = iota
	// CrossRegionLenient serves the bucket whatever region the request
	// asserts, and logs a warning naming both regions.
	CrossRegionLenient
)

// String returns the configuration spelling: aws or lenient.
func (c CrossRegion) String() string {
	if c == CrossRegionLenient {
		return "lenient"
	}
	return "aws"
}
