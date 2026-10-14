package s3_test

import (
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// read is one Get of a subresource of bucket b.
type read func(h *harness, b string) (any, *awserr.Error)

// unset is a subresource read's answer while the subresource is unset:
// the default document out, or the not-configured error code and message
// (with a BucketName element unless noBucketName).
type unset struct {
	out          any
	code         string
	message      string
	noBucketName bool
}

func getPolicy(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketPolicy, &s3api.GetBucketPolicyInput{Bucket: b})
}

func getCORS(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketCors, &s3api.GetBucketCorsInput{Bucket: b})
}

func getWebsite(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketWebsite, &s3api.GetBucketWebsiteInput{Bucket: b})
}

func getVersioning(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketVersioning, &s3api.GetBucketVersioningInput{Bucket: b})
}

func getAccelerate(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketAccelerateConfiguration, &s3api.GetBucketAccelerateConfigurationInput{Bucket: b})
}

func getRequestPayment(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketRequestPayment, &s3api.GetBucketRequestPaymentInput{Bucket: b})
}

func getLogging(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketLogging, &s3api.GetBucketLoggingInput{Bucket: b})
}

func getLifecycle(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketLifecycleConfiguration, &s3api.GetBucketLifecycleConfigurationInput{Bucket: b})
}

func getReplication(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketReplication, &s3api.GetBucketReplicationInput{Bucket: b})
}

func getEncryption(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketEncryption, &s3api.GetBucketEncryptionInput{Bucket: b})
}

func getObjectLock(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetObjectLockConfiguration, &s3api.GetObjectLockConfigurationInput{Bucket: b})
}

func getTagging(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketTagging, &s3api.GetBucketTaggingInput{Bucket: b})
}

func getOwnership(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketOwnershipControls, &s3api.GetBucketOwnershipControlsInput{Bucket: b})
}

func getPublicAccessBlock(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetPublicAccessBlock, &s3api.GetPublicAccessBlockInput{Bucket: b})
}

func getMetrics(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketMetricsConfiguration, &s3api.GetBucketMetricsConfigurationInput{Bucket: b, Id: "all"})
}

func getAnalytics(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketAnalyticsConfiguration, &s3api.GetBucketAnalyticsConfigurationInput{Bucket: b, Id: "all"})
}

func getInventory(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketInventoryConfiguration, &s3api.GetBucketInventoryConfigurationInput{Bucket: b, Id: "all"})
}

func getIntelligentTiering(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketIntelligentTieringConfiguration, &s3api.GetBucketIntelligentTieringConfigurationInput{Bucket: b, Id: "all"})
}

func getNotification(h *harness, b string) (any, *awserr.Error) {
	return invoke(h, s3api.OpGetBucketNotificationConfiguration, &s3api.GetBucketNotificationConfigurationInput{Bucket: b})
}

// The configuration documents of the families keyed by Id.
func analyticsConfig(id string) s3api.AnalyticsConfiguration {
	return s3api.AnalyticsConfiguration{Id: id, Filter: s3api.AnalyticsFilterMemberPrefix{Value: "logs/"}}
}

func inventoryConfig(id string) s3api.InventoryConfiguration {
	return s3api.InventoryConfiguration{
		Id: id, IsEnabled: true, IncludedObjectVersions: s3api.InventoryIncludedObjectVersionsAll,
		Schedule:    s3api.InventorySchedule{Frequency: s3api.InventoryFrequencyDaily},
		Destination: s3api.InventoryDestination{S3BucketDestination: s3api.InventoryS3BucketDestination{Bucket: "arn:aws:s3:::inventory", Format: s3api.InventoryFormatCsv}},
	}
}

func tieringConfig(id string) s3api.IntelligentTieringConfiguration {
	return s3api.IntelligentTieringConfiguration{
		Id: id, Status: s3api.IntelligentTieringStatusEnabled,
		Tierings: []s3api.Tiering{{Days: 90, AccessTier: s3api.IntelligentTieringAccessTierArchiveAccess}},
	}
}

// fresh is every subresource read of a new bucket and AWS's answer
// (tf-storage.md 2.2 and 2.4, s3.md 4.1).
func fresh() map[string]struct {
	get  read
	want unset
} {
	type row = struct {
		get  read
		want unset
	}
	return map[string]row{
		"policy":      {getPolicy, unset{code: "NoSuchBucketPolicy", message: "The bucket policy does not exist"}},
		"cors":        {getCORS, unset{code: "NoSuchCORSConfiguration", message: "The CORS configuration does not exist"}},
		"website":     {getWebsite, unset{code: "NoSuchWebsiteConfiguration", message: "The specified bucket does not have a website configuration"}},
		"versioning":  {getVersioning, unset{out: &s3api.GetBucketVersioningOutput{}}},
		"accelerate":  {getAccelerate, unset{out: &s3api.GetBucketAccelerateConfigurationOutput{}}},
		"payment":     {getRequestPayment, unset{out: &s3api.GetBucketRequestPaymentOutput{Payer: new(s3api.PayerBucketOwner)}}},
		"logging":     {getLogging, unset{out: &s3api.GetBucketLoggingOutput{}}},
		"lifecycle":   {getLifecycle, unset{code: "NoSuchLifecycleConfiguration", message: "The lifecycle configuration does not exist"}},
		"replication": {getReplication, unset{code: "ReplicationConfigurationNotFoundError", message: "The replication configuration was not found"}},
		"encryption": {getEncryption, unset{out: &s3api.GetBucketEncryptionOutput{ServerSideEncryptionConfiguration: &s3api.ServerSideEncryptionConfiguration{
			Rules: []s3api.ServerSideEncryptionRule{{
				ApplyServerSideEncryptionByDefault: &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAes256},
				BucketKeyEnabled:                   new(false),
				// testdata/parity/s3/R06/01-get-get-bucket-encryption.json
				BlockedEncryptionTypes: &s3api.BlockedEncryptionTypes{EncryptionType: []s3api.EncryptionType{s3api.EncryptionTypeSseC}},
			}},
		}}}},
		"object lock": {getObjectLock, unset{code: "ObjectLockConfigurationNotFoundError", message: "Object Lock configuration does not exist for this bucket"}},
		"tagging":     {getTagging, unset{code: "NoSuchTagSet", message: "The TagSet does not exist"}},
		"ownership": {getOwnership, unset{out: &s3api.GetBucketOwnershipControlsOutput{OwnershipControls: &s3api.OwnershipControls{
			Rules: []s3api.OwnershipControlsRule{{ObjectOwnership: s3api.ObjectOwnershipBucketOwnerEnforced}},
		}}}},
		"public access block": {getPublicAccessBlock, unset{out: &s3api.GetPublicAccessBlockOutput{PublicAccessBlockConfiguration: &s3api.PublicAccessBlockConfiguration{
			BlockPublicAcls: new(true), IgnorePublicAcls: new(true), BlockPublicPolicy: new(true), RestrictPublicBuckets: new(true),
		}}}},
		"metrics":             {getMetrics, unset{code: "NoSuchConfiguration", message: "The specified configuration does not exist.", noBucketName: true}},
		"analytics":           {getAnalytics, unset{code: "NoSuchConfiguration", message: "The specified configuration does not exist.", noBucketName: true}},
		"inventory":           {getInventory, unset{code: "NoSuchConfiguration", message: "The specified configuration does not exist.", noBucketName: true}},
		"intelligent tiering": {getIntelligentTiering, unset{code: "NoSuchConfiguration", message: "The specified configuration does not exist.", noBucketName: true}},
		"notification":        {getNotification, unset{out: &s3api.NotificationConfiguration{}}},
	}
}

