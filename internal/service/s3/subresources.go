package s3

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The bucket subresources Terraform reads (tf-storage.md 2.2, s3.md 4).
// Every Get answers the subresource's not-configured error or default
// document while it is unset (s3.md 4.1); a Put stores the document as
// sent, so the Get echoes it; a Delete restores the unset answer. Stateless
// document checks run at step 7 through kernel.WithCheck; the checks that
// read the bucket run in the write.

// stored is the coverage of a subresource understudy stores and echoes
// without acting on it.
func stored(what string) registry.StoredOnly {
	return registry.StoredOnly{Note: what + " stored and echoed; not applied"}
}

// bindSubresources binds the Get, Put and Delete operations of every
// bucket subresource except tagging and notification.
func (s *Service) bindSubresources(b s3api.Binder) {
	s.bindVersioning(b)
	s.bindACL(b)
	s.bindCORS(b)
	s.bindWebsite(b)
	s.bindPolicy(b)
	s.bindLogging(b)
	s.bindLifecycle(b)
	s.bindReplication(b)
	s.bindEncryption(b)
	s.bindObjectLock(b)
	s.bindRequestPayment(b)
	s.bindAccelerate(b)
	s.bindPublicAccessBlock(b)
	s.bindOwnership(b)
	s.bindMetrics(b)
	s.bindAnalytics(b)
	s.bindInventory(b)
	s.bindIntelligentTiering(b)
}

