package s3

import (
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// Bucket naming rules (s3.md 3.1, wire-rest.md 3.1).
const (
	minBucketName = 3
	maxBucketName = 63
	// accountRegionalSuffix ends a name only in the account regional
	// namespace.
	accountRegionalSuffix = "-an"
)

// reservedPrefixes and reservedSuffixes are the forms AWS keeps for its
// own features. amzn-s3-demo- is reserved too, but every AWS example uses
// it, so it is allowed here (s3.md 14.1).
func reservedPrefixes() []string { return []string{"xn--", "sthree-"} }

func reservedSuffixes() []string {
	return []string{"-s3alias", "--ol-s3", ".mrap", "--x-s3", "--table-s3"}
}

// validBucketName reports whether name is a general purpose bucket name a
// CreateBucket in namespace may use: 3 to 63 lowercase letters, digits,
// dots and hyphens, beginning and ending with a letter or digit, without
// "..", not shaped like an IPv4 address, and free of the reserved
// prefixes and suffixes.
func validBucketName(name string, namespace *s3api.BucketNamespace) bool {
	if len(name) < minBucketName || len(name) > maxBucketName {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if !isLowerAlnum(c) && c != '.' && c != '-' {
			return false
		}
	}
	if !isLowerAlnum(name[0]) || !isLowerAlnum(name[len(name)-1]) || strings.Contains(name, "..") || ipv4Shaped(name) {
		return false
	}
	for _, p := range reservedPrefixes() {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	for _, s := range reservedSuffixes() {
		if strings.HasSuffix(name, s) {
			return false
		}
	}
	regional := namespace != nil && *namespace == s3api.BucketNamespaceAccountRegional
	return regional || !strings.HasSuffix(name, accountRegionalSuffix)
}

func isLowerAlnum(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

// ipv4Shaped reports whether name is four dot-separated decimal numbers,
// such as 192.168.5.4.
func ipv4Shaped(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

// parseOwnership reads the x-amz-object-ownership header of CreateBucket;
// absent means AWS's default, BucketOwnerEnforced.
func parseOwnership(v *s3api.ObjectOwnership) (s3api.ObjectOwnership, *awserr.Error) {
	if v == nil {
		return s3api.ObjectOwnershipBucketOwnerEnforced, nil
	}
	if !v.Known() {
		return "", errInvalidArgument("Invalid x-amz-object-ownership header: "+string(*v), "x-amz-object-ownership", string(*v))
	}
	return *v, nil
}

// The grantee groups a grant header or document may name by URI.
const (
	allUsersURI           = "http://acs.amazonaws.com/groups/global/AllUsers"
	authenticatedUsersURI = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers"
	logDeliveryURI        = "http://acs.amazonaws.com/groups/s3/LogDelivery"
)

// knownGroup reports whether uri is a grantee group URI S3 knows.
func knownGroup(uri string) bool {
	return uri == allUsersURI || uri == authenticatedUsersURI || uri == logDeliveryURI
}

// canonicalUserID reports whether id has the canonical user form: 64
// lowercase hexadecimal digits.
func canonicalUserID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// checkACP refuses an AccessControlPolicy body S3 refuses, with the answers
// recorded against AWS (test_s3.snapshot.json test_s3_bucket_acl_exceptions
// at 8b9a79f): no Owner, or a grant without its grantee or permission,
// MalformedACLError; an Owner or CanonicalUser grantee id that is not a
// canonical user id, InvalidArgument naming CanonicalUser/ID; a Group
// grantee URI that names no S3 group, InvalidArgument naming Group/URI. A
// Permission or grantee Type S3 does not know, which it answers
// MalformedACLError too, is refused earlier by the generated enum check,
// as InvalidArgument in the Coral grammar (package doc).
func checkACP(acp *s3api.AccessControlPolicy) *awserr.Error {
	if acp.Owner == nil {
		return errMalformedACL()
	}
	if id := acp.Owner.ID; id != nil && !canonicalUserID(*id) {
		return errInvalidArgument("Invalid id", "CanonicalUser/ID", *id)
	}
	for _, g := range acp.Grants {
		if g.Permission == nil || g.Grantee == nil {
			return errMalformedACL()
		}
		switch g.Grantee.Type {
		case s3api.TypeCanonicalUser:
			if id := g.Grantee.ID; id == nil || !canonicalUserID(*id) {
				return errInvalidArgument("Invalid id", "CanonicalUser/ID", deref(id))
			}
		case s3api.TypeGroup:
			if uri := g.Grantee.URI; uri == nil || !knownGroup(*uri) {
				return errInvalidArgument("Invalid group uri", "Group/URI", deref(uri))
			}
		case s3api.TypeAmazonCustomerByEmail:
		}
	}
	return nil
}

// deref is *p, or the zero value for nil.
func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// aclRequest is the access control a request asks for: a canned ACL, the
// x-amz-grant-* headers, or neither.
type aclRequest struct {
	canned *s3api.BucketCannedACL
	// headers are the x-amz-grant-* headers by permission, in the order
	// AWS lists them.
	headers []grantHeader
}

// grantHeader is one x-amz-grant-* header.
type grantHeader struct {
	name       string
	permission s3api.Permission
	value      *string
}

// grantHeaders lists a request's x-amz-grant-* headers.
func grantHeaders(fullControl, read, readACP, write, writeACP *string) []grantHeader {
	return []grantHeader{
		{name: "x-amz-grant-full-control", permission: s3api.PermissionFullControl, value: fullControl},
		{name: "x-amz-grant-read", permission: s3api.PermissionRead, value: read},
		{name: "x-amz-grant-read-acp", permission: s3api.PermissionReadAcp, value: readACP},
		{name: "x-amz-grant-write", permission: s3api.PermissionWrite, value: write},
		{name: "x-amz-grant-write-acp", permission: s3api.PermissionWriteAcp, value: writeACP},
	}
}

// hasHeaderGrants reports whether any x-amz-grant-* header was sent.
func (r aclRequest) hasHeaderGrants() bool {
	for _, h := range r.headers {
		if h.value != nil {
			return true
		}
	}
	return false
}

// empty reports whether the request asks for no access control at all.
func (r aclRequest) empty() bool { return r.canned == nil && !r.hasHeaderGrants() }

// private reports whether the request's access control is the owner's
// alone: no ACL, or the canned private ACL, which BucketOwnerEnforced
// accepts.
func (r aclRequest) private() bool {
	return !r.hasHeaderGrants() && (r.canned == nil || *r.canned == s3api.BucketCannedACLPrivate)
}

// grants resolves the request to the grant list of a bucket owned by
// owner: the canned ACL's grants, else the header grants, else the owner's
// FULL_CONTROL alone.
func (r aclRequest) grants(owner aws.AccountID) ([]s3api.Grant, *awserr.Error) {
	if r.canned != nil && r.hasHeaderGrants() {
		return nil, errCannedWithGrants()
	}
	if r.canned != nil {
		return cannedGrants(*r.canned, owner), nil
	}
	if !r.hasHeaderGrants() {
		return cannedGrants(s3api.BucketCannedACLPrivate, owner), nil
	}
	var out []s3api.Grant
	for _, h := range r.headers {
		if h.value == nil {
			continue
		}
		gs, err := parseGrantHeader(h)
		if err != nil {
			return nil, err
		}
		out = append(out, gs...)
	}
	return out, nil
}

// cannedGrants is the grant list of a canned ACL (s3.md 4.2.4).
func cannedGrants(acl s3api.BucketCannedACL, owner aws.AccountID) []s3api.Grant {
	out := []s3api.Grant{ownerGrant(owner)}
	group := func(uri string, p s3api.Permission) s3api.Grant {
		return s3api.Grant{Grantee: &s3api.Grantee{Type: s3api.TypeGroup, URI: new(uri)}, Permission: new(p)}
	}
	switch acl {
	case s3api.BucketCannedACLPublicRead:
		out = append(out, group(allUsersURI, s3api.PermissionRead))
	case s3api.BucketCannedACLPublicReadWrite:
		out = append(out, group(allUsersURI, s3api.PermissionRead), group(allUsersURI, s3api.PermissionWrite))
	case s3api.BucketCannedACLAuthenticatedRead:
		out = append(out, group(authenticatedUsersURI, s3api.PermissionRead))
	case s3api.BucketCannedACLPrivate:
	}
	return out
}

// ownerGrant is the owner's FULL_CONTROL grant every new bucket has.
func ownerGrant(owner aws.AccountID) s3api.Grant {
	return s3api.Grant{
		Grantee:    &s3api.Grantee{Type: s3api.TypeCanonicalUser, ID: new(canonicalID(owner))},
		Permission: new(s3api.PermissionFullControl),
	}
}

// parseGrantHeader reads one x-amz-grant-* header: a comma-separated list
// of id="...", uri="..." and emailAddress="..." grantees.
func parseGrantHeader(h grantHeader) ([]s3api.Grant, *awserr.Error) {
	var out []s3api.Grant
	for item := range strings.SplitSeq(*h.value, ",") {
		key, raw, ok := strings.Cut(strings.TrimSpace(item), "=")
		value := strings.Trim(strings.TrimSpace(raw), `"`)
		if !ok || value == "" {
			return nil, errInvalidArgument("Argument format not recognized", h.name, *h.value)
		}
		var g s3api.Grantee
		switch key {
		case "id":
			if !canonicalUserID(value) {
				return nil, errInvalidArgument("Invalid id", "id", value)
			}
			g = s3api.Grantee{Type: s3api.TypeCanonicalUser, ID: new(value)}
		case "uri":
			if !knownGroup(value) {
				return nil, errInvalidArgument("Invalid group uri", "uri", value)
			}
			g = s3api.Grantee{Type: s3api.TypeGroup, URI: new(value)}
		case "emailAddress":
			g = s3api.Grantee{Type: s3api.TypeAmazonCustomerByEmail, EmailAddress: new(value)}
		default:
			return nil, errInvalidArgument("Argument format not recognized", h.name, *h.value)
		}
		out = append(out, s3api.Grant{Grantee: &g, Permission: new(h.permission)})
	}
	return out, nil
}