// check fails the test unless a read answered want: the same document, or
// the not-configured error with its exact status, body and region header.
func (want unset) check(t *testing.T, bucket string, out any, err *awserr.Error) {
	t.Helper()
	if want.code == "" {
		if err != nil {
			t.Fatalf("got %s: %s, want %+v", err.ID.QueryCode, err.Message, want.out)
		}
		if !reflect.DeepEqual(out, want.out) {
			t.Fatalf("got %+v, want %+v", out, want.out)
		}
		return
	}
	if err == nil {
		t.Fatalf("got %+v, want %s", out, want.code)
	}
	got := render(t, err)
	var fields []string
	if !want.noBucketName {
		fields = []string{"BucketName", bucket}
	}
	if body := errorBody(want.code, want.message, fields...); got.status != http.StatusNotFound || got.body != body {
		t.Fatalf("got %d %s, want 404 %s", got.status, got.body, body)
	}
	if r := got.header.Get("X-Amz-Bucket-Region"); r != "us-east-1" {
		t.Fatalf("x-amz-bucket-region = %q, want us-east-1", r)
	}
}

func TestFreshBucketReadsAnswerAWSDefaults(t *testing.T) {
	for name, tc := range fresh() {
		t.Run(name, func(t *testing.T) {
			run(t, s3.Options{}, func(h *harness) {
				h.createBucket("fresh-bucket")
				out, err := tc.get(h, "fresh-bucket")
				tc.want.check(h.T, "fresh-bucket", out, err)
			})
		})
	}
}

func TestFreshBucketACLIsOwnerFullControl(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("acl-bucket")
		out := ok(h, who{}, s3api.OpGetBucketAcl, &s3api.GetBucketAclInput{Bucket: "acl-bucket"})
		if out.Owner == nil || out.Owner.ID == nil || len(*out.Owner.ID) != 64 {
			t.Fatalf("owner = %+v, want a 64-character canonical id", out.Owner)
		}
		want := []s3api.Grant{{Grantee: &s3api.Grantee{Type: s3api.TypeCanonicalUser, ID: out.Owner.ID}, Permission: new(s3api.PermissionFullControl)}}
		if !reflect.DeepEqual(out.Grants, want) {
			t.Fatalf("grants = %+v, want the owner's FULL_CONTROL", out.Grants)
		}
	})
}

// roundTrip is a subresource's put, the read that echoes it, and the
// delete that restores the unset answer (nil when the subresource has no
// delete).
type roundTrip struct {
	setup func(h *harness, b string)
	put   func(h *harness, b string) *awserr.Error
	get   read
	want  any
	del   func(h *harness, b string) *awserr.Error
	unset unset
}

func errOnly[Out any](_ *Out, err *awserr.Error) *awserr.Error { return err }

// putACP sends PutBucketAcl with an AccessControlPolicy body of owner and
// grants.
func putACP(h *harness, b string, owner *s3api.Owner, grants ...s3api.Grant) *awserr.Error {
	return errOnly(invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{
		Bucket: b, AccessControlPolicy: &s3api.AccessControlPolicy{Owner: owner, Grants: grants},
	}))
}

// ownerOf is the owner GetBucketAcl names for bucket b.
func ownerOf(h *harness, b string) *s3api.Owner {
	h.Helper()
	return ok(h, who{}, s3api.OpGetBucketAcl, &s3api.GetBucketAclInput{Bucket: b}).Owner
}

// logDeliveryGrant grants the LogDelivery group p.
func logDeliveryGrant(p s3api.Permission) s3api.Grant {
	return s3api.Grant{
		Grantee:    &s3api.Grantee{Type: s3api.TypeGroup, URI: new("http://acs.amazonaws.com/groups/s3/LogDelivery")},
		Permission: new(p),
	}
}

func enableVersioning(h *harness, b string) {
	h.Helper()
	ok(h, who{}, s3api.OpPutBucketVersioning, &s3api.PutBucketVersioningInput{
		Bucket: b, VersioningConfiguration: s3api.VersioningConfiguration{Status: new(s3api.BucketVersioningStatusEnabled)},
	})
}

