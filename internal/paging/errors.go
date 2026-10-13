package paging

import "fmt"

// MarkerReason is why a marker was refused.
type MarkerReason int

const (
	// Undecodable is a marker that is not unpadded base64url.
	Undecodable MarkerReason = iota + 1
	// OtherListing is a marker another listing issued.
	OtherListing
	// Malformed is a marker that decodes to something other than
	// "v1|<listing>|<key>" with a non-empty key: another format version,
	// or text no listing issued.
	Malformed
)

func (r MarkerReason) String() string {
	switch r {
	case Undecodable:
		return "it is not unpadded base64url"
	case OtherListing:
		return "another listing issued it"
	case Malformed:
		return "it does not name a format version, listing and key this build issues"
	}
	return fmt.Sprintf("MarkerReason(%d)", int(r))
}

// MarkerError is a marker that is not a well-formed marker of Listing, so
// no page of it issued the marker. No page was served; the caller starts
// the listing again without a marker.
type MarkerError struct {
	Marker  string
	Listing Listing
	Reason  MarkerReason
}

func (e *MarkerError) Error() string {
	return fmt.Sprintf("paging: marker %q is not one a page of listing %q issued: %s; start the listing again without a marker", e.Marker, e.Listing, e.Reason)
}