func (s *Service) bindVersioning(b s3api.Binder) {
	get := func(in *s3api.GetBucketVersioningInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketVersioning(reader(s, get, func(_ *s3api.GetBucketVersioningInput, b *Bucket) (*s3api.GetBucketVersioningOutput, *awserr.Error) {
		v := b.Versioning
		if v == nil {
			return s3api.NotConfiguredGetBucketVersioning(), nil
		}
		out := &s3api.GetBucketVersioningOutput{Status: v.Status}
		if v.MFADelete != nil {
			out.MFADelete = new(s3api.MFADeleteStatus(*v.MFADelete))
		}
		return out, nil
	}), registry.Full{}, touches(get))
	put := func(in *s3api.PutBucketVersioningInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketVersioning(writer[s3api.PutBucketVersioningInput, s3api.PutBucketVersioningOutput](s, put, func(in *s3api.PutBucketVersioningInput, b *Bucket) *awserr.Error {
		cfg := in.VersioningConfiguration
		if b.ObjectLock != nil && *cfg.Status != s3api.BucketVersioningStatusEnabled {
			return errVersioningLocked()
		}
		b.Versioning = &cfg
		return nil
	}), registry.Partial{Note: "MFA delete is stored, not enforced, and the x-amz-mfa header is not verified"}, touches(put),
		kernel.WithCheck(func(in *s3api.PutBucketVersioningInput) *awserr.Error {
			st := in.VersioningConfiguration.Status
			switch {
			case st == nil:
				return s3api.ErrIllegalVersioningConfigurationException("The Versioning element must be specified")
			case !st.Known():
				return errMalformedXML()
			}
			return nil
		}))
}

func (s *Service) bindACL(b s3api.Binder) {
	get := func(in *s3api.GetBucketAclInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketAcl(reader(s, get, func(_ *s3api.GetBucketAclInput, b *Bucket) (*s3api.GetBucketAclOutput, *awserr.Error) {
		return &s3api.GetBucketAclOutput{Owner: &s3api.Owner{ID: new(canonicalID(b.Home.OwnerAccount))}, Grants: b.Grants}, nil
	}), stored("the access control list is"), touches(get))
	put := func(in *s3api.PutBucketAclInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketAcl(writer[s3api.PutBucketAclInput, s3api.PutBucketAclOutput](s, put, putACL), stored("the access control list is"), touches(put),
		kernel.WithCheck(checkACLRequest))
}

// checkACLRequest applies PutBucketAcl's request rules in the order AWS
// answers them (test_s3.snapshot.json put-bucket-two-type-acl and
// put-bucket-two-type-acl-acp): no ACL at all, then a canned ACL with
// grant headers, then either with an AccessControlPolicy body; then the
// body or each grant header on its own (test_s3_bucket_acl_exceptions).
// Running at step 7, they come before a BucketOwnerEnforced bucket's
// AccessControlListNotSupported, as the recording, made on such a bucket,
// shows.
func checkACLRequest(in *s3api.PutBucketAclInput) *awserr.Error {
	req := aclOf(in)
	switch {
	case in.AccessControlPolicy == nil && req.empty():
		return errMissingSecurityHeader()
	case req.canned != nil && req.hasHeaderGrants():
		return errCannedWithGrants()
	case in.AccessControlPolicy != nil && !req.empty():
		return errUnexpectedContent()
	case in.AccessControlPolicy != nil:
		return checkACP(in.AccessControlPolicy)
	}
	for _, h := range req.headers {
		if h.value == nil {
			continue
		}
		if _, err := parseGrantHeader(h); err != nil {
			return err
		}
	}
	return nil
}

// aclOf is the canned ACL and grant headers of a PutBucketAcl.
func aclOf(in *s3api.PutBucketAclInput) aclRequest {
	return aclRequest{canned: in.ACL, headers: grantHeaders(in.GrantFullControl, in.GrantRead, in.GrantReadACP, in.GrantWrite, in.GrantWriteACP)}
}

// putACL replaces the access control list, which a bucket whose ownership
// is BucketOwnerEnforced refuses (s3.md 4.2.4).
func putACL(in *s3api.PutBucketAclInput, b *Bucket) *awserr.Error {
	if aclsDisabled(b) {
		return errACLsDisabled()
	}
	if in.AccessControlPolicy != nil {
		b.Grants = in.AccessControlPolicy.Grants
		return nil
	}
	grants, err := aclOf(in).grants(b.Home.OwnerAccount)
	if err != nil {
		return err
	}
	b.Grants = grants
	return nil
}

// aclsDisabled reports a bucket whose ownership controls disable ACLs.
func aclsDisabled(b *Bucket) bool {
	return b.Ownership != nil && slices.ContainsFunc(b.Ownership.Rules, func(r s3api.OwnershipControlsRule) bool {
		return r.ObjectOwnership == s3api.ObjectOwnershipBucketOwnerEnforced
	})
}

// corsMethods are the methods a CORS rule may allow.
func corsMethods() []string { return []string{"GET", "PUT", "HEAD", "POST", "DELETE"} }

func (s *Service) bindCORS(b s3api.Binder) {
	get := func(in *s3api.GetBucketCorsInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketCors(reader(s, get, func(_ *s3api.GetBucketCorsInput, b *Bucket) (*s3api.GetBucketCorsOutput, *awserr.Error) {
		if b.CORS == nil {
			return nil, notConfigured(s3api.NotConfiguredGetBucketCors(noSuchCORSMessage), b)
		}
		return &s3api.GetBucketCorsOutput{CORSRules: b.CORS.CORSRules}, nil
	}), stored("the CORS configuration is"), touches(get))
	put := func(in *s3api.PutBucketCorsInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketCors(writer[s3api.PutBucketCorsInput, s3api.PutBucketCorsOutput](s, put, func(in *s3api.PutBucketCorsInput, b *Bucket) *awserr.Error {
		b.CORS = &in.CORSConfiguration
		return nil
	}), stored("the CORS configuration is"), touches(put),
		kernel.WithCheck(func(in *s3api.PutBucketCorsInput) *awserr.Error {
			for _, r := range in.CORSConfiguration.CORSRules {
				for _, m := range r.AllowedMethods {
					if !slices.Contains(corsMethods(), m) {
						return errInvalidRequest("Found unsupported HTTP method in CORS config. Unsupported method is " + m)
					}
				}
			}
			return nil
		}))
	del := func(in *s3api.DeleteBucketCorsInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeleteBucketCors(writer[s3api.DeleteBucketCorsInput, s3api.DeleteBucketCorsOutput](s, del, func(_ *s3api.DeleteBucketCorsInput, b *Bucket) *awserr.Error {
		b.CORS = nil
		return nil
	}), stored("the CORS configuration is"), touches(del))
}

func (s *Service) bindWebsite(b s3api.Binder) {
	get := func(in *s3api.GetBucketWebsiteInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketWebsite(reader(s, get, func(_ *s3api.GetBucketWebsiteInput, b *Bucket) (*s3api.GetBucketWebsiteOutput, *awserr.Error) {
		w := b.Website
		if w == nil {
			return nil, notConfigured(s3api.NotConfiguredGetBucketWebsite(noSuchWebsiteMessage), b)
		}
		return &s3api.GetBucketWebsiteOutput{
			RedirectAllRequestsTo: w.RedirectAllRequestsTo, IndexDocument: w.IndexDocument,
			ErrorDocument: w.ErrorDocument, RoutingRules: w.RoutingRules,
		}, nil
	}), stored("the website configuration is"), touches(get))
	put := func(in *s3api.PutBucketWebsiteInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketWebsite(writer[s3api.PutBucketWebsiteInput, s3api.PutBucketWebsiteOutput](s, put, func(in *s3api.PutBucketWebsiteInput, b *Bucket) *awserr.Error {
		b.Website = &in.WebsiteConfiguration
		return nil
	}), stored("the website configuration is"), touches(put))
	del := func(in *s3api.DeleteBucketWebsiteInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeleteBucketWebsite(writer[s3api.DeleteBucketWebsiteInput, s3api.DeleteBucketWebsiteOutput](s, del, func(_ *s3api.DeleteBucketWebsiteInput, b *Bucket) *awserr.Error {
		b.Website = nil
		return nil
	}), stored("the website configuration is"), touches(del))
}

// The MalformedPolicy texts (s3.md 4.2.5).
const (
	policyNotJSON      = "Policies must be valid JSON and the first byte must be '{'"
	policyNoStatements = "Missing required field Statement"
)

func (s *Service) bindPolicy(b s3api.Binder) {
	get := func(in *s3api.GetBucketPolicyInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketPolicy(reader(s, get, func(_ *s3api.GetBucketPolicyInput, b *Bucket) (*s3api.GetBucketPolicyOutput, *awserr.Error) {
		if b.Policy == "" {
			return nil, notConfigured(s3api.NotConfiguredGetBucketPolicy(noSuchBucketPolicyMessage), b)
		}
		return &s3api.GetBucketPolicyOutput{Policy: new(b.Policy)}, nil
	}), stored("the policy is"), touches(get))
	put := func(in *s3api.PutBucketPolicyInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketPolicy(writer[s3api.PutBucketPolicyInput, s3api.PutBucketPolicyOutput](s, put, func(in *s3api.PutBucketPolicyInput, b *Bucket) *awserr.Error {
		b.Policy = in.Policy
		return nil
	}), stored("the policy is"), touches(put),
		kernel.WithCheck(func(in *s3api.PutBucketPolicyInput) *awserr.Error { return checkPolicy(in.Policy) }))
	del := func(in *s3api.DeleteBucketPolicyInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeleteBucketPolicy(writer[s3api.DeleteBucketPolicyInput, s3api.DeleteBucketPolicyOutput](s, del, func(_ *s3api.DeleteBucketPolicyInput, b *Bucket) *awserr.Error {
		b.Policy = ""
		return nil
	}), stored("the policy is"), touches(del))
}

// checkPolicy checks a bucket policy's form only: a JSON object with a
// Statement. Phase 1 stores policies and evaluates none.
func checkPolicy(doc string) *awserr.Error {
	trimmed := bytes.TrimSpace([]byte(doc))
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return errMalformedPolicy(policyNotJSON)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return errMalformedPolicy(policyNotJSON)
	}
	if _, ok := fields["Statement"]; !ok {
		return errMalformedPolicy(policyNoStatements)
	}
	return nil
}

func (s *Service) bindLogging(b s3api.Binder) {
	get := func(in *s3api.GetBucketLoggingInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketLogging(reader(s, get, func(_ *s3api.GetBucketLoggingInput, b *Bucket) (*s3api.GetBucketLoggingOutput, *awserr.Error) {
		if b.Logging == nil {
			return s3api.NotConfiguredGetBucketLogging(), nil
		}
		return &s3api.GetBucketLoggingOutput{LoggingEnabled: b.Logging}, nil
	}), stored("the logging configuration is"), touches(get))
	put := func(in *s3api.PutBucketLoggingInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketLogging(writer[s3api.PutBucketLoggingInput, s3api.PutBucketLoggingOutput](s, put, func(in *s3api.PutBucketLoggingInput, b *Bucket) *awserr.Error {
		b.Logging = in.BucketLoggingStatus.LoggingEnabled
		return nil
	}), stored("the logging configuration is"), touches(put))
}

func (s *Service) bindLifecycle(b s3api.Binder) {
	get := func(in *s3api.GetBucketLifecycleConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.GetBucketLifecycleConfiguration(reader(s, get, func(_ *s3api.GetBucketLifecycleConfigurationInput, b *Bucket) (*s3api.GetBucketLifecycleConfigurationOutput, *awserr.Error) {
		l := b.Lifecycle
		if l == nil {
			return nil, notConfigured(s3api.NotConfiguredGetBucketLifecycleConfiguration(noSuchLifecycleMessage), b)
		}
		return &s3api.GetBucketLifecycleConfigurationOutput{Rules: l.Rules, TransitionDefaultMinimumObjectSize: new(l.TransitionDefaultMinimumObjectSize)}, nil
	}), stored("the lifecycle rules are"), touches(get))
	put := func(in *s3api.PutBucketLifecycleConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.PutBucketLifecycleConfiguration(writer[s3api.PutBucketLifecycleConfigurationInput, s3api.PutBucketLifecycleConfigurationOutput](s, put, func(in *s3api.PutBucketLifecycleConfigurationInput, b *Bucket) *awserr.Error {
		b.Lifecycle = &Lifecycle{Rules: in.LifecycleConfiguration.Rules, TransitionDefaultMinimumObjectSize: transitionDefault(in)}
		return nil
	}), stored("the lifecycle rules are"), touches(put), kernel.WithCheck(checkLifecycle))
	del := func(in *s3api.DeleteBucketLifecycleInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeleteBucketLifecycle(writer[s3api.DeleteBucketLifecycleInput, s3api.DeleteBucketLifecycleOutput](s, del, func(_ *s3api.DeleteBucketLifecycleInput, b *Bucket) *awserr.Error {
		b.Lifecycle = nil
		return nil
	}), stored("the lifecycle rules are"), touches(del))
}

// transitionDefault is the x-amz-transition-default-minimum-object-size a
// lifecycle put carried, or the default all_storage_classes_128K.
func transitionDefault(in *s3api.PutBucketLifecycleConfigurationInput) s3api.TransitionDefaultMinimumObjectSize {
	if in.TransitionDefaultMinimumObjectSize == nil {
		return s3api.TransitionDefaultMinimumObjectSizeAllStorageClasses128k
	}
	return *in.TransitionDefaultMinimumObjectSize
}

// maxLifecycleRules is the most rules one lifecycle configuration holds
// (DOC lifecycle-configuration-examples, "up to 1,000 rules").
const maxLifecycleRules = 1000

// The lifecycle rule-set texts, which no AWS recording shows: S3-compatible
// servers written against AWS use them (minio errLifecycleDuplicateID and
// errLifecycleTooManyRules), so they are UNVERIFIED (lifecycleRuleLimits).
const (
	lifecycleDuplicateIDMessage  = "Rule ID must be unique. Found same ID for more than one rule"
	lifecycleTooManyRulesMessage = "Lifecycle configuration allows a maximum of 1000 rules"
)

// checkLifecycle checks a lifecycle configuration with the errors recorded
// against AWS (s3.md 4.2.8), and the documented limits of its rule set:
// at most 1,000 rules, each with its own ID, whose actions keep the day
// limits of checkLifecycleDays.
func checkLifecycle(in *s3api.PutBucketLifecycleConfigurationInput) *awserr.Error {
	if v := in.TransitionDefaultMinimumObjectSize; v != nil && !v.Known() {
		return errInvalidRequest("Invalid TransitionDefaultMinimumObjectSize found: " + string(*v))
	}
	if in.LifecycleConfiguration == nil || len(in.LifecycleConfiguration.Rules) == 0 {
		return errMalformedXML()
	}
	rules := in.LifecycleConfiguration.Rules
	if len(rules) > maxLifecycleRules {
		return s3api.ErrInvalidArgument(lifecycleTooManyRulesMessage)
	}
	seen := make(map[string]bool, len(rules))
	for _, r := range rules {
		if err := checkLifecycleRule(r); err != nil {
			return err
		}
		if seen[*r.ID] {
			return errInvalidArgument(lifecycleDuplicateIDMessage, "ID", *r.ID)
		}
		seen[*r.ID] = true
	}
	return nil
}

func checkLifecycleRule(r s3api.LifecycleRule) *awserr.Error {
	switch {
	case r.ID == nil || *r.ID == "",
		r.Filter == nil && r.Prefix == nil,
		r.NoncurrentVersionExpiration != nil && r.NoncurrentVersionExpiration.NoncurrentDays == nil && r.NoncurrentVersionExpiration.NewerNoncurrentVersions == nil:
		return errMalformedXML()
	}
	if e := r.Expiration; e != nil {
		if e.ExpiredObjectDeleteMarker != nil && *e.ExpiredObjectDeleteMarker && e.Days != nil {
			return errMalformedXML()
		}
		if e.Date != nil && !atMidnight(*e.Date) {
			return errInvalidArgument("'Date' must be at midnight GMT", "Date", e.Date.UTC().Format(time.RFC3339))
		}
	}
	if err := checkLifecycleDays(r); err != nil {
		return err
	}
	if f := r.Filter; f != nil {
		return checkLifecycleFilter(f)
	}
	return nil
}

// checkLifecycleDays checks the days of a rule's actions against the
// limits below.
func checkLifecycleDays(r s3api.LifecycleRule) *awserr.Error {
	if e := r.Expiration; e != nil && e.Days != nil && *e.Days < 1 {
		return errInvalidArgument(lifecycleExpirationDaysMessage, "Days", strconv.Itoa(int(*e.Days)))
	}
	if e := r.NoncurrentVersionExpiration; e != nil && e.NoncurrentDays != nil && *e.NoncurrentDays < 1 {
		return errInvalidArgument(lifecycleNoncurrentExpirationDaysMessage, "NoncurrentDays", strconv.Itoa(int(*e.NoncurrentDays)))
	}
	current := make([]transitionStep, 0, len(r.Transitions))
	for _, t := range r.Transitions {
		if t.StorageClass != nil && t.Date == nil {
			current = append(current, transitionStep{days: deref(t.Days), class: *t.StorageClass})
		}
	}
	if err := checkTransitionDays(transitionAction{name: "Transition", days: "Days"}, current); err != nil {
		return err
	}
	noncurrent := make([]transitionStep, 0, len(r.NoncurrentVersionTransitions))
	for _, t := range r.NoncurrentVersionTransitions {
		if t.StorageClass != nil {
			noncurrent = append(noncurrent, transitionStep{days: deref(t.NoncurrentDays), class: *t.StorageClass})
		}
	}
	return checkTransitionDays(transitionAction{name: "NoncurrentVersionTransition", days: "NoncurrentDays"}, noncurrent)
}

// The lifecycle day limits. AWS documents them: Expiration Days is "a
// non-zero positive integer" (API_LifecycleExpiration), and "you can't
// create a single Lifecycle rule that transitions objects from one storage
// class to another before the minimum storage duration period has passed"
// (lifecycle-transition-general-considerations). Transition Days may be 0
// (API_Transition), save to the Infrequent Access classes, which take 30
// days at least. The IA text is an AWS answer quoted in a Terraform
// write-up (zenn.dev/thaim 2024-04-s3-lifecycle-configuration-min-duration)
// and the storage-duration text is composed, so both are UNVERIFIED
// (lifecycleRuleLimits). The expiration texts, with the ArgumentName and
// ArgumentValue S3 adds, are recorded
// (testdata/parity/s3/R12/12-lifecycle-expiration-0.json,
// testdata/parity/s3/R12/12-lifecycle-noncurrent-expiration-0.json).
const (
	lifecycleExpirationDaysMessage           = "'Days' for Expiration action must be a positive integer"
	lifecycleNoncurrentExpirationDaysMessage = "'NoncurrentDays' for NoncurrentVersionExpiration action must be a positive integer"
	infrequentAccessMinDays                  = 30
)

// transitionStep is one Days-based transition of a rule: the days after
// creation (or after becoming noncurrent) and the class it moves to. A
// transition with neither Days nor Date is a 0-day one, as AWS reads it.
type transitionStep struct {
	days  int32
	class s3api.TransitionStorageClass
}

// transitionAction names a transition action and its days member in a
// refusal: Transition/Days or NoncurrentVersionTransition/NoncurrentDays.
type transitionAction struct {
	name string
	days string
}

// checkTransitionDays refuses a transition to an Infrequent Access class
// before 30 days, and a transition sooner after the one before it than
// that one's class must hold an object.
func checkTransitionDays(a transitionAction, steps []transitionStep) *awserr.Error {
	for _, s := range steps {
		if (s.class == s3api.TransitionStorageClassStandardIa || s.class == s3api.TransitionStorageClassOnezoneIa) && s.days < infrequentAccessMinDays {
			return s3api.ErrInvalidArgument(fmt.Sprintf("'%s' in %s action must be greater than or equal to %d for storageClass '%s'", a.days, a.name, infrequentAccessMinDays, s.class))
		}
	}
	slices.SortStableFunc(steps, func(x, y transitionStep) int { return cmp.Compare(x.days, y.days) })
	for i := 1; i < len(steps); i++ {
		prev, next := steps[i-1], steps[i]
		if hold := minStorageDays(prev.class); next.days-prev.days < hold {
			return s3api.ErrInvalidArgument(fmt.Sprintf("'%s' in the '%s' action for StorageClass '%s' must be at least %d days more than '%s' in the '%s' action for StorageClass '%s'",
				a.days, a.name, next.class, hold, a.days, a.name, prev.class))
		}
	}
	return nil
}

// minStorageDays is the minimum storage duration of class c (DOC
// storage-class-intro, "Comparing the Amazon S3 storage classes"):
// Intelligent-Tiering has none.
func minStorageDays(c s3api.TransitionStorageClass) int32 {
	switch c {
	case s3api.TransitionStorageClassStandardIa, s3api.TransitionStorageClassOnezoneIa:
		return infrequentAccessMinDays
	case s3api.TransitionStorageClassGlacierIr, s3api.TransitionStorageClassGlacier:
		return 90
	case s3api.TransitionStorageClassDeepArchive:
		return 180
	case s3api.TransitionStorageClassIntelligentTiering:
	}
	return 0
}

// checkLifecycleFilter refuses a filter that sets more than one of Prefix,
// Tag, the object size bounds and And: combinations go inside And.
func checkLifecycleFilter(f *s3api.LifecycleRuleFilter) *awserr.Error {
	set := 0
	for _, on := range []bool{f.Prefix != nil, f.Tag != nil, f.ObjectSizeGreaterThan != nil, f.ObjectSizeLessThan != nil, f.And != nil} {
		if on {
			set++
		}
	}
	if set > 1 {
		return errMalformedXML()
	}
	if f.And != nil {
		seen := map[string]bool{}
		for _, t := range f.And.Tags {
			if seen[t.Key] {
				return errInvalidRequest("Duplicate Tag Keys are not allowed.")
			}
			seen[t.Key] = true
		}
	}
	return nil
}

func atMidnight(t time.Time) bool {
	t = t.UTC()
	return t.Equal(t.Truncate(24 * time.Hour))
}

func (s *Service) bindReplication(b s3api.Binder) {
	get := func(in *s3api.GetBucketReplicationInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketReplication(reader(s, get, func(_ *s3api.GetBucketReplicationInput, b *Bucket) (*s3api.GetBucketReplicationOutput, *awserr.Error) {
		if b.Replication == nil {
			return nil, notConfigured(s3api.NotConfiguredGetBucketReplication(noSuchReplicationMessage), b)
		}
		return &s3api.GetBucketReplicationOutput{ReplicationConfiguration: b.Replication}, nil
	}), stored("the replication configuration is"), touches(get))
	put := func(in *s3api.PutBucketReplicationInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketReplication(writer[s3api.PutBucketReplicationInput, s3api.PutBucketReplicationOutput](s, put, func(in *s3api.PutBucketReplicationInput, b *Bucket) *awserr.Error {
		if b.versioning() != versionsEnabled {
			return errReplicationNeedsVersioning()
		}
		b.Replication = &in.ReplicationConfiguration
		return nil
	}), stored("the replication configuration is"), touches(put))
	del := func(in *s3api.DeleteBucketReplicationInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeleteBucketReplication(writer[s3api.DeleteBucketReplicationInput, s3api.DeleteBucketReplicationOutput](s, del, func(_ *s3api.DeleteBucketReplicationInput, b *Bucket) *awserr.Error {
		b.Replication = nil
		return nil
	}), stored("the replication configuration is"), touches(del))
}

func (s *Service) bindEncryption(b s3api.Binder) {
	get := func(in *s3api.GetBucketEncryptionInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketEncryption(reader(s, get, func(_ *s3api.GetBucketEncryptionInput, b *Bucket) (*s3api.GetBucketEncryptionOutput, *awserr.Error) {
		if b.Encryption == nil {
			return s3api.NotConfiguredGetBucketEncryption(), nil
		}
		return &s3api.GetBucketEncryptionOutput{ServerSideEncryptionConfiguration: b.Encryption}, nil
	}), stored("the default encryption rule is"), touches(get))
	put := func(in *s3api.PutBucketEncryptionInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketEncryption(writer[s3api.PutBucketEncryptionInput, s3api.PutBucketEncryptionOutput](s, put, func(in *s3api.PutBucketEncryptionInput, b *Bucket) *awserr.Error {
		b.Encryption = &in.ServerSideEncryptionConfiguration
		return nil
	}), stored("the default encryption rule is"), touches(put), kernel.WithCheck(checkEncryption))
	del := func(in *s3api.DeleteBucketEncryptionInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeleteBucketEncryption(writer[s3api.DeleteBucketEncryptionInput, s3api.DeleteBucketEncryptionOutput](s, del, func(_ *s3api.DeleteBucketEncryptionInput, b *Bucket) *awserr.Error {
		b.Encryption = nil
		return nil
	}), stored("the default encryption rule is"), touches(del))
}

// checkEncryption checks a default encryption configuration: exactly one
// rule with a default, and a KMS key only for the KMS algorithms (s3.md
// 4.2.2).
func checkEncryption(in *s3api.PutBucketEncryptionInput) *awserr.Error {
	rules := in.ServerSideEncryptionConfiguration.Rules
	if len(rules) != 1 || rules[0].ApplyServerSideEncryptionByDefault == nil {
		return errMalformedXML()
	}
	d := rules[0].ApplyServerSideEncryptionByDefault
	kms := d.SSEAlgorithm == s3api.ServerSideEncryptionAwsKms || d.SSEAlgorithm == s3api.ServerSideEncryptionAwsKmsDsse
	if d.KMSMasterKeyID != nil && !kms {
		return withField(s3api.ErrInvalidArgument("a KMSMasterKeyID is not applicable if the default sse algorithm is not aws:kms or aws:kms:dsse"),
			"ArgumentName", "ApplyServerSideEncryptionByDefault")
	}
	return nil
}

func (s *Service) bindObjectLock(b s3api.Binder) {
	get := func(in *s3api.GetObjectLockConfigurationInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetObjectLockConfiguration(reader(s, get, func(_ *s3api.GetObjectLockConfigurationInput, b *Bucket) (*s3api.GetObjectLockConfigurationOutput, *awserr.Error) {
		if b.ObjectLock == nil {
			return nil, notConfigured(s3api.NotConfiguredGetObjectLockConfiguration(noSuchObjectLockMessage), b)
		}
		return &s3api.GetObjectLockConfigurationOutput{ObjectLockConfiguration: b.ObjectLock}, nil
	}), stored("the Object Lock configuration is"), touches(get))
	put := func(in *s3api.PutObjectLockConfigurationInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutObjectLockConfiguration(writer[s3api.PutObjectLockConfigurationInput, s3api.PutObjectLockConfigurationOutput](s, put, func(in *s3api.PutObjectLockConfigurationInput, b *Bucket) *awserr.Error {
		if b.versioning() != versionsEnabled {
			return errObjectLockNeedsVersioning()
		}
		b.ObjectLock = in.ObjectLockConfiguration
		return nil
	}), stored("the Object Lock configuration is"), touches(put), kernel.WithCheck(checkObjectLock))
}

// checkObjectLock checks an Object Lock configuration (s3.md 4.2.10): it
// is enabled, and a default retention names a mode and exactly one of
// Days and Years.
func checkObjectLock(in *s3api.PutObjectLockConfigurationInput) *awserr.Error {
	cfg := in.ObjectLockConfiguration
	if cfg == nil || cfg.ObjectLockEnabled == nil || *cfg.ObjectLockEnabled != s3api.ObjectLockEnabledEnabled {
		return errMalformedXML()
	}
	if cfg.Rule == nil {
		return nil
	}
	d := cfg.Rule.DefaultRetention
	if d == nil || d.Mode == nil || (d.Days == nil) == (d.Years == nil) {
		return errMalformedXML()
	}
	return nil
}

func (s *Service) bindRequestPayment(b s3api.Binder) {
	get := func(in *s3api.GetBucketRequestPaymentInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketRequestPayment(reader(s, get, func(_ *s3api.GetBucketRequestPaymentInput, b *Bucket) (*s3api.GetBucketRequestPaymentOutput, *awserr.Error) {
		if b.RequestPayment == nil {
			return s3api.NotConfiguredGetBucketRequestPayment(), nil
		}
		return &s3api.GetBucketRequestPaymentOutput{Payer: new(b.RequestPayment.Payer)}, nil
	}), stored("the payer is"), touches(get))
	put := func(in *s3api.PutBucketRequestPaymentInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketRequestPayment(writer[s3api.PutBucketRequestPaymentInput, s3api.PutBucketRequestPaymentOutput](s, put, func(in *s3api.PutBucketRequestPaymentInput, b *Bucket) *awserr.Error {
		b.RequestPayment = &in.RequestPaymentConfiguration
		return nil
	}), stored("the payer is"), touches(put))
}

func (s *Service) bindAccelerate(b s3api.Binder) {
	get := func(in *s3api.GetBucketAccelerateConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.GetBucketAccelerateConfiguration(reader(s, get, func(_ *s3api.GetBucketAccelerateConfigurationInput, b *Bucket) (*s3api.GetBucketAccelerateConfigurationOutput, *awserr.Error) {
		if b.Accelerate == nil {
			return s3api.NotConfiguredGetBucketAccelerateConfiguration(), nil
		}
		return &s3api.GetBucketAccelerateConfigurationOutput{Status: b.Accelerate.Status}, nil
	}), stored("the transfer acceleration status is"), touches(get))
	put := func(in *s3api.PutBucketAccelerateConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.PutBucketAccelerateConfiguration(writer[s3api.PutBucketAccelerateConfigurationInput, s3api.PutBucketAccelerateConfigurationOutput](s, put, func(in *s3api.PutBucketAccelerateConfigurationInput, b *Bucket) *awserr.Error {
		b.Accelerate = &in.AccelerateConfiguration
		return nil
	}), stored("the transfer acceleration status is"), touches(put))
}

func (s *Service) bindPublicAccessBlock(b s3api.Binder) {
	get := func(in *s3api.GetPublicAccessBlockInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetPublicAccessBlock(reader(s, get, func(_ *s3api.GetPublicAccessBlockInput, b *Bucket) (*s3api.GetPublicAccessBlockOutput, *awserr.Error) {
		if b.PublicAccessBlock == nil {
			return nil, notConfigured(s3api.NotConfiguredGetPublicAccessBlock(noSuchPublicAccessMessage), b)
		}
		return &s3api.GetPublicAccessBlockOutput{PublicAccessBlockConfiguration: b.PublicAccessBlock}, nil
	}), stored("the public access block is"), touches(get))
	put := func(in *s3api.PutPublicAccessBlockInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutPublicAccessBlock(writer[s3api.PutPublicAccessBlockInput, s3api.PutPublicAccessBlockOutput](s, put, func(in *s3api.PutPublicAccessBlockInput, b *Bucket) *awserr.Error {
		b.PublicAccessBlock = publicAccessBlock(in.PublicAccessBlockConfiguration)
		return nil
	}), stored("the public access block is"), touches(put),
		kernel.WithCheck(func(in *s3api.PutPublicAccessBlockInput) *awserr.Error {
			if c := in.PublicAccessBlockConfiguration; c.BlockPublicAcls == nil && c.IgnorePublicAcls == nil && c.BlockPublicPolicy == nil && c.RestrictPublicBuckets == nil {
				return errInvalidRequest("Must specify at least one configuration.")
			}
			return nil
		}))
	del := func(in *s3api.DeletePublicAccessBlockInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeletePublicAccessBlock(writer[s3api.DeletePublicAccessBlockInput, s3api.DeletePublicAccessBlockOutput](s, del, func(_ *s3api.DeletePublicAccessBlockInput, b *Bucket) *awserr.Error {
		b.PublicAccessBlock = nil
		return nil
	}), stored("the public access block is"), touches(del))
}

// publicAccessBlock is the stored form of a put: every flag present, an
// absent one false, as Terraform reads all four back (tf-storage.md 2.6).
func publicAccessBlock(c s3api.PublicAccessBlockConfiguration) *s3api.PublicAccessBlockConfiguration {
	flag := func(v *bool) *bool { return new(v != nil && *v) }
	return &s3api.PublicAccessBlockConfiguration{
		BlockPublicAcls: flag(c.BlockPublicAcls), IgnorePublicAcls: flag(c.IgnorePublicAcls),
		BlockPublicPolicy: flag(c.BlockPublicPolicy), RestrictPublicBuckets: flag(c.RestrictPublicBuckets),
	}
}

func (s *Service) bindOwnership(b s3api.Binder) {
	get := func(in *s3api.GetBucketOwnershipControlsInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketOwnershipControls(reader(s, get, func(_ *s3api.GetBucketOwnershipControlsInput, b *Bucket) (*s3api.GetBucketOwnershipControlsOutput, *awserr.Error) {
		if b.Ownership == nil {
			return nil, notConfigured(s3api.NotConfiguredGetBucketOwnershipControls(noSuchOwnershipMessage), b)
		}
		return &s3api.GetBucketOwnershipControlsOutput{OwnershipControls: b.Ownership}, nil
	}), registry.Partial{Note: "BucketOwnerEnforced refuses ACL writes; object ownership is not applied to objects in this build"}, touches(get))
	put := func(in *s3api.PutBucketOwnershipControlsInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutBucketOwnershipControls(writer[s3api.PutBucketOwnershipControlsInput, s3api.PutBucketOwnershipControlsOutput](s, put, func(in *s3api.PutBucketOwnershipControlsInput, b *Bucket) *awserr.Error {
		b.Ownership = &in.OwnershipControls
		return nil
	}), registry.Partial{Note: "BucketOwnerEnforced refuses ACL writes; object ownership is not applied to objects in this build"}, touches(put),
		kernel.WithCheck(func(in *s3api.PutBucketOwnershipControlsInput) *awserr.Error {
			rules := in.OwnershipControls.Rules
			if len(rules) != 1 || !rules[0].ObjectOwnership.Known() {
				return errMalformedXML()
			}
			return nil
		}))
	del := func(in *s3api.DeleteBucketOwnershipControlsInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.DeleteBucketOwnershipControls(writer[s3api.DeleteBucketOwnershipControlsInput, s3api.DeleteBucketOwnershipControlsOutput](s, del, func(_ *s3api.DeleteBucketOwnershipControlsInput, b *Bucket) *awserr.Error {
		b.Ownership = nil
		return nil
	}), registry.Partial{Note: "BucketOwnerEnforced refuses ACL writes; object ownership is not applied to objects in this build"}, touches(del))
}

// family is one of the configuration families a bucket keys by Id:
// metrics, analytics, inventory and intelligent tiering. Get and Delete of
// an id the bucket does not hold answer NoSuchConfiguration (s3.md 4.1).
type family[C any] struct {
	of  func(b *Bucket) map[string]C
	set func(b *Bucket, m map[string]C)
}

func (f family[C]) get(b *Bucket, id string) (*C, *awserr.Error) {
	c, ok := f.of(b)[id]
	if !ok {
		return nil, errNoSuchConfiguration()
	}
	return &c, nil
}

func (f family[C]) put(b *Bucket, id string, c C) { f.set(b, with(f.of(b), id, c)) }

func (f family[C]) remove(b *Bucket, id string) *awserr.Error {
	if _, ok := f.of(b)[id]; !ok {
		return errNoSuchConfiguration()
	}
	f.set(b, without(f.of(b), id))
	return nil
}

// list is every configuration in Id order, on one page.
func (f family[C]) list(b *Bucket) []C {
	m := f.of(b)
	out := make([]C, 0, len(m))
	for _, id := range slices.Sorted(maps.Keys(m)) {
		out = append(out, m[id])
	}
	return out
}

func (s *Service) bindMetrics(b s3api.Binder) {
	f := family[s3api.MetricsConfiguration]{
		of:  func(b *Bucket) map[string]s3api.MetricsConfiguration { return b.Metrics },
		set: func(b *Bucket, m map[string]s3api.MetricsConfiguration) { b.Metrics = m },
	}
	cov := stored("the metrics configuration is")
	get := func(in *s3api.GetBucketMetricsConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.GetBucketMetricsConfiguration(reader(s, get, func(in *s3api.GetBucketMetricsConfigurationInput, b *Bucket) (*s3api.GetBucketMetricsConfigurationOutput, *awserr.Error) {
		c, err := f.get(b, in.Id)
		if err != nil {
			return nil, err
		}
		return &s3api.GetBucketMetricsConfigurationOutput{MetricsConfiguration: c}, nil
	}), cov, touches(get))
	put := func(in *s3api.PutBucketMetricsConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.PutBucketMetricsConfiguration(writer[s3api.PutBucketMetricsConfigurationInput, s3api.PutBucketMetricsConfigurationOutput](s, put, func(in *s3api.PutBucketMetricsConfigurationInput, b *Bucket) *awserr.Error {
		f.put(b, in.Id, in.MetricsConfiguration)
		return nil
	}), cov, touches(put))
	del := func(in *s3api.DeleteBucketMetricsConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.DeleteBucketMetricsConfiguration(writer[s3api.DeleteBucketMetricsConfigurationInput, s3api.DeleteBucketMetricsConfigurationOutput](s, del, func(in *s3api.DeleteBucketMetricsConfigurationInput, b *Bucket) *awserr.Error {
		return f.remove(b, in.Id)
	}), cov, touches(del))
	list := func(in *s3api.ListBucketMetricsConfigurationsInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.ListBucketMetricsConfigurations(reader(s, list, func(in *s3api.ListBucketMetricsConfigurationsInput, b *Bucket) (*s3api.ListBucketMetricsConfigurationsOutput, *awserr.Error) {
		return &s3api.ListBucketMetricsConfigurationsOutput{IsTruncated: new(false), ContinuationToken: in.ContinuationToken, MetricsConfigurationList: f.list(b)}, nil
	}), cov, touches(list))
}

func (s *Service) bindAnalytics(b s3api.Binder) {
	f := family[s3api.AnalyticsConfiguration]{
		of:  func(b *Bucket) map[string]s3api.AnalyticsConfiguration { return b.Analytics },
		set: func(b *Bucket, m map[string]s3api.AnalyticsConfiguration) { b.Analytics = m },
	}
	cov := stored("the analytics configuration is")
	get := func(in *s3api.GetBucketAnalyticsConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.GetBucketAnalyticsConfiguration(reader(s, get, func(in *s3api.GetBucketAnalyticsConfigurationInput, b *Bucket) (*s3api.GetBucketAnalyticsConfigurationOutput, *awserr.Error) {
		c, err := f.get(b, in.Id)
		if err != nil {
			return nil, err
		}
		return &s3api.GetBucketAnalyticsConfigurationOutput{AnalyticsConfiguration: c}, nil
	}), cov, touches(get))
	put := func(in *s3api.PutBucketAnalyticsConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.PutBucketAnalyticsConfiguration(writer[s3api.PutBucketAnalyticsConfigurationInput, s3api.PutBucketAnalyticsConfigurationOutput](s, put, func(in *s3api.PutBucketAnalyticsConfigurationInput, b *Bucket) *awserr.Error {
		f.put(b, in.Id, in.AnalyticsConfiguration)
		return nil
	}), cov, touches(put))
	del := func(in *s3api.DeleteBucketAnalyticsConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.DeleteBucketAnalyticsConfiguration(writer[s3api.DeleteBucketAnalyticsConfigurationInput, s3api.DeleteBucketAnalyticsConfigurationOutput](s, del, func(in *s3api.DeleteBucketAnalyticsConfigurationInput, b *Bucket) *awserr.Error {
		return f.remove(b, in.Id)
	}), cov, touches(del))
	list := func(in *s3api.ListBucketAnalyticsConfigurationsInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.ListBucketAnalyticsConfigurations(reader(s, list, func(in *s3api.ListBucketAnalyticsConfigurationsInput, b *Bucket) (*s3api.ListBucketAnalyticsConfigurationsOutput, *awserr.Error) {
		return &s3api.ListBucketAnalyticsConfigurationsOutput{IsTruncated: new(false), ContinuationToken: in.ContinuationToken, AnalyticsConfigurationList: f.list(b)}, nil
	}), cov, touches(list))
}

func (s *Service) bindInventory(b s3api.Binder) {
	f := family[s3api.InventoryConfiguration]{
		of:  func(b *Bucket) map[string]s3api.InventoryConfiguration { return b.Inventory },
		set: func(b *Bucket, m map[string]s3api.InventoryConfiguration) { b.Inventory = m },
	}
	cov := stored("the inventory configuration is")
	get := func(in *s3api.GetBucketInventoryConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.GetBucketInventoryConfiguration(reader(s, get, func(in *s3api.GetBucketInventoryConfigurationInput, b *Bucket) (*s3api.GetBucketInventoryConfigurationOutput, *awserr.Error) {
		c, err := f.get(b, in.Id)
		if err != nil {
			return nil, err
		}
		return &s3api.GetBucketInventoryConfigurationOutput{InventoryConfiguration: c}, nil
	}), cov, touches(get))
	put := func(in *s3api.PutBucketInventoryConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.PutBucketInventoryConfiguration(writer[s3api.PutBucketInventoryConfigurationInput, s3api.PutBucketInventoryConfigurationOutput](s, put, func(in *s3api.PutBucketInventoryConfigurationInput, b *Bucket) *awserr.Error {
		f.put(b, in.Id, in.InventoryConfiguration)
		return nil
	}), cov, touches(put))
	del := func(in *s3api.DeleteBucketInventoryConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.DeleteBucketInventoryConfiguration(writer[s3api.DeleteBucketInventoryConfigurationInput, s3api.DeleteBucketInventoryConfigurationOutput](s, del, func(in *s3api.DeleteBucketInventoryConfigurationInput, b *Bucket) *awserr.Error {
		return f.remove(b, in.Id)
	}), cov, touches(del))
	list := func(in *s3api.ListBucketInventoryConfigurationsInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.ListBucketInventoryConfigurations(reader(s, list, func(in *s3api.ListBucketInventoryConfigurationsInput, b *Bucket) (*s3api.ListBucketInventoryConfigurationsOutput, *awserr.Error) {
		return &s3api.ListBucketInventoryConfigurationsOutput{IsTruncated: new(false), ContinuationToken: in.ContinuationToken, InventoryConfigurationList: f.list(b)}, nil
	}), cov, touches(list))
}

func (s *Service) bindIntelligentTiering(b s3api.Binder) {
	f := family[s3api.IntelligentTieringConfiguration]{
		of:  func(b *Bucket) map[string]s3api.IntelligentTieringConfiguration { return b.IntelligentTiering },
		set: func(b *Bucket, m map[string]s3api.IntelligentTieringConfiguration) { b.IntelligentTiering = m },
	}
	cov := stored("the Intelligent-Tiering configuration is")
	get := func(in *s3api.GetBucketIntelligentTieringConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.GetBucketIntelligentTieringConfiguration(reader(s, get, func(in *s3api.GetBucketIntelligentTieringConfigurationInput, b *Bucket) (*s3api.GetBucketIntelligentTieringConfigurationOutput, *awserr.Error) {
		c, err := f.get(b, in.Id)
		if err != nil {
			return nil, err
		}
		return &s3api.GetBucketIntelligentTieringConfigurationOutput{IntelligentTieringConfiguration: c}, nil
	}), cov, touches(get))
	put := func(in *s3api.PutBucketIntelligentTieringConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.PutBucketIntelligentTieringConfiguration(writer[s3api.PutBucketIntelligentTieringConfigurationInput, s3api.PutBucketIntelligentTieringConfigurationOutput](s, put, func(in *s3api.PutBucketIntelligentTieringConfigurationInput, b *Bucket) *awserr.Error {
		f.put(b, in.Id, in.IntelligentTieringConfiguration)
		return nil
	}), cov, touches(put))
	del := func(in *s3api.DeleteBucketIntelligentTieringConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.DeleteBucketIntelligentTieringConfiguration(writer[s3api.DeleteBucketIntelligentTieringConfigurationInput, s3api.DeleteBucketIntelligentTieringConfigurationOutput](s, del, func(in *s3api.DeleteBucketIntelligentTieringConfigurationInput, b *Bucket) *awserr.Error {
		return f.remove(b, in.Id)
	}), cov, touches(del))
	list := func(in *s3api.ListBucketIntelligentTieringConfigurationsInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.ListBucketIntelligentTieringConfigurations(reader(s, list, func(in *s3api.ListBucketIntelligentTieringConfigurationsInput, b *Bucket) (*s3api.ListBucketIntelligentTieringConfigurationsOutput, *awserr.Error) {
		return &s3api.ListBucketIntelligentTieringConfigurationsOutput{IsTruncated: new(false), ContinuationToken: in.ContinuationToken, IntelligentTieringConfigurationList: f.list(b)}, nil
	}), cov, touches(list))
}