func roundTrips() map[string]roundTrip {
	cors := s3api.CORSConfiguration{CORSRules: []s3api.CORSRule{{AllowedMethods: []string{"GET", "PUT"}, AllowedOrigins: []string{"*"}, MaxAgeSeconds: new(int32(300))}}}
	website := s3api.WebsiteConfiguration{IndexDocument: &s3api.IndexDocument{Suffix: "index.html"}, ErrorDocument: &s3api.ErrorDocument{Key: "error.html"}}
	rules := []s3api.LifecycleRule{
		{ID: new("expire"), Status: s3api.ExpirationStatusEnabled, Filter: &s3api.LifecycleRuleFilter{}, Expiration: &s3api.LifecycleExpiration{Days: new(int32(30))}},
		{ID: new("logs"), Status: s3api.ExpirationStatusEnabled, Filter: &s3api.LifecycleRuleFilter{Prefix: new("")}, AbortIncompleteMultipartUpload: &s3api.AbortIncompleteMultipartUpload{DaysAfterInitiation: new(int32(7))}},
	}
	replication := s3api.ReplicationConfiguration{Role: "arn:aws:iam::000000000000:role/replication", Rules: []s3api.ReplicationRule{{
		Status: s3api.ReplicationRuleStatusEnabled, Destination: s3api.Destination{Bucket: "arn:aws:s3:::other"},
	}}}
	kms := s3api.ServerSideEncryptionConfiguration{Rules: []s3api.ServerSideEncryptionRule{{
		ApplyServerSideEncryptionByDefault: &s3api.ServerSideEncryptionByDefault{
			SSEAlgorithm: s3api.ServerSideEncryptionAwsKms, KMSMasterKeyID: new("arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab"),
		},
		BucketKeyEnabled: new(true),
	}}}
	open := s3api.PublicAccessBlockConfiguration{BlockPublicAcls: new(false), IgnorePublicAcls: new(false), BlockPublicPolicy: new(false), RestrictPublicBuckets: new(false)}
	preferred := s3api.OwnershipControls{Rules: []s3api.OwnershipControlsRule{{ObjectOwnership: s3api.ObjectOwnershipBucketOwnerPreferred}}}
	metrics := s3api.MetricsConfiguration{Id: "all", Filter: s3api.MetricsFilterMemberPrefix{Value: "logs/"}}
	analytics, inventory, tiering := analyticsConfig("all"), inventoryConfig("all"), tieringConfig("all")
	f := fresh()
	return map[string]roundTrip{
		"policy": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: b, Policy: `{"Version":"2012-10-17","Statement":[]}`}))
			},
			get:  getPolicy,
			want: &s3api.GetBucketPolicyOutput{Policy: new(`{"Version":"2012-10-17","Statement":[]}`)},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketPolicy, &s3api.DeleteBucketPolicyInput{Bucket: b}))
			},
			unset: f["policy"].want,
		},
		"cors": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketCors, &s3api.PutBucketCorsInput{Bucket: b, CORSConfiguration: cors}))
			},
			get:  getCORS,
			want: &s3api.GetBucketCorsOutput{CORSRules: cors.CORSRules},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketCors, &s3api.DeleteBucketCorsInput{Bucket: b}))
			},
			unset: f["cors"].want,
		},
		"website": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketWebsite, &s3api.PutBucketWebsiteInput{Bucket: b, WebsiteConfiguration: website}))
			},
			get:  getWebsite,
			want: &s3api.GetBucketWebsiteOutput{IndexDocument: website.IndexDocument, ErrorDocument: website.ErrorDocument},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketWebsite, &s3api.DeleteBucketWebsiteInput{Bucket: b}))
			},
			unset: f["website"].want,
		},
		"lifecycle": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketLifecycleConfiguration, &s3api.PutBucketLifecycleConfigurationInput{
					Bucket: b, LifecycleConfiguration: &s3api.BucketLifecycleConfiguration{Rules: rules},
				}))
			},
			get:  getLifecycle,
			want: &s3api.GetBucketLifecycleConfigurationOutput{Rules: rules, TransitionDefaultMinimumObjectSize: new(s3api.TransitionDefaultMinimumObjectSizeAllStorageClasses128k)},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketLifecycle, &s3api.DeleteBucketLifecycleInput{Bucket: b}))
			},
			unset: f["lifecycle"].want,
		},
		"replication": {
			setup: enableVersioning,
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketReplication, &s3api.PutBucketReplicationInput{Bucket: b, ReplicationConfiguration: replication}))
			},
			get:  getReplication,
			want: &s3api.GetBucketReplicationOutput{ReplicationConfiguration: &replication},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketReplication, &s3api.DeleteBucketReplicationInput{Bucket: b}))
			},
			unset: f["replication"].want,
		},
		"encryption": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketEncryption, &s3api.PutBucketEncryptionInput{Bucket: b, ServerSideEncryptionConfiguration: kms}))
			},
			get:  getEncryption,
			want: &s3api.GetBucketEncryptionOutput{ServerSideEncryptionConfiguration: &kms},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketEncryption, &s3api.DeleteBucketEncryptionInput{Bucket: b}))
			},
			unset: f["encryption"].want,
		},
		"public access block": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutPublicAccessBlock, &s3api.PutPublicAccessBlockInput{Bucket: b, PublicAccessBlockConfiguration: open}))
			},
			get:  getPublicAccessBlock,
			want: &s3api.GetPublicAccessBlockOutput{PublicAccessBlockConfiguration: &open},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeletePublicAccessBlock, &s3api.DeletePublicAccessBlockInput{Bucket: b}))
			},
			unset: unset{code: "NoSuchPublicAccessBlockConfiguration", message: "The public access block configuration was not found"},
		},
		"ownership": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketOwnershipControls, &s3api.PutBucketOwnershipControlsInput{Bucket: b, OwnershipControls: preferred}))
			},
			get:  getOwnership,
			want: &s3api.GetBucketOwnershipControlsOutput{OwnershipControls: &preferred},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketOwnershipControls, &s3api.DeleteBucketOwnershipControlsInput{Bucket: b}))
			},
			unset: unset{code: "OwnershipControlsNotFoundError", message: "The bucket ownership controls were not found"},
		},
		"tagging": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketTagging, &s3api.PutBucketTaggingInput{
					Bucket: b, Tagging: s3api.Tagging{TagSet: []s3api.Tag{{Key: "team", Value: "storage"}, {Key: "env", Value: "test"}}},
				}))
			},
			get:  getTagging,
			want: &s3api.GetBucketTaggingOutput{TagSet: []s3api.Tag{{Key: "env", Value: "test"}, {Key: "team", Value: "storage"}}},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketTagging, &s3api.DeleteBucketTaggingInput{Bucket: b}))
			},
			unset: f["tagging"].want,
		},
		"metrics": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketMetricsConfiguration, &s3api.PutBucketMetricsConfigurationInput{Bucket: b, Id: "all", MetricsConfiguration: metrics}))
			},
			get:  getMetrics,
			want: &s3api.GetBucketMetricsConfigurationOutput{MetricsConfiguration: &metrics},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketMetricsConfiguration, &s3api.DeleteBucketMetricsConfigurationInput{Bucket: b, Id: "all"}))
			},
			unset: f["metrics"].want,
		},
		"analytics": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketAnalyticsConfiguration, &s3api.PutBucketAnalyticsConfigurationInput{Bucket: b, Id: "all", AnalyticsConfiguration: analytics}))
			},
			get:  getAnalytics,
			want: &s3api.GetBucketAnalyticsConfigurationOutput{AnalyticsConfiguration: &analytics},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketAnalyticsConfiguration, &s3api.DeleteBucketAnalyticsConfigurationInput{Bucket: b, Id: "all"}))
			},
			unset: f["analytics"].want,
		},
		"inventory": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketInventoryConfiguration, &s3api.PutBucketInventoryConfigurationInput{Bucket: b, Id: "all", InventoryConfiguration: inventory}))
			},
			get:  getInventory,
			want: &s3api.GetBucketInventoryConfigurationOutput{InventoryConfiguration: &inventory},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketInventoryConfiguration, &s3api.DeleteBucketInventoryConfigurationInput{Bucket: b, Id: "all"}))
			},
			unset: f["inventory"].want,
		},
		"intelligent tiering": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketIntelligentTieringConfiguration, &s3api.PutBucketIntelligentTieringConfigurationInput{Bucket: b, Id: "all", IntelligentTieringConfiguration: tiering}))
			},
			get:  getIntelligentTiering,
			want: &s3api.GetBucketIntelligentTieringConfigurationOutput{IntelligentTieringConfiguration: &tiering},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketIntelligentTieringConfiguration, &s3api.DeleteBucketIntelligentTieringConfigurationInput{Bucket: b, Id: "all"}))
			},
			unset: f["intelligent tiering"].want,
		},
		"versioning": {
			put: func(h *harness, b string) *awserr.Error {
				enableVersioning(h, b)
				return nil
			},
			get:  getVersioning,
			want: &s3api.GetBucketVersioningOutput{Status: new(s3api.BucketVersioningStatusEnabled)},
		},
		"logging": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketLogging, &s3api.PutBucketLoggingInput{
					Bucket: b, BucketLoggingStatus: s3api.BucketLoggingStatus{LoggingEnabled: &s3api.LoggingEnabled{TargetBucket: "logs", TargetPrefix: "access/"}},
				}))
			},
			get:  getLogging,
			want: &s3api.GetBucketLoggingOutput{LoggingEnabled: &s3api.LoggingEnabled{TargetBucket: "logs", TargetPrefix: "access/"}},
			del: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketLogging, &s3api.PutBucketLoggingInput{Bucket: b}))
			},
			unset: f["logging"].want,
		},
		"accelerate": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketAccelerateConfiguration, &s3api.PutBucketAccelerateConfigurationInput{
					Bucket: b, AccelerateConfiguration: s3api.AccelerateConfiguration{Status: new(s3api.BucketAccelerateStatusEnabled)},
				}))
			},
			get:  getAccelerate,
			want: &s3api.GetBucketAccelerateConfigurationOutput{Status: new(s3api.BucketAccelerateStatusEnabled)},
		},
		"payment": {
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketRequestPayment, &s3api.PutBucketRequestPaymentInput{
					Bucket: b, RequestPaymentConfiguration: s3api.RequestPaymentConfiguration{Payer: s3api.PayerRequester},
				}))
			},
			get:  getRequestPayment,
			want: &s3api.GetBucketRequestPaymentOutput{Payer: new(s3api.PayerRequester)},
		},
		"object lock": {
			setup: enableVersioning,
			put: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutObjectLockConfiguration, &s3api.PutObjectLockConfigurationInput{
					Bucket: b, ObjectLockConfiguration: &s3api.ObjectLockConfiguration{ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled)},
				}))
			},
			get:  getObjectLock,
			want: &s3api.GetObjectLockConfigurationOutput{ObjectLockConfiguration: &s3api.ObjectLockConfiguration{ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled)}},
		},
	}
}

