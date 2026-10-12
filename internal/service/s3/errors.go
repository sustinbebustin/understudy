package s3

import (
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The message builders over the generated constructors. Texts are the ones
// recorded against AWS (s3.md 3 and 4, wire-rest.md 3.6) unless a comment
// says otherwise.

// malformedXMLMessage is S3's MalformedXML text.
const malformedXMLMessage = "The XML you provided was not well-formed or did not validate against our published schema"

// regionHeader is the response header naming a bucket's home region.
const regionHeader = "x-amz-bucket-region"

// The not-configured texts of the bucket subresources (s3.md 4.1,
// wire-rest.md 3.6).
const (
	noSuchBucketPolicyMessage   = "The bucket policy does not exist"
	noSuchCORSMessage           = "The CORS configuration does not exist"
	noSuchWebsiteMessage        = "The specified bucket does not have a website configuration"
	noSuchLifecycleMessage      = "The lifecycle configuration does not exist"
	noSuchReplicationMessage    = "The replication configuration was not found"
	noSuchObjectLockMessage     = "Object Lock configuration does not exist for this bucket"
	noSuchPublicAccessMessage   = "The public access block configuration was not found"
	noSuchOwnershipMessage      = "The bucket ownership controls were not found"
	noSuchTagSetMessage         = "The TagSet does not exist"
	noSuchConfigurationMessage  = "The specified configuration does not exist."
	invalidContinuationMessage  = "The continuation token provided is incorrect"
	invalidExpectedOwnerMessage = "The value of the expected bucket owner parameter must be an AWS Account ID"
	invalidAccountIDMessage     = "The value of the x-amz-account-id header must be an AWS Account ID"
)

// withField returns e with the context element name set to value.
func withField(e *awserr.Error, name, value string) *awserr.Error {
	e.Fields = append(e.Fields, awserr.Field{Name: name, Value: value})
	return e
}

// inRegion returns e carrying x-amz-bucket-region, as AWS answers every
// error about a bucket it found (ADR 0013). read, change and DeleteBucket
// apply it to every error they answer about an existing bucket, so the
// builders below leave it out; e keeps a region header it already has.
func inRegion(e *awserr.Error, h Home) *awserr.Error {
	if e == nil || slices.ContainsFunc(e.Headers, func(x awserr.Header) bool { return strings.EqualFold(x.Name, regionHeader) }) {
		return e
	}
	e.Headers = append(e.Headers, awserr.Header{Name: regionHeader, Value: h.Region.String()})
	return e
}

func errNoSuchBucket(name string) *awserr.Error {
	return withField(s3api.ErrNoSuchBucket("The specified bucket does not exist"), "BucketName", name)
}

func errInvalidBucketName(name string) *awserr.Error {
	return withField(s3api.ErrInvalidBucketName("The specified bucket is not valid."), "BucketName", name)
}

func errBucketAlreadyExists(name string) *awserr.Error {
	return withField(s3api.ErrBucketAlreadyExists("The requested bucket name is not available. The bucket namespace is shared by all users of the system. Please select a different name and try again."), "BucketName", name)
}

func errBucketAlreadyOwnedByYou(name string) *awserr.Error {
	return withField(s3api.ErrBucketAlreadyOwnedByYou("Your previous request to create the named bucket succeeded and you already own it."), "BucketName", name)
}

func errInvalidLocationConstraint(constraint string) *awserr.Error {
	return withField(s3api.ErrInvalidLocationConstraint("The specified location-constraint is not valid"), "LocationConstraint", constraint)
}

// errIllegalLocationConstraint names the constraint, or says it is
// missing when constraint is empty.
func errIllegalLocationConstraint(constraint string) *awserr.Error {
	if constraint == "" {
		return s3api.ErrIllegalLocationConstraintException("The unspecified location constraint is incompatible for the region specific endpoint this request was sent to.")
	}
	return s3api.ErrIllegalLocationConstraintException("The " + constraint + " location constraint is incompatible for the region specific endpoint this request was sent to.")
}

// errBucketNotEmpty answers DeleteBucket of a bucket that still holds
// objects: for a bucket versioning was ever set on, whose objects are
// versions and delete markers, AWS adds "You must delete all versions in
// the bucket." (s3.md 3, AWS-recorded).
func errBucketNotEmpty(b *Bucket) *awserr.Error {
	msg := "The bucket you tried to delete is not empty"
	if b.Versioning != nil && b.Versioning.Status != nil {
		msg += ". You must delete all versions in the bucket."
	}
	return withField(s3api.ErrBucketNotEmpty(msg), "BucketName", b.Name)
}

// errWrongRegion answers a request signed for region asserted at a bucket
// whose home is another region (ADR 0013).
func errWrongRegion(asserted string, h Home) *awserr.Error {
	e := s3api.ErrAuthorizationHeaderMalformed("The authorization header is malformed; the region '" + asserted + "' is wrong; expecting '" + h.Region.String() + "'")
	return withField(e, "Region", h.Region.String())
}

// errPermanentRedirect answers an unsigned request addressed to a host of
// another region with the endpoint to use instead.
func errPermanentRedirect(name, endpoint string) *awserr.Error {
	e := s3api.ErrPermanentRedirect("The bucket you are attempting to access must be addressed using the specified endpoint. Please send all future requests to this endpoint.")
	return withField(withField(e, "Endpoint", endpoint), "Bucket", name)
}

// errAccessDenied answers x-amz-expected-bucket-owner naming an account
// that does not own the bucket.
func errAccessDenied() *awserr.Error {
	return s3api.ErrAccessDenied("Access Denied")
}

// notConfigured is a subresource's not-configured error about bucket b,
// with its BucketName context element.
func notConfigured(e *awserr.Error, b *Bucket) *awserr.Error {
	return withField(e, "BucketName", b.Name)
}

// errNoSuchConfiguration answers an analytics, inventory, metrics or
// intelligent-tiering id the bucket does not hold; it carries no
// BucketName (s3.md 4.1).
func errNoSuchConfiguration() *awserr.Error {
	return s3api.ErrNoSuchConfiguration(noSuchConfigurationMessage)
}

// errMalformedACL is an AccessControlPolicy body S3 refuses as a whole
// (test_s3_bucket_acl_exceptions): S3's text is MalformedXML's.
func errMalformedACL() *awserr.Error {
	return s3api.ErrMalformedACLError(malformedXMLMessage)
}

func errMalformedXML() *awserr.Error {
	return s3api.ErrMalformedXML(malformedXMLMessage)
}

// errInvalidArgument names the argument and echoes its value.
func errInvalidArgument(message, name, value string) *awserr.Error {
	return withField(withField(s3api.ErrInvalidArgument(message), "ArgumentName", name), "ArgumentValue", value)
}

func errInvalidTag(message, key string) *awserr.Error {
	return withField(s3api.ErrInvalidTag(message), "TagKey", key)
}

func errInvalidTagValue(key, value string) *awserr.Error {
	return withField(errInvalidTag("The TagValue you have provided is invalid", key), "TagValue", value)
}

func errMalformedPolicy(message string) *awserr.Error {
	return s3api.ErrMalformedPolicy(message)
}

// errACLsDisabled answers an ACL write to a bucket whose ownership is
// BucketOwnerEnforced. The code is documented; the text is UNVERIFIED.
func errACLsDisabled() *awserr.Error {
	return s3api.ErrAccessControlListNotSupported("The bucket does not allow ACLs")
}

// errPublicACLAtCreate answers a CreateBucket with a canned ACL other than
// private while ownership is BucketOwnerEnforced (inferred, s3.md 3.2).
func errPublicACLAtCreate() *awserr.Error {
	return s3api.ErrInvalidBucketAclWithObjectOwnership("Bucket cannot have ACLs set with ObjectOwnership's BucketOwnerEnforced setting")
}

func errVersioningLocked() *awserr.Error {
	return s3api.ErrInvalidBucketState("An Object Lock configuration is present on this bucket, so the versioning state cannot be changed.")
}

func errObjectLockNeedsVersioning() *awserr.Error {
	return s3api.ErrInvalidBucketState("Versioning must be 'Enabled' on the bucket to apply a Object Lock configuration")
}

func errReplicationNeedsVersioning() *awserr.Error {
	return errInvalidRequest("Versioning must be 'Enabled' on the bucket to apply a replication configuration")
}

func errInvalidRequest(message string) *awserr.Error {
	return s3api.ErrInvalidRequest(message)
}

// errNotFound is HeadBucket's answer for a missing bucket: 404 without a
// body, which SDKs report as NotFound (s3.md 3.3).
func errNotFound() *awserr.Error {
	return s3api.ErrNotFound("Not Found")
}

// errInvalidBucketRegion answers a ListBuckets bucket-region that is not a
// region (s3.md 3.3).
func errInvalidBucketRegion(region string) *awserr.Error {
	return withField(s3api.ErrInvalidArgument("Argument value "+region+" is not a valid AWS Region"), "ArgumentName", "bucket-region")
}

// errInvalidContinuationToken answers a ListBuckets token understudy did
// not mint. The text is UNVERIFIED.
func errInvalidContinuationToken(token string) *awserr.Error {
	return errInvalidArgument(invalidContinuationMessage, "continuation-token", token)
}

// errInvalidOwner answers an owner header that is not an account id, under
// the name of the header that carried it. Both texts are UNVERIFIED
// (s3.md 4.2.5).
func errInvalidOwner(o ownerClaim) *awserr.Error {
	message := invalidExpectedOwnerMessage
	if o.header == accountIDHeader {
		message = invalidAccountIDMessage
	}
	return errInvalidArgument(message, string(o.header), o.account)
}

// errCannedWithGrants answers a request with both a canned ACL and
// x-amz-grant-* headers.
func errCannedWithGrants() *awserr.Error {
	return errInvalidRequest("Specifying both Canned ACLs and Header Grants is not allowed")
}

// errUnexpectedContent answers a PutBucketAcl that sends an
// AccessControlPolicy body together with a canned ACL or grant headers.
func errUnexpectedContent() *awserr.Error {
	return s3api.ErrUnexpectedContent("This request does not support content")
}

// errMissingSecurityHeader answers a PutBucketAcl that names no ACL at all
// (s3.md 4.2.4).
func errMissingSecurityHeader() *awserr.Error {
	return withField(s3api.ErrMissingSecurityHeader("Your request was missing a required header"), "MissingHeaderName", "x-amz-acl")
}