func TestSubresourcePutGetDeleteRoundTrip(t *testing.T) {
	for name, tc := range roundTrips() {
		t.Run(name, func(t *testing.T) {
			run(t, s3.Options{}, func(h *harness) {
				const b = "round-trip"
				h.createBucket(b)
				if tc.setup != nil {
					tc.setup(h, b)
				}
				if err := tc.put(h, b); err != nil {
					t.Fatalf("put: %s: %s", err.ID.QueryCode, err.Message)
				}
				out, err := tc.get(h, b)
				unset{out: tc.want}.check(h.T, b, out, err)
				if tc.del == nil {
					return
				}
				if err := tc.del(h, b); err != nil {
					t.Fatalf("delete: %s: %s", err.ID.QueryCode, err.Message)
				}
				out, err = tc.get(h, b)
				tc.unset.check(h.T, b, out, err)
			})
		})
	}
}

func TestSubresourceOfMissingBucketIsNoSuchBucket(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		for name, tc := range fresh() {
			_, err := tc.get(h, "no-such-bucket")
			if err == nil {
				t.Fatalf("%s: got success, want NoSuchBucket", name)
			}
			got := render(h.T, err)
			want := errorBody("NoSuchBucket", "The specified bucket does not exist", "BucketName", "no-such-bucket")
			if got.status != http.StatusNotFound || got.body != want {
				t.Fatalf("%s: got %d %s, want 404 %s", name, got.status, got.body, want)
			}
		}
	})
}

func TestSubresourceRulesRecordedAgainstAWS(t *testing.T) {
	cases := map[string]struct {
		setup  func(h *harness, b string)
		call   func(h *harness, b string) *awserr.Error
		code   string
		status int
		// stateful marks an error the bucket's state decides, which
		// carries x-amz-bucket-region (ADR 0013).
		stateful bool
		// fields are context elements the error must carry.
		fields map[string]string
	}{
		"empty versioning configuration": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketVersioning, &s3api.PutBucketVersioningInput{Bucket: b}))
			},
			code: "IllegalVersioningConfigurationException", status: 400,
		},
		"lowercase versioning status": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketVersioning, &s3api.PutBucketVersioningInput{
					Bucket: b, VersioningConfiguration: s3api.VersioningConfiguration{Status: new(s3api.BucketVersioningStatus("enabled"))},
				}))
			},
			code: "MalformedXML", status: 400,
		},
		"suspend versioning under object lock": {
			setup: func(h *harness, b string) {
				enableVersioning(h, b)
				ok(h, who{}, s3api.OpPutObjectLockConfiguration, &s3api.PutObjectLockConfigurationInput{
					Bucket: b, ObjectLockConfiguration: &s3api.ObjectLockConfiguration{ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled)},
				})
			},
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketVersioning, &s3api.PutBucketVersioningInput{
					Bucket: b, VersioningConfiguration: s3api.VersioningConfiguration{Status: new(s3api.BucketVersioningStatusSuspended)},
				}))
			},
			code: "InvalidBucketState", status: 409, stateful: true,
		},
		"object lock without versioning": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutObjectLockConfiguration, &s3api.PutObjectLockConfigurationInput{
					Bucket: b, ObjectLockConfiguration: &s3api.ObjectLockConfiguration{ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled)},
				}))
			},
			code: "InvalidBucketState", status: 409, stateful: true,
		},
		"replication without versioning": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketReplication, &s3api.PutBucketReplicationInput{
					Bucket: b, ReplicationConfiguration: s3api.ReplicationConfiguration{Role: "arn:aws:iam::000000000000:role/r", Rules: []s3api.ReplicationRule{{
						Status: s3api.ReplicationRuleStatusEnabled, Destination: s3api.Destination{Bucket: "arn:aws:s3:::other"},
					}}},
				}))
			},
			code: "InvalidRequest", status: 400, stateful: true,
		},
		"two encryption rules": {
			call: func(h *harness, b string) *awserr.Error {
				rule := s3api.ServerSideEncryptionRule{ApplyServerSideEncryptionByDefault: &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAes256}}
				return errOnly(invoke(h, s3api.OpPutBucketEncryption, &s3api.PutBucketEncryptionInput{
					Bucket: b, ServerSideEncryptionConfiguration: s3api.ServerSideEncryptionConfiguration{Rules: []s3api.ServerSideEncryptionRule{rule, rule}},
				}))
			},
			code: "MalformedXML", status: 400,
		},
		"KMS key with AES256": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketEncryption, &s3api.PutBucketEncryptionInput{
					Bucket: b, ServerSideEncryptionConfiguration: s3api.ServerSideEncryptionConfiguration{Rules: []s3api.ServerSideEncryptionRule{{
						ApplyServerSideEncryptionByDefault: &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAes256, KMSMasterKeyID: new("key")},
					}}},
				}))
			},
			code: "InvalidArgument", status: 400,
		},
		"policy that is not JSON": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: b, Policy: "not json"}))
			},
			code: "MalformedPolicy", status: 400,
		},
		"policy without statements": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: b, Policy: "{}"}))
			},
			code: "MalformedPolicy", status: 400,
		},
		"lifecycle rule without filter": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketLifecycleConfiguration, &s3api.PutBucketLifecycleConfigurationInput{
					Bucket: b, LifecycleConfiguration: &s3api.BucketLifecycleConfiguration{Rules: []s3api.LifecycleRule{{ID: new("r"), Status: s3api.ExpirationStatusEnabled}}},
				}))
			},
			code: "MalformedXML", status: 400,
		},
		"bad transition default": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketLifecycleConfiguration, &s3api.PutBucketLifecycleConfigurationInput{
					Bucket: b, TransitionDefaultMinimumObjectSize: new(s3api.TransitionDefaultMinimumObjectSize("value")),
					LifecycleConfiguration: &s3api.BucketLifecycleConfiguration{Rules: []s3api.LifecycleRule{{ID: new("r"), Status: s3api.ExpirationStatusEnabled, Filter: &s3api.LifecycleRuleFilter{}}}},
				}))
			},
			code: "InvalidRequest", status: 400,
		},
		"lifecycle rules sharing an ID": {
			call: func(h *harness, b string) *awserr.Error {
				rule := s3api.LifecycleRule{ID: new("same"), Status: s3api.ExpirationStatusEnabled, Filter: &s3api.LifecycleRuleFilter{}, Expiration: &s3api.LifecycleExpiration{Days: new(int32(1))}}
				return errOnly(invoke(h, s3api.OpPutBucketLifecycleConfiguration, &s3api.PutBucketLifecycleConfigurationInput{
					Bucket: b, LifecycleConfiguration: &s3api.BucketLifecycleConfiguration{Rules: []s3api.LifecycleRule{rule, rule}},
				}))
			},
			code: "InvalidArgument", status: 400, fields: map[string]string{"ArgumentName": "ID", "ArgumentValue": "same"},
		},
		"over 1000 lifecycle rules": {
			call: func(h *harness, b string) *awserr.Error {
				rules := make([]s3api.LifecycleRule, 1001)
				for i := range rules {
					rules[i] = s3api.LifecycleRule{ID: new(strconv.Itoa(i)), Status: s3api.ExpirationStatusEnabled, Filter: &s3api.LifecycleRuleFilter{}, Expiration: &s3api.LifecycleExpiration{Days: new(int32(1))}}
				}
				return errOnly(invoke(h, s3api.OpPutBucketLifecycleConfiguration, &s3api.PutBucketLifecycleConfigurationInput{
					Bucket: b, LifecycleConfiguration: &s3api.BucketLifecycleConfiguration{Rules: rules},
				}))
			},
			code: "InvalidArgument", status: 400,
		},
		"two ownership rules": {
			call: func(h *harness, b string) *awserr.Error {
				rule := s3api.OwnershipControlsRule{ObjectOwnership: s3api.ObjectOwnershipObjectWriter}
				return errOnly(invoke(h, s3api.OpPutBucketOwnershipControls, &s3api.PutBucketOwnershipControlsInput{
					Bucket: b, OwnershipControls: s3api.OwnershipControls{Rules: []s3api.OwnershipControlsRule{rule, rule}},
				}))
			},
			code: "MalformedXML", status: 400,
		},
		"ACL on an owner-enforced bucket": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: b, ACL: new(s3api.BucketCannedACLPrivate)}))
			},
			code: "AccessControlListNotSupported", status: 400, stateful: true,
		},
		"ACL with nothing to set": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: b}))
			},
			code: "MissingSecurityHeader", status: 400,
		},
		"canned ACL with grant headers": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: b, ACL: new(s3api.BucketCannedACLPrivate), GrantRead: new(`id="abc"`)}))
			},
			code: "InvalidRequest", status: 400,
		},
		"canned ACL with a policy body": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{
					Bucket: b, ACL: new(s3api.BucketCannedACLPrivate), AccessControlPolicy: &s3api.AccessControlPolicy{},
				}))
			},
			code: "UnexpectedContent", status: 400,
		},
		"grant headers with a policy body": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{
					Bucket: b, GrantRead: new(`id="abc"`), AccessControlPolicy: &s3api.AccessControlPolicy{},
				}))
			},
			code: "UnexpectedContent", status: 400,
		},
		// test_s3_bucket_acl_exceptions, recorded on an owner-enforced
		// bucket: malformed access control is refused before ownership.
		"grant header id that is not a canonical id": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: b, GrantWrite: new(`id="wrong-id"`)}))
			},
			code: "InvalidArgument", status: 400, fields: map[string]string{"ArgumentName": "id", "ArgumentValue": "wrong-id"},
		},
		"grant header uri of no group": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: b, GrantWrite: new(`uri="http://acs.amazonaws.com/groups/s3/FakeGroup"`)}))
			},
			code: "InvalidArgument", status: 400, fields: map[string]string{"ArgumentName": "uri", "ArgumentValue": "http://acs.amazonaws.com/groups/s3/FakeGroup"},
		},
		"policy body without an owner": {
			call: func(h *harness, b string) *awserr.Error {
				return putACP(h, b, nil, logDeliveryGrant(s3api.PermissionWrite))
			},
			code: "MalformedACLError", status: 400,
		},
		"empty policy body": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: b, AccessControlPolicy: &s3api.AccessControlPolicy{}}))
			},
			code: "MalformedACLError", status: 400,
		},
		"policy body owner id that is not a canonical id": {
			call: func(h *harness, b string) *awserr.Error {
				return putACP(h, b, &s3api.Owner{ID: new("wrong-id")}, logDeliveryGrant(s3api.PermissionFullControl))
			},
			code: "InvalidArgument", status: 400, fields: map[string]string{"ArgumentName": "CanonicalUser/ID", "ArgumentValue": "wrong-id"},
		},
		"policy body uri of no group": {
			call: func(h *harness, b string) *awserr.Error {
				g := logDeliveryGrant(s3api.PermissionFullControl)
				g.Grantee.URI = new("http://acs.amazonaws.com/groups/s3/FakeGroup")
				return putACP(h, b, ownerOf(h, b), g)
			},
			code: "InvalidArgument", status: 400, fields: map[string]string{"ArgumentName": "Group/URI", "ArgumentValue": "http://acs.amazonaws.com/groups/s3/FakeGroup"},
		},
		"policy body grantee id that is not a canonical id": {
			call: func(h *harness, b string) *awserr.Error {
				g := s3api.Grant{Grantee: &s3api.Grantee{Type: s3api.TypeCanonicalUser, ID: new("wrong-id")}, Permission: new(s3api.PermissionFullControl)}
				return putACP(h, b, ownerOf(h, b), g)
			},
			code: "InvalidArgument", status: 400, fields: map[string]string{"ArgumentName": "CanonicalUser/ID", "ArgumentValue": "wrong-id"},
		},
		"duplicate tag keys": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketTagging, &s3api.PutBucketTaggingInput{
					Bucket: b, Tagging: s3api.Tagging{TagSet: []s3api.Tag{{Key: "k", Value: "1"}, {Key: "k", Value: "2"}}},
				}))
			},
			code: "InvalidTag", status: 400,
		},
		"system tag key": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpPutBucketTagging, &s3api.PutBucketTaggingInput{
					Bucket: b, Tagging: s3api.Tagging{TagSet: []s3api.Tag{{Key: "aws:k", Value: "1"}}},
				}))
			},
			code: "InvalidTag", status: 400,
		},
		"delete an absent metrics configuration": {
			call: func(h *harness, b string) *awserr.Error {
				return errOnly(invoke(h, s3api.OpDeleteBucketMetricsConfiguration, &s3api.DeleteBucketMetricsConfigurationInput{Bucket: b, Id: "none"}))
			},
			code: "NoSuchConfiguration", status: 404, stateful: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			run(t, s3.Options{}, func(h *harness) {
				const b = "rules"
				h.createBucket(b)
				if tc.setup != nil {
					tc.setup(h, b)
				}
				err := tc.call(h, b)
				wantCode(h.T, err, tc.code, tc.status)
				wantFields(h.T, err, tc.fields)
				if r := header(err, "x-amz-bucket-region"); tc.stateful && r != "us-east-1" {
					t.Fatalf("x-amz-bucket-region = %q, want us-east-1", r)
				}
			})
		})
	}
}

// TestLifecycleDayLimits: a lifecycle Put is refused with InvalidArgument
// when an expiration is not a positive number of days, a transition to an
// Infrequent Access class comes before 30 days, or a rule moves an object
// on before the earlier class's minimum storage duration (API_Transition,
// API_LifecycleExpiration, lifecycle-transition-general-considerations).
// A 0-day transition to another class and a chain that keeps the minimums
// are stored.
func TestLifecycleDayLimits(t *testing.T) {
	transition := func(days int32, c s3api.TransitionStorageClass) s3api.Transition {
		return s3api.Transition{Days: new(days), StorageClass: new(c)}
	}
	noncurrent := func(days int32, c s3api.TransitionStorageClass) s3api.NoncurrentVersionTransition {
		return s3api.NoncurrentVersionTransition{NoncurrentDays: new(days), StorageClass: new(c)}
	}
	for name, tc := range map[string]struct {
		rule s3api.LifecycleRule
		// refusal is the InvalidArgument message, "" when the Put succeeds.
		refusal string
		// fields, when set, are the refusal's ArgumentName and
		// ArgumentValue.
		fields map[string]string
	}{
		"0-day transition to Glacier": {rule: s3api.LifecycleRule{Transitions: []s3api.Transition{transition(0, s3api.TransitionStorageClassGlacier)}}},
		"waterfall keeping each minimum": {rule: s3api.LifecycleRule{Transitions: []s3api.Transition{
			transition(150, s3api.TransitionStorageClassDeepArchive), transition(30, s3api.TransitionStorageClassStandardIa), transition(60, s3api.TransitionStorageClassGlacierIr),
		}}},
		"30-day noncurrent transition to One Zone-IA": {rule: s3api.LifecycleRule{NoncurrentVersionTransitions: []s3api.NoncurrentVersionTransition{noncurrent(30, s3api.TransitionStorageClassOnezoneIa)}}},
		"7-day transition to Standard-IA": {
			rule:    s3api.LifecycleRule{Transitions: []s3api.Transition{transition(7, s3api.TransitionStorageClassStandardIa)}},
			refusal: "'Days' in Transition action must be greater than or equal to 30 for storageClass 'STANDARD_IA'",
		},
		"transition to One Zone-IA without days": {
			rule:    s3api.LifecycleRule{Transitions: []s3api.Transition{{StorageClass: new(s3api.TransitionStorageClassOnezoneIa)}}},
			refusal: "'Days' in Transition action must be greater than or equal to 30 for storageClass 'ONEZONE_IA'",
		},
		"10-day noncurrent transition to Standard-IA": {
			rule:    s3api.LifecycleRule{NoncurrentVersionTransitions: []s3api.NoncurrentVersionTransition{noncurrent(10, s3api.TransitionStorageClassStandardIa)}},
			refusal: "'NoncurrentDays' in NoncurrentVersionTransition action must be greater than or equal to 30 for storageClass 'STANDARD_IA'",
		},
		"Deep Archive 16 days after Glacier Instant Retrieval": {
			rule: s3api.LifecycleRule{Transitions: []s3api.Transition{
				transition(4, s3api.TransitionStorageClassGlacierIr), transition(20, s3api.TransitionStorageClassDeepArchive),
			}},
			refusal: "'Days' in the 'Transition' action for StorageClass 'DEEP_ARCHIVE' must be at least 90 days more than 'Days' in the 'Transition' action for StorageClass 'GLACIER_IR'",
		},
		"noncurrent Glacier 10 days after Standard-IA": {
			rule: s3api.LifecycleRule{NoncurrentVersionTransitions: []s3api.NoncurrentVersionTransition{
				noncurrent(30, s3api.TransitionStorageClassStandardIa), noncurrent(40, s3api.TransitionStorageClassGlacier),
			}},
			refusal: "'NoncurrentDays' in the 'NoncurrentVersionTransition' action for StorageClass 'GLACIER' must be at least 30 days more than 'NoncurrentDays' in the 'NoncurrentVersionTransition' action for StorageClass 'STANDARD_IA'",
		},
		// testdata/parity/s3/R12/12-lifecycle-expiration-0.json
		"0-day expiration": {
			rule:    s3api.LifecycleRule{Expiration: &s3api.LifecycleExpiration{Days: new(int32(0))}},
			refusal: "'Days' for Expiration action must be a positive integer",
			fields:  map[string]string{"ArgumentName": "Days", "ArgumentValue": "0"},
		},
		// testdata/parity/s3/R12/12-lifecycle-noncurrent-expiration-0.json
		"0-day noncurrent expiration": {
			rule:    s3api.LifecycleRule{NoncurrentVersionExpiration: &s3api.NoncurrentVersionExpiration{NoncurrentDays: new(int32(0))}},
			refusal: "'NoncurrentDays' for NoncurrentVersionExpiration action must be a positive integer",
			fields:  map[string]string{"ArgumentName": "NoncurrentDays", "ArgumentValue": "0"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			run(t, s3.Options{}, func(h *harness) {
				h.createBucket("days")
				rule := tc.rule
				rule.ID, rule.Status, rule.Filter = new("r"), s3api.ExpirationStatusEnabled, &s3api.LifecycleRuleFilter{}
				_, err := invoke(h, s3api.OpPutBucketLifecycleConfiguration, &s3api.PutBucketLifecycleConfigurationInput{
					Bucket: "days", LifecycleConfiguration: &s3api.BucketLifecycleConfiguration{Rules: []s3api.LifecycleRule{rule}},
				})
				if tc.refusal == "" {
					if err != nil {
						h.Fatalf("PutBucketLifecycleConfiguration = %s (%s), want success", err.ID.QueryCode, err.Message)
					}
					return
				}
				wantCode(h.T, err, "InvalidArgument", http.StatusBadRequest)
				if err.Message != tc.refusal {
					h.Fatalf("message = %q, want %q", err.Message, tc.refusal)
				}
				if tc.fields != nil {
					wantFieldOrder(h.T, err, "ArgumentName", "ArgumentValue")
					wantFields(h.T, err, tc.fields)
				}
			})
		})
	}
}

// TestConfigurationFamiliesList: each family keyed by Id lists every
// configuration in Id order on one page, and a delete removes only its Id.
func TestConfigurationFamiliesList(t *testing.T) {
	type family struct {
		put  func(h *harness, b, id string)
		del  func(h *harness, b, id string)
		list func(h *harness, b string) []string
	}
	cases := map[string]family{
		"metrics": {
			put: func(h *harness, b, id string) {
				ok(h, who{}, s3api.OpPutBucketMetricsConfiguration, &s3api.PutBucketMetricsConfigurationInput{Bucket: b, Id: id, MetricsConfiguration: s3api.MetricsConfiguration{Id: id}})
			},
			del: func(h *harness, b, id string) {
				ok(h, who{}, s3api.OpDeleteBucketMetricsConfiguration, &s3api.DeleteBucketMetricsConfigurationInput{Bucket: b, Id: id})
			},
			list: func(h *harness, b string) []string {
				out := ok(h, who{}, s3api.OpListBucketMetricsConfigurations, &s3api.ListBucketMetricsConfigurationsInput{Bucket: b})
				if out.IsTruncated == nil || *out.IsTruncated {
					h.Fatalf("IsTruncated = %v, want false", out.IsTruncated)
				}
				return ids(out.MetricsConfigurationList, func(c s3api.MetricsConfiguration) string { return c.Id })
			},
		},
		"analytics": {
			put: func(h *harness, b, id string) {
				ok(h, who{}, s3api.OpPutBucketAnalyticsConfiguration, &s3api.PutBucketAnalyticsConfigurationInput{Bucket: b, Id: id, AnalyticsConfiguration: analyticsConfig(id)})
			},
			del: func(h *harness, b, id string) {
				ok(h, who{}, s3api.OpDeleteBucketAnalyticsConfiguration, &s3api.DeleteBucketAnalyticsConfigurationInput{Bucket: b, Id: id})
			},
			list: func(h *harness, b string) []string {
				out := ok(h, who{}, s3api.OpListBucketAnalyticsConfigurations, &s3api.ListBucketAnalyticsConfigurationsInput{Bucket: b})
				if out.IsTruncated == nil || *out.IsTruncated {
					h.Fatalf("IsTruncated = %v, want false", out.IsTruncated)
				}
				return ids(out.AnalyticsConfigurationList, func(c s3api.AnalyticsConfiguration) string { return c.Id })
			},
		},
		"inventory": {
			put: func(h *harness, b, id string) {
				ok(h, who{}, s3api.OpPutBucketInventoryConfiguration, &s3api.PutBucketInventoryConfigurationInput{Bucket: b, Id: id, InventoryConfiguration: inventoryConfig(id)})
			},
			del: func(h *harness, b, id string) {
				ok(h, who{}, s3api.OpDeleteBucketInventoryConfiguration, &s3api.DeleteBucketInventoryConfigurationInput{Bucket: b, Id: id})
			},
			list: func(h *harness, b string) []string {
				out := ok(h, who{}, s3api.OpListBucketInventoryConfigurations, &s3api.ListBucketInventoryConfigurationsInput{Bucket: b})
				if out.IsTruncated == nil || *out.IsTruncated {
					h.Fatalf("IsTruncated = %v, want false", out.IsTruncated)
				}
				return ids(out.InventoryConfigurationList, func(c s3api.InventoryConfiguration) string { return c.Id })
			},
		},
		"intelligent tiering": {
			put: func(h *harness, b, id string) {
				ok(h, who{}, s3api.OpPutBucketIntelligentTieringConfiguration, &s3api.PutBucketIntelligentTieringConfigurationInput{Bucket: b, Id: id, IntelligentTieringConfiguration: tieringConfig(id)})
			},
			del: func(h *harness, b, id string) {
				ok(h, who{}, s3api.OpDeleteBucketIntelligentTieringConfiguration, &s3api.DeleteBucketIntelligentTieringConfigurationInput{Bucket: b, Id: id})
			},
			list: func(h *harness, b string) []string {
				out := ok(h, who{}, s3api.OpListBucketIntelligentTieringConfigurations, &s3api.ListBucketIntelligentTieringConfigurationsInput{Bucket: b})
				if out.IsTruncated == nil || *out.IsTruncated {
					h.Fatalf("IsTruncated = %v, want false", out.IsTruncated)
				}
				return ids(out.IntelligentTieringConfigurationList, func(c s3api.IntelligentTieringConfiguration) string { return c.Id })
			},
		},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			run(t, s3.Options{}, func(h *harness) {
				const b = "families"
				h.createBucket(b)
				if got := f.list(h, b); len(got) != 0 {
					t.Fatalf("list of a new bucket = %v, want none", got)
				}
				f.put(h, b, "zeta")
				f.put(h, b, "alpha")
				if got, want := f.list(h, b), []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("list = %v, want %v", got, want)
				}
				f.del(h, b, "alpha")
				if got, want := f.list(h, b), []string{"zeta"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("list after deleting alpha = %v, want %v", got, want)
				}
			})
		})
	}
}

// ids is the Id of each configuration in cs, in order.
func ids[C any](cs []C, id func(C) string) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, id(c))
	}
	return out
}

func TestBucketACLWithPreferredOwnership(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		const b = "acl-preferred"
		h.createBucket(b)
		ok(h, who{}, s3api.OpPutBucketOwnershipControls, &s3api.PutBucketOwnershipControlsInput{
			Bucket: b, OwnershipControls: s3api.OwnershipControls{Rules: []s3api.OwnershipControlsRule{{ObjectOwnership: s3api.ObjectOwnershipBucketOwnerPreferred}}},
		})
		ok(h, who{}, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: b, ACL: new(s3api.BucketCannedACLPublicRead)})
		out := ok(h, who{}, s3api.OpGetBucketAcl, &s3api.GetBucketAclInput{Bucket: b})
		want := []s3api.Grant{
			{Grantee: &s3api.Grantee{Type: s3api.TypeCanonicalUser, ID: out.Owner.ID}, Permission: new(s3api.PermissionFullControl)},
			{Grantee: &s3api.Grantee{Type: s3api.TypeGroup, URI: new("http://acs.amazonaws.com/groups/global/AllUsers")}, Permission: new(s3api.PermissionRead)},
		}
		if !reflect.DeepEqual(out.Grants, want) {
			t.Fatalf("grants = %+v, want owner FULL_CONTROL and AllUsers READ", out.Grants)
		}
		_, err := invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: b, ACL: new(s3api.BucketCannedACLPrivate), GrantRead: new(`id="abc"`)})
		wantCode(h.T, err, "InvalidRequest", 400)
		_, err = invoke(h, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: b})
		wantCode(h.T, err, "MissingSecurityHeader", 400)
	})
}
