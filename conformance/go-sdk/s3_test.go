package gosdk_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash/crc32"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	tmtypes "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Research: docs/research/phase1/s3.md, wire-rest.md,
// sigv4-checksums-endpoints.md, s3-checksum-residuals-and-record-backlog.md,
// error-matrix.md 5.4.

// helloCRC32 is the base64 CRC32 of "hello", as captured on the wire from
// the real clients (s3.md section 2, PROBE E4 and E7).
const helloCRC32 = "NhCmhg=="

const mib = 1024 * 1024

// s3Client is an S3 client with the default settings plus opts.
func s3Client(opts ...func(*s3.Options)) *s3.Client {
	return s3.NewFromConfig(awsCfg, opts...)
}

// createBucket creates a bucket through client and, when the test ends,
// empties and deletes it.
func createBucket(t *testing.T, client *s3.Client) string {
	t.Helper()
	bucket := name("cf")
	_, err := client.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	ok(t, err, "CreateBucket "+bucket)
	t.Cleanup(func() { deleteBucketFully(client, bucket) })
	return bucket
}

// deleteBucketFully removes every version, delete marker and multipart
// upload in bucket, then the bucket itself.
func deleteBucketFully(client *s3.Client, bucket string) {
	ctx := context.Background()
	versions, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
	if err == nil {
		for _, v := range versions.Versions {
			_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range versions.DeleteMarkers {
			_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: m.Key, VersionId: m.VersionId})
		}
	}
	objects, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err == nil {
		for _, o := range objects.Contents {
			_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: o.Key})
		}
	}
	uploads, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket)})
	if err == nil {
		for _, u := range uploads.Uploads {
			_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: u.Key, UploadId: u.UploadId})
		}
	}
	_, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
}

// putObject writes body under key and returns the response.
func putObject(t *testing.T, client *s3.Client, bucket, key, body string) *s3.PutObjectOutput {
	t.Helper()
	out, err := client.PutObject(t.Context(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(body)})
	ok(t, err, "PutObject "+key)
	return out
}

// getBody reads the whole object at key.
func getBody(t *testing.T, client *s3.Client, bucket, key string) string {
	t.Helper()
	out, err := client.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	ok(t, err, "GetObject "+key)
	return string(readAll(t, out.Body, "the GetObject body"))
}

// quotedMD5 is the ETag S3 gives a plaintext or SSE-S3 single-part object.
func quotedMD5(b []byte) string {
	sum := md5.Sum(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// multipartETag is the ETag of a multipart object: the MD5 of the
// concatenated raw part MD5s, a dash and the part count (s3.md 5.3).
func multipartETag(parts [][]byte) string {
	var cat []byte
	for _, p := range parts {
		sum := md5.Sum(p)
		cat = append(cat, sum[:]...)
	}
	sum := md5.Sum(cat)
	return `"` + hex.EncodeToString(sum[:]) + "-" + strconv.Itoa(len(parts)) + `"`
}

// crc32B64 is the base64 of the big-endian CRC32 of b.
func crc32B64(b []byte) string {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], crc32.ChecksumIEEE(b))
	return base64.StdEncoding.EncodeToString(buf[:])
}

// compositeCRC32 is the composite multipart checksum: the CRC32 of the
// concatenated raw part CRC32s, a dash and the part count (s3.md 6.4).
func compositeCRC32(parts [][]byte) string {
	var cat []byte
	for _, p := range parts {
		cat = binary.BigEndian.AppendUint32(cat, crc32.ChecksumIEEE(p))
	}
	return crc32B64(cat) + "-" + strconv.Itoa(len(parts))
}

// randomBytes returns n random bytes.
func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	ok(t, err, "rand.Read")
	return b
}

// httpStatus is the HTTP status of a successful call.
func httpStatus(t *testing.T, md middleware.Metadata) int {
	t.Helper()
	raw, isResp := awsmiddleware.GetRawResponse(md).(*smithyhttp.Response)
	if !isResp {
		t.Fatalf("no raw HTTP response in the result metadata")
	}
	return raw.StatusCode
}

func TestS3Buckets(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := createBucket(t, client)

	t.Run("HeadBucket and GetBucketLocation", func(t *testing.T) {
		head, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
		ok(t, err, "HeadBucket")
		if got := aws.ToString(head.BucketRegion); got != region() {
			t.Errorf("HeadBucket BucketRegion = %q, want %q", got, region())
		}
		loc, err := client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketLocation")
		if region() == "us-east-1" && loc.LocationConstraint != "" {
			t.Errorf("GetBucketLocation of a us-east-1 bucket = %q, want empty", loc.LocationConstraint)
		}
	})

	t.Run("ListBuckets", func(t *testing.T) {
		out, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
		ok(t, err, "ListBuckets")
		i := slices.IndexFunc(out.Buckets, func(b types.Bucket) bool { return aws.ToString(b.Name) == bucket })
		if i < 0 {
			t.Fatalf("ListBuckets does not list %s", bucket)
		}
		if b := out.Buckets[i]; b.CreationDate == nil || aws.ToString(b.BucketRegion) != region() {
			t.Errorf("ListBuckets entry = CreationDate %v, BucketRegion %q; want a date and %q", b.CreationDate, aws.ToString(b.BucketRegion), region())
		}
	})

	t.Run("CreateBucket errors", func(t *testing.T) {
		if region() != "us-east-1" {
			t.Skip("the recorded matrix is for us-east-1")
		}
		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		ok(t, err, "re-creating an owned bucket in us-east-1 without tags (200 per s3.md 3.2)")

		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{
			Bucket:                    aws.String(name("cf")),
			CreateBucketConfiguration: &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraint("us-east-1")},
		})
		apiErr(t, err, "CreateBucket LocationConstraint us-east-1 in us-east-1", "InvalidLocationConstraint")
		wantStatus(t, err, "CreateBucket LocationConstraint us-east-1", 400)

		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("Bad_Bucket_Name")})
		apiErr(t, err, "CreateBucket with an invalid name", "InvalidBucketName")
		wantStatus(t, err, "CreateBucket with an invalid name", 400)
	})

	t.Run("missing bucket", func(t *testing.T) {
		missing := name("missing")
		_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(missing)})
		errAs[*types.NotFound](t, err, "HeadBucket missing")
		wantStatus(t, err, "HeadBucket missing", 404)
		_, err = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(missing)})
		apiErr(t, err, "DeleteBucket missing", "NoSuchBucket")
		wantStatus(t, err, "DeleteBucket missing", 404)
		_, err = client.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(missing)})
		apiErr(t, err, "GetBucketPolicy on a missing bucket (NoSuchBucket wins over not-configured)", "NoSuchBucket")
		_, err = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(missing)})
		errAs[*types.NoSuchBucket](t, err, "ListObjectsV2 missing bucket")
	})

	t.Run("DeleteBucket not empty", func(t *testing.T) {
		b := createBucket(t, client)
		putObject(t, client, b, "k", "v")
		_, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(b)})
		apiErr(t, err, "DeleteBucket non-empty", "BucketNotEmpty")
		wantStatus(t, err, "DeleteBucket non-empty", 409)
	})

	t.Run("DeleteBucket", func(t *testing.T) {
		b := name("cf")
		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(b)})
		ok(t, err, "CreateBucket")
		out, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(b)})
		ok(t, err, "DeleteBucket")
		if s := httpStatus(t, out.ResultMetadata); s != 204 {
			t.Errorf("DeleteBucket status = %d, want 204", s)
		}
		_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(b)})
		errAs[*types.NotFound](t, err, "HeadBucket after DeleteBucket")
	})

	t.Run("fresh bucket defaults", func(t *testing.T) {
		own, err := client.GetBucketOwnershipControls(ctx, &s3.GetBucketOwnershipControlsInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketOwnershipControls on a new bucket")
		if r := own.OwnershipControls.Rules; len(r) != 1 || r[0].ObjectOwnership != types.ObjectOwnershipBucketOwnerEnforced {
			t.Errorf("ownership rules = %+v, want one BucketOwnerEnforced rule", r)
		}
		pab, err := client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetPublicAccessBlock on a new bucket")
		c := pab.PublicAccessBlockConfiguration
		if !aws.ToBool(c.BlockPublicAcls) || !aws.ToBool(c.BlockPublicPolicy) || !aws.ToBool(c.IgnorePublicAcls) || !aws.ToBool(c.RestrictPublicBuckets) {
			t.Errorf("public access block = %+v, want all four true", c)
		}
		enc, err := client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketEncryption on a new bucket")
		rules := enc.ServerSideEncryptionConfiguration.Rules
		if len(rules) != 1 || rules[0].ApplyServerSideEncryptionByDefault == nil ||
			rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm != types.ServerSideEncryptionAes256 || aws.ToBool(rules[0].BucketKeyEnabled) {
			t.Errorf("encryption rules = %+v, want one AES256 rule with BucketKeyEnabled false", rules)
		}
		ver, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketVersioning on a new bucket")
		if ver.Status != "" {
			t.Errorf("GetBucketVersioning Status = %q, want none", ver.Status)
		}
		acc, err := client.GetBucketAccelerateConfiguration(ctx, &s3.GetBucketAccelerateConfigurationInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketAccelerateConfiguration on a new bucket")
		if acc.Status != "" {
			t.Errorf("GetBucketAccelerateConfiguration Status = %q, want none", acc.Status)
		}
		logging, err := client.GetBucketLogging(ctx, &s3.GetBucketLoggingInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketLogging on a new bucket")
		if logging.LoggingEnabled != nil {
			t.Errorf("GetBucketLogging LoggingEnabled = %+v, want none", logging.LoggingEnabled)
		}
		notif, err := client.GetBucketNotificationConfiguration(ctx, &s3.GetBucketNotificationConfigurationInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketNotificationConfiguration on a new bucket")
		if len(notif.QueueConfigurations)+len(notif.TopicConfigurations)+len(notif.LambdaFunctionConfigurations) != 0 {
			t.Errorf("notification configuration of a new bucket is not empty: %+v", notif)
		}
		acl, err := client.GetBucketAcl(ctx, &s3.GetBucketAclInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketAcl on a new bucket")
		if len(acl.Grants) != 1 || acl.Grants[0].Permission != types.PermissionFullControl || acl.Owner == nil ||
			acl.Grants[0].Grantee == nil || aws.ToString(acl.Grants[0].Grantee.ID) != aws.ToString(acl.Owner.ID) {
			t.Errorf("GetBucketAcl = owner %+v grants %+v, want one FULL_CONTROL grant to the owner", acl.Owner, acl.Grants)
		}
	})

	t.Run("not-configured subresources", func(t *testing.T) {
		b := aws.String(bucket)
		cases := []struct {
			op   string
			code string
			call func() error
		}{
			{"GetBucketPolicy", "NoSuchBucketPolicy", func() error { _, err := client.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: b}); return err }},
			{"GetBucketTagging", "NoSuchTagSet", func() error { _, err := client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: b}); return err }},
			{"GetBucketCors", "NoSuchCORSConfiguration", func() error { _, err := client.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: b}); return err }},
			{"GetBucketWebsite", "NoSuchWebsiteConfiguration", func() error { _, err := client.GetBucketWebsite(ctx, &s3.GetBucketWebsiteInput{Bucket: b}); return err }},
			{"GetBucketLifecycleConfiguration", "NoSuchLifecycleConfiguration", func() error {
				_, err := client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: b})
				return err
			}},
			{"GetBucketReplication", "ReplicationConfigurationNotFoundError", func() error {
				_, err := client.GetBucketReplication(ctx, &s3.GetBucketReplicationInput{Bucket: b})
				return err
			}},
			{"GetObjectLockConfiguration", "ObjectLockConfigurationNotFoundError", func() error {
				_, err := client.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: b})
				return err
			}},
		}
		for _, c := range cases {
			err := c.call()
			apiErr(t, err, c.op+" on a new bucket", c.code)
			wantStatus(t, err, c.op, 404)
		}
	})

	t.Run("tagging", func(t *testing.T) {
		out, err := client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{Bucket: aws.String(bucket), Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}}}})
		ok(t, err, "PutBucketTagging")
		if s := httpStatus(t, out.ResultMetadata); s != 204 {
			t.Errorf("PutBucketTagging status = %d, want 204 (recorded against AWS, s3.md section 4)", s)
		}
		got, err := client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketTagging")
		if len(got.TagSet) != 1 || aws.ToString(got.TagSet[0].Key) != "team" || aws.ToString(got.TagSet[0].Value) != "platform" {
			t.Errorf("GetBucketTagging = %+v, want team=platform", got.TagSet)
		}
		_, err = client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{Bucket: aws.String(bucket), Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("aws:reserved"), Value: aws.String("x")}}}})
		apiErr(t, err, "PutBucketTagging with an aws: key", "InvalidTag")
		_, err = client.DeleteBucketTagging(ctx, &s3.DeleteBucketTaggingInput{Bucket: aws.String(bucket)})
		ok(t, err, "DeleteBucketTagging")
		_, err = client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(bucket)})
		apiErr(t, err, "GetBucketTagging after delete", "NoSuchTagSet")
	})

	t.Run("policy", func(t *testing.T) {
		policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + account(t) + `:root"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::` + bucket + `/*"}]}`
		out, err := client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(bucket), Policy: aws.String(policy)})
		ok(t, err, "PutBucketPolicy")
		if s := httpStatus(t, out.ResultMetadata); s != 204 {
			t.Errorf("PutBucketPolicy status = %d, want 204", s)
		}
		got, err := client.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketPolicy")
		wantJSONEqual(t, "GetBucketPolicy Policy", aws.ToString(got.Policy), policy)
		_, err = client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(bucket), Policy: aws.String("not json")})
		apiErr(t, err, "PutBucketPolicy with invalid JSON", "MalformedPolicy")
		_, err = client.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: aws.String(bucket)})
		ok(t, err, "DeleteBucketPolicy")
		_, err = client.DeleteBucketPolicy(ctx, &s3.DeleteBucketPolicyInput{Bucket: aws.String(bucket)})
		ok(t, err, "DeleteBucketPolicy again (idempotent)")
		_, err = client.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(bucket)})
		apiErr(t, err, "GetBucketPolicy after delete", "NoSuchBucketPolicy")
	})

	t.Run("CORS", func(t *testing.T) {
		rule := types.CORSRule{AllowedMethods: []string{"GET", "PUT"}, AllowedOrigins: []string{"https://example.com"}, AllowedHeaders: []string{"*"}, MaxAgeSeconds: aws.Int32(300)}
		_, err := client.PutBucketCors(ctx, &s3.PutBucketCorsInput{Bucket: aws.String(bucket), CORSConfiguration: &types.CORSConfiguration{CORSRules: []types.CORSRule{rule}}})
		ok(t, err, "PutBucketCors")
		got, err := client.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketCors")
		if len(got.CORSRules) != 1 || !slices.Equal(got.CORSRules[0].AllowedMethods, rule.AllowedMethods) ||
			!slices.Equal(got.CORSRules[0].AllowedOrigins, rule.AllowedOrigins) || aws.ToInt32(got.CORSRules[0].MaxAgeSeconds) != 300 {
			t.Errorf("GetBucketCors = %+v, want the rule as put", got.CORSRules)
		}
		_, err = client.DeleteBucketCors(ctx, &s3.DeleteBucketCorsInput{Bucket: aws.String(bucket)})
		ok(t, err, "DeleteBucketCors")
		_, err = client.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(bucket)})
		apiErr(t, err, "GetBucketCors after delete", "NoSuchCORSConfiguration")
	})

	t.Run("lifecycle", func(t *testing.T) {
		rule := types.LifecycleRule{
			ID: aws.String("expire-tmp"), Status: types.ExpirationStatusEnabled,
			Filter:     &types.LifecycleRuleFilter{Prefix: aws.String("tmp/")},
			Expiration: &types.LifecycleExpiration{Days: aws.Int32(1)},
		}
		_, err := client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
			Bucket: aws.String(bucket), LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{rule}},
		})
		ok(t, err, "PutBucketLifecycleConfiguration")
		got, err := client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
		ok(t, err, "GetBucketLifecycleConfiguration")
		if len(got.Rules) != 1 || aws.ToString(got.Rules[0].ID) != "expire-tmp" || got.Rules[0].Filter == nil ||
			aws.ToString(got.Rules[0].Filter.Prefix) != "tmp/" || got.Rules[0].Expiration == nil || aws.ToInt32(got.Rules[0].Expiration.Days) != 1 {
			t.Errorf("GetBucketLifecycleConfiguration rules = %+v, want the rule as put", got.Rules)
		}
		if got.TransitionDefaultMinimumObjectSize != types.TransitionDefaultMinimumObjectSizeAllStorageClasses128k {
			t.Errorf("TransitionDefaultMinimumObjectSize = %q, want all_storage_classes_128K (the default)", got.TransitionDefaultMinimumObjectSize)
		}
		_, err = client.DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String(bucket)})
		ok(t, err, "DeleteBucketLifecycle")
		_, err = client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
		apiErr(t, err, "GetBucketLifecycleConfiguration after delete", "NoSuchLifecycleConfiguration")
	})

	t.Run("public access block and ownership deletes", func(t *testing.T) {
		b := createBucket(t, client)
		_, err := client.DeletePublicAccessBlock(ctx, &s3.DeletePublicAccessBlockInput{Bucket: aws.String(b)})
		ok(t, err, "DeletePublicAccessBlock")
		_, err = client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(b)})
		apiErr(t, err, "GetPublicAccessBlock after delete", "NoSuchPublicAccessBlockConfiguration")
		_, err = client.DeleteBucketOwnershipControls(ctx, &s3.DeleteBucketOwnershipControlsInput{Bucket: aws.String(b)})
		ok(t, err, "DeleteBucketOwnershipControls")
		_, err = client.GetBucketOwnershipControls(ctx, &s3.GetBucketOwnershipControlsInput{Bucket: aws.String(b)})
		apiErr(t, err, "GetBucketOwnershipControls after delete", "OwnershipControlsNotFoundError")
		_, err = client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{Bucket: aws.String(b), PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls: aws.Bool(false), BlockPublicPolicy: aws.Bool(false), IgnorePublicAcls: aws.Bool(false), RestrictPublicBuckets: aws.Bool(false),
		}})
		ok(t, err, "PutPublicAccessBlock all false")
		pab, err := client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(b)})
		ok(t, err, "GetPublicAccessBlock")
		if c := pab.PublicAccessBlockConfiguration; aws.ToBool(c.BlockPublicAcls) || aws.ToBool(c.BlockPublicPolicy) || aws.ToBool(c.IgnorePublicAcls) || aws.ToBool(c.RestrictPublicBuckets) {
			t.Errorf("GetPublicAccessBlock = %+v, want all four false as put", c)
		}
	})
}

func TestS3Objects(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := createBucket(t, client)

	t.Run("PutObject response", func(t *testing.T) {
		out := putObject(t, client, bucket, "hello.txt", "hello")
		if got := aws.ToString(out.ETag); got != quotedMD5([]byte("hello")) {
			t.Errorf("PutObject ETag = %s, want the quoted MD5 %s", got, quotedMD5([]byte("hello")))
		}
		if got := aws.ToString(out.ChecksumCRC32); got != helloCRC32 {
			t.Errorf("PutObject ChecksumCRC32 = %q, want %q (the SDK sends CRC32 by default)", got, helloCRC32)
		}
		if out.ChecksumType != types.ChecksumTypeFullObject {
			t.Errorf("PutObject ChecksumType = %q, want FULL_OBJECT", out.ChecksumType)
		}
		if out.ServerSideEncryption != types.ServerSideEncryptionAes256 {
			t.Errorf("PutObject ServerSideEncryption = %q, want AES256 (always reported)", out.ServerSideEncryption)
		}
		if out.VersionId != nil {
			t.Errorf("PutObject VersionId = %q on an unversioned bucket, want none", aws.ToString(out.VersionId))
		}
	})

	t.Run("GetObject and HeadObject", func(t *testing.T) {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String("meta.txt"), Body: strings.NewReader("hello"),
			ContentType: aws.String("text/plain"), CacheControl: aws.String("max-age=60"), Metadata: map[string]string{"Owner": "probe"},
		})
		ok(t, err, "PutObject with metadata")
		get, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("meta.txt"), ChecksumMode: types.ChecksumModeEnabled})
		ok(t, err, "GetObject")
		body := readAll(t, get.Body, "the body")
		if string(body) != "hello" || aws.ToString(get.ContentType) != "text/plain" || aws.ToString(get.CacheControl) != "max-age=60" {
			t.Errorf("GetObject = body %q, ContentType %q, CacheControl %q", body, aws.ToString(get.ContentType), aws.ToString(get.CacheControl))
		}
		if get.Metadata["owner"] != "probe" {
			t.Errorf("GetObject Metadata = %v, want owner=probe (keys lower-cased)", get.Metadata)
		}
		if aws.ToString(get.ChecksumCRC32) != helloCRC32 {
			t.Errorf("GetObject with ChecksumMode ENABLED ChecksumCRC32 = %q, want %q", aws.ToString(get.ChecksumCRC32), helloCRC32)
		}
		if aws.ToString(get.AcceptRanges) != "bytes" {
			t.Errorf("GetObject AcceptRanges = %q, want bytes", aws.ToString(get.AcceptRanges))
		}
		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("meta.txt")})
		ok(t, err, "HeadObject")
		if aws.ToInt64(head.ContentLength) != 5 || aws.ToString(head.ETag) != quotedMD5([]byte("hello")) || head.LastModified == nil {
			t.Errorf("HeadObject = ContentLength %d, ETag %s, LastModified %v", aws.ToInt64(head.ContentLength), aws.ToString(head.ETag), head.LastModified)
		}
		if head.StorageClass != "" {
			t.Errorf("HeadObject StorageClass = %q, want none for STANDARD", head.StorageClass)
		}
		if head.ServerSideEncryption != types.ServerSideEncryptionAes256 {
			t.Errorf("HeadObject ServerSideEncryption = %q, want AES256", head.ServerSideEncryption)
		}
		if head.ChecksumCRC32 != nil {
			t.Errorf("HeadObject without ChecksumMode returned ChecksumCRC32 %q; checksums come back only with x-amz-checksum-mode: ENABLED", aws.ToString(head.ChecksumCRC32))
		}
		if head.LastModified != nil && head.LastModified.Nanosecond() != 0 {
			t.Errorf("HeadObject LastModified %v has sub-second precision; the header carries seconds", head.LastModified)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		_, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("nope")})
		errAs[*types.NoSuchKey](t, err, "GetObject missing key")
		wantStatus(t, err, "GetObject missing key", 404)
		_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("nope")})
		errAs[*types.NotFound](t, err, "HeadObject missing key")
		wantStatus(t, err, "HeadObject missing key", 404)
		_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("Hello.txt")})
		errAs[*types.NoSuchKey](t, err, "GetObject with a differently cased key (keys are case sensitive)")
	})

	t.Run("ranges", func(t *testing.T) {
		putObject(t, client, bucket, "range.txt", "hello")
		out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("range.txt"), Range: aws.String("bytes=1-3")})
		ok(t, err, "GetObject Range bytes=1-3")
		b := readAll(t, out.Body, "the ranged body")
		if string(b) != "ell" || aws.ToString(out.ContentRange) != "bytes 1-3/5" || aws.ToInt64(out.ContentLength) != 3 {
			t.Errorf("ranged GetObject = body %q, ContentRange %q, ContentLength %d; want ell, bytes 1-3/5, 3", b, aws.ToString(out.ContentRange), aws.ToInt64(out.ContentLength))
		}
		if s := httpStatus(t, out.ResultMetadata); s != 206 {
			t.Errorf("ranged GetObject status = %d, want 206", s)
		}
		out, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("range.txt"), Range: aws.String("bytes=-2")})
		ok(t, err, "GetObject Range bytes=-2")
		b = readAll(t, out.Body, "the suffix-range body")
		if string(b) != "lo" || aws.ToString(out.ContentRange) != "bytes 3-4/5" {
			t.Errorf("suffix range = body %q, ContentRange %q; want lo, bytes 3-4/5", b, aws.ToString(out.ContentRange))
		}
		_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("range.txt"), Range: aws.String("bytes=10-20")})
		apiErr(t, err, "GetObject Range beyond the object", "InvalidRange")
		wantStatus(t, err, "GetObject Range beyond the object", 416)
	})

	t.Run("conditional reads", func(t *testing.T) {
		put := putObject(t, client, bucket, "cond.txt", "hello")
		etag := aws.ToString(put.ETag)
		_, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("cond.txt"), IfNoneMatch: aws.String(etag)})
		wantStatus(t, err, "GetObject If-None-Match equal to the ETag", 304)
		_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("cond.txt"), IfMatch: aws.String(`"0123456789abcdef0123456789abcdef"`)})
		apiErr(t, err, "GetObject If-Match not equal", "PreconditionFailed")
		wantStatus(t, err, "GetObject If-Match not equal", 412)
		out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("cond.txt"), IfMatch: aws.String(etag)})
		ok(t, err, "GetObject If-Match equal")
		readAll(t, out.Body, "the body")
		_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("cond.txt"), IfModifiedSince: aws.Time(time.Now().Add(time.Hour))})
		ok(t, err, "HeadObject If-Modified-Since in the future (ignored, 200)")
		_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("cond.txt"), IfUnmodifiedSince: aws.Time(time.Now().Add(-24 * time.Hour))})
		wantStatus(t, err, "HeadObject If-Unmodified-Since before Last-Modified", 412)
	})

	t.Run("conditional writes", func(t *testing.T) {
		put := putObject(t, client, bucket, "cw.txt", "one")
		_, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("cw.txt"), Body: strings.NewReader("two"), IfNoneMatch: aws.String("*")})
		apiErr(t, err, "PutObject If-None-Match * on an existing key", "PreconditionFailed")
		wantStatus(t, err, "PutObject If-None-Match *", 412)
		_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("cw-new.txt"), Body: strings.NewReader("new"), IfNoneMatch: aws.String("*")})
		ok(t, err, "PutObject If-None-Match * on a new key")
		_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("cw.txt"), Body: strings.NewReader("two"), IfMatch: aws.String(`"0123456789abcdef0123456789abcdef"`)})
		apiErr(t, err, "PutObject If-Match with another ETag", "PreconditionFailed")
		_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("cw-missing.txt"), Body: strings.NewReader("x"), IfMatch: put.ETag})
		apiErr(t, err, "PutObject If-Match on a missing key", "NoSuchKey")
		wantStatus(t, err, "PutObject If-Match on a missing key", 404)
		_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("cw.txt"), Body: strings.NewReader("two"), IfMatch: put.ETag})
		ok(t, err, "PutObject If-Match equal")
		if got := getBody(t, client, bucket, "cw.txt"); got != "two" {
			t.Errorf("after a matching conditional write the body is %q, want two", got)
		}
		_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("cw.txt"), Body: strings.NewReader("three"), IfNoneMatch: aws.String(`"abc"`)})
		apiErr(t, err, "PutObject If-None-Match with an ETag value", "NotImplemented")
		wantStatus(t, err, "PutObject If-None-Match with an ETag value", 501)
	})

	t.Run("checksum validation", func(t *testing.T) {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("bad.txt"), Body: strings.NewReader("hello"), ChecksumCRC32: aws.String("AAAAAA==")})
		apiErr(t, err, "PutObject with a wrong x-amz-checksum-crc32", "BadDigest")
		wantStatus(t, err, "PutObject with a wrong checksum", 400)
		_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("bad.txt"), Body: strings.NewReader("hello"), ContentMD5: aws.String("MTIz")})
		apiErr(t, err, "PutObject with an invalid Content-MD5", "InvalidDigest")
		sum := md5.Sum([]byte("other"))
		_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("bad.txt"), Body: strings.NewReader("hello"), ContentMD5: aws.String(base64.StdEncoding.EncodeToString(sum[:]))})
		apiErr(t, err, "PutObject with a Content-MD5 of other bytes", "BadDigest")
		_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("bad.txt")})
		errAs[*types.NotFound](t, err, "HeadObject after rejected writes (nothing stored)")
		out, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("sha.txt"), Body: strings.NewReader("hello"), ChecksumAlgorithm: types.ChecksumAlgorithmSha256})
		ok(t, err, "PutObject with ChecksumAlgorithm SHA256")
		want := "LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ="
		if aws.ToString(out.ChecksumSHA256) != want {
			t.Errorf("PutObject ChecksumSHA256 = %q, want %q", aws.ToString(out.ChecksumSHA256), want)
		}
	})

	t.Run("CopyObject", func(t *testing.T) {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String("src.txt"), Body: strings.NewReader("copy me"),
			ContentType: aws.String("text/plain"), Metadata: map[string]string{"a": "1"}, Tagging: aws.String("k=v"),
		})
		ok(t, err, "PutObject source")
		out, err := client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(bucket), Key: aws.String("dst.txt"), CopySource: aws.String(bucket + "/src.txt")})
		ok(t, err, "CopyObject")
		if out.CopyObjectResult == nil || aws.ToString(out.CopyObjectResult.ETag) != quotedMD5([]byte("copy me")) {
			t.Errorf("CopyObject result = %+v, want the content MD5 ETag", out.CopyObjectResult)
		}
		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("dst.txt")})
		ok(t, err, "HeadObject copy")
		if aws.ToString(head.ContentType) != "text/plain" || head.Metadata["a"] != "1" || aws.ToInt32(head.TagCount) != 1 {
			t.Errorf("copy (directive COPY) = ContentType %q, Metadata %v, TagCount %d; want the source's", aws.ToString(head.ContentType), head.Metadata, aws.ToInt32(head.TagCount))
		}
		_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String(bucket), Key: aws.String("dst2.txt"), CopySource: aws.String(bucket + "/src.txt"),
			MetadataDirective: types.MetadataDirectiveReplace, Metadata: map[string]string{"b": "2"},
		})
		ok(t, err, "CopyObject REPLACE")
		head, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("dst2.txt")})
		ok(t, err, "HeadObject REPLACE copy")
		if head.Metadata["a"] != "" || head.Metadata["b"] != "2" || aws.ToString(head.ContentType) != "binary/octet-stream" {
			t.Errorf("copy (directive REPLACE) = Metadata %v, ContentType %q; want only b=2 and binary/octet-stream", head.Metadata, aws.ToString(head.ContentType))
		}
		_, err = client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(bucket), Key: aws.String("src.txt"), CopySource: aws.String(bucket + "/src.txt")})
		apiErr(t, err, "CopyObject onto itself without changes", "InvalidRequest")
		wantStatus(t, err, "CopyObject onto itself", 400)
		_, err = client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(bucket), Key: aws.String("x"), CopySource: aws.String(bucket + "/no-such-key")})
		apiErr(t, err, "CopyObject from a missing key", "NoSuchKey")
		_, err = client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(bucket), Key: aws.String("x"), CopySource: aws.String(bucket + "/src.txt"), CopySourceIfNoneMatch: out.CopyObjectResult.ETag})
		apiErr(t, err, "CopyObject x-amz-copy-source-if-none-match equal", "PreconditionFailed")
		wantStatus(t, err, "CopyObject copy-source-if-none-match", 412)
	})

	t.Run("object tagging", func(t *testing.T) {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("tagged"), Body: strings.NewReader("x"), Tagging: aws.String("k1=v1&k2=v2")})
		ok(t, err, "PutObject with tags")
		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("tagged")})
		ok(t, err, "HeadObject")
		if aws.ToInt32(head.TagCount) != 2 {
			t.Errorf("HeadObject TagCount = %d, want 2", aws.ToInt32(head.TagCount))
		}
		_, err = client.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("tagged"), Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("only"), Value: aws.String("one")}}}})
		ok(t, err, "PutObjectTagging")
		got, err := client.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("tagged")})
		ok(t, err, "GetObjectTagging")
		if len(got.TagSet) != 1 || aws.ToString(got.TagSet[0].Key) != "only" {
			t.Errorf("GetObjectTagging = %+v, want only=one", got.TagSet)
		}
		del, err := client.DeleteObjectTagging(ctx, &s3.DeleteObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("tagged")})
		ok(t, err, "DeleteObjectTagging")
		if s := httpStatus(t, del.ResultMetadata); s != 204 {
			t.Errorf("DeleteObjectTagging status = %d, want 204", s)
		}
		got, err = client.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("tagged")})
		ok(t, err, "GetObjectTagging after delete")
		if len(got.TagSet) != 0 {
			t.Errorf("GetObjectTagging after delete = %+v, want empty", got.TagSet)
		}
		_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("retag"), Body: strings.NewReader("x"), Tagging: aws.String("a=b")})
		ok(t, err, "PutObject tagged")
		putObject(t, client, bucket, "retag", "y")
		got, err = client.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("retag")})
		ok(t, err, "GetObjectTagging after overwrite")
		if len(got.TagSet) != 0 {
			t.Errorf("tags after an untagged overwrite = %+v, want none (a PUT replaces tags)", got.TagSet)
		}
		_, err = client.GetObjectTagging(ctx, &s3.GetObjectTaggingInput{Bucket: aws.String(bucket), Key: aws.String("missing")})
		apiErr(t, err, "GetObjectTagging missing key", "NoSuchKey")
		_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("dup"), Body: strings.NewReader("x"), Tagging: aws.String("a=1&a=2")})
		apiErr(t, err, "PutObject x-amz-tagging with a repeated key", "InvalidArgument")
	})

	t.Run("deletes", func(t *testing.T) {
		putObject(t, client, bucket, "del1", "x")
		out, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("del1")})
		ok(t, err, "DeleteObject")
		if s := httpStatus(t, out.ResultMetadata); s != 204 {
			t.Errorf("DeleteObject status = %d, want 204", s)
		}
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("del1")})
		ok(t, err, "DeleteObject of a missing key (204)")
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("del1"), VersionId: aws.String("notnull")})
		apiErr(t, err, "DeleteObject with a version id on an unversioned bucket", "InvalidArgument")

		putObject(t, client, bucket, "batch1", "x")
		putObject(t, client, bucket, "batch2", "x")
		multi, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: &types.Delete{Objects: []types.ObjectIdentifier{
			{Key: aws.String("batch1")}, {Key: aws.String("batch2")}, {Key: aws.String("never-existed")},
		}}})
		ok(t, err, "DeleteObjects")
		var deleted []string
		for _, d := range multi.Deleted {
			deleted = append(deleted, aws.ToString(d.Key))
		}
		slices.Sort(deleted)
		if !slices.Equal(deleted, []string{"batch1", "batch2", "never-existed"}) || len(multi.Errors) != 0 {
			t.Errorf("DeleteObjects Deleted = %v, Errors %+v; want all three keys deleted (missing keys count as deleted)", deleted, multi.Errors)
		}
		quiet, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: &types.Delete{Quiet: aws.Bool(true), Objects: []types.ObjectIdentifier{{Key: aws.String("batch1")}}}})
		ok(t, err, "DeleteObjects Quiet")
		if len(quiet.Deleted) != 0 {
			t.Errorf("DeleteObjects Quiet Deleted = %+v, want none", quiet.Deleted)
		}
	})
}

func TestS3Listing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := createBucket(t, client)
	keys := []string{"a/1", "a/2", "a/b/3", "c", "test_0", "test_1", "test_10", "test_2"}
	for _, k := range keys {
		putObject(t, client, bucket, k, "x")
	}

	t.Run("ListObjectsV2 order and fields", func(t *testing.T) {
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		ok(t, err, "ListObjectsV2")
		var got []string
		for _, o := range out.Contents {
			got = append(got, aws.ToString(o.Key))
			if o.StorageClass != types.ObjectStorageClassStandard || aws.ToInt64(o.Size) != 1 || aws.ToString(o.ETag) != quotedMD5([]byte("x")) {
				t.Errorf("entry %s = StorageClass %q, Size %d, ETag %s", aws.ToString(o.Key), o.StorageClass, aws.ToInt64(o.Size), aws.ToString(o.ETag))
			}
		}
		if !slices.Equal(got, keys) {
			t.Errorf("ListObjectsV2 keys = %v, want byte order %v", got, keys)
		}
		if aws.ToInt32(out.KeyCount) != int32(len(keys)) || aws.ToBool(out.IsTruncated) {
			t.Errorf("KeyCount %d, IsTruncated %v; want %d, false", aws.ToInt32(out.KeyCount), aws.ToBool(out.IsTruncated), len(keys))
		}
	})

	t.Run("prefix and delimiter", func(t *testing.T) {
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String("a/"), Delimiter: aws.String("/")})
		ok(t, err, "ListObjectsV2 prefix a/ delimiter /")
		var got, prefixes []string
		for _, o := range out.Contents {
			got = append(got, aws.ToString(o.Key))
		}
		for _, p := range out.CommonPrefixes {
			prefixes = append(prefixes, aws.ToString(p.Prefix))
		}
		if !slices.Equal(got, []string{"a/1", "a/2"}) || !slices.Equal(prefixes, []string{"a/b/"}) {
			t.Errorf("Contents %v, CommonPrefixes %v; want [a/1 a/2] and [a/b/]", got, prefixes)
		}
	})

	t.Run("pagination", func(t *testing.T) {
		var got []string
		pages := 0
		p := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), MaxKeys: aws.Int32(3)})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			ok(t, err, "ListObjectsV2 page")
			pages++
			for _, o := range page.Contents {
				got = append(got, aws.ToString(o.Key))
			}
		}
		if !slices.Equal(got, keys) || pages != 3 {
			t.Errorf("paginated keys = %v in %d pages, want %v in 3", got, pages, keys)
		}
		_, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), ContinuationToken: aws.String("made-up-token")})
		apiErr(t, err, "ListObjectsV2 with an invalid continuation token", "InvalidArgument")
	})

	t.Run("ListObjects v1", func(t *testing.T) {
		out, err := client.ListObjects(ctx, &s3.ListObjectsInput{Bucket: aws.String(bucket), MaxKeys: aws.Int32(2)})
		ok(t, err, "ListObjects")
		if !aws.ToBool(out.IsTruncated) || len(out.Contents) != 2 || out.NextMarker != nil {
			t.Errorf("ListObjects MaxKeys 2 = IsTruncated %v, %d entries, NextMarker %q; want true, 2, none (NextMarker only with a delimiter)",
				aws.ToBool(out.IsTruncated), len(out.Contents), aws.ToString(out.NextMarker))
		}
		out, err = client.ListObjects(ctx, &s3.ListObjectsInput{Bucket: aws.String(bucket), MaxKeys: aws.Int32(2), Delimiter: aws.String("/")})
		ok(t, err, "ListObjects with a delimiter")
		if aws.ToString(out.NextMarker) != "c" {
			t.Errorf("ListObjects with a delimiter NextMarker = %q, want c (the page is the common prefix a/ and the key c)", aws.ToString(out.NextMarker))
		}
		out, err = client.ListObjects(ctx, &s3.ListObjectsInput{Bucket: aws.String(bucket), Marker: aws.String("test_1")})
		ok(t, err, "ListObjects Marker")
		if len(out.Contents) != 2 || aws.ToString(out.Contents[0].Key) != "test_10" {
			t.Errorf("ListObjects after marker test_1 = %d entries, first %v", len(out.Contents), out.Contents)
		}
	})

	t.Run("ListObjectVersions on an unversioned bucket", func(t *testing.T) {
		out, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket), Prefix: aws.String("c")})
		ok(t, err, "ListObjectVersions")
		if len(out.Versions) != 1 || aws.ToString(out.Versions[0].VersionId) != "null" || !aws.ToBool(out.Versions[0].IsLatest) {
			t.Errorf("ListObjectVersions = %+v, want one version with VersionId null and IsLatest", out.Versions)
		}
	})
}

func TestS3Versioning(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := createBucket(t, client)
	_, err := client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(bucket), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}})
	ok(t, err, "PutBucketVersioning Enabled")
	ver, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
	ok(t, err, "GetBucketVersioning")
	if ver.Status != types.BucketVersioningStatusEnabled {
		t.Fatalf("GetBucketVersioning Status = %q, want Enabled", ver.Status)
	}

	v1 := putObject(t, client, bucket, "doc", "one")
	v2 := putObject(t, client, bucket, "doc", "two")
	id1, id2 := aws.ToString(v1.VersionId), aws.ToString(v2.VersionId)
	if len(id1) != 32 || len(id2) != 32 || id1 == id2 {
		t.Errorf("version ids %q, %q; want two distinct 32-character ids", id1, id2)
	}
	old, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("doc"), VersionId: aws.String(id1)})
	ok(t, err, "GetObject by version id")
	b := readAll(t, old.Body, "the old version")
	if string(b) != "one" || aws.ToString(old.VersionId) != id1 {
		t.Errorf("GetObject version %s = %q (VersionId %q), want one", id1, b, aws.ToString(old.VersionId))
	}

	del, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("doc")})
	ok(t, err, "DeleteObject without a version id")
	if !aws.ToBool(del.DeleteMarker) || aws.ToString(del.VersionId) == "" {
		t.Errorf("DeleteObject on a versioned bucket = DeleteMarker %v, VersionId %q; want a delete marker with an id", aws.ToBool(del.DeleteMarker), aws.ToString(del.VersionId))
	}
	_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("doc")})
	errAs[*types.NoSuchKey](t, err, "GetObject behind a delete marker")
	_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("doc"), VersionId: del.VersionId})
	apiErr(t, err, "GetObject addressing a delete marker by id", "MethodNotAllowed")
	wantStatus(t, err, "GetObject of a delete marker", 405)

	list, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
	ok(t, err, "ListObjectVersions")
	if len(list.Versions) != 2 || len(list.DeleteMarkers) != 1 || !aws.ToBool(list.DeleteMarkers[0].IsLatest) ||
		aws.ToString(list.Versions[0].VersionId) != id2 || aws.ToString(list.Versions[1].VersionId) != id1 {
		t.Errorf("ListObjectVersions = versions %+v, markers %+v; want [%s %s] newest first and one latest marker", list.Versions, list.DeleteMarkers, id2, id1)
	}
	_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("doc"), VersionId: aws.String("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")})
	apiErr(t, err, "GetObject with an unknown version id", "NoSuchVersion")

	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("doc"), VersionId: del.VersionId})
	ok(t, err, "DeleteObject of the delete marker")
	if got := getBody(t, client, bucket, "doc"); got != "two" {
		t.Errorf("after removing the delete marker the object reads %q, want two", got)
	}
}

func TestS3Multipart(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := createBucket(t, client)

	t.Run("transfer manager upload and download", func(t *testing.T) {
		data := randomBytes(t, 12*mib)
		tm := transfermanager.New(client, func(o *transfermanager.Options) {
			o.PartSizeBytes = 5 * mib
			o.MultipartUploadThreshold = 5 * mib
		})
		up, err := tm.UploadObject(ctx, &transfermanager.UploadObjectInput{Bucket: aws.String(bucket), Key: aws.String("big.bin"), Body: bytes.NewReader(data)})
		ok(t, err, "transfermanager UploadObject 12 MiB")
		parts := [][]byte{data[:5*mib], data[5*mib : 10*mib], data[10*mib:]}
		if aws.ToString(up.ETag) != multipartETag(parts) {
			t.Errorf("multipart ETag = %s, want %s", aws.ToString(up.ETag), multipartETag(parts))
		}
		if aws.ToString(up.ChecksumCRC32) != compositeCRC32(parts) || up.ChecksumType != tmtypes.ChecksumTypeComposite {
			t.Errorf("CompleteMultipartUpload ChecksumCRC32 %q type %q, want composite %q", aws.ToString(up.ChecksumCRC32), up.ChecksumType, compositeCRC32(parts))
		}
		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("big.bin"), ChecksumMode: types.ChecksumModeEnabled})
		ok(t, err, "HeadObject")
		if aws.ToInt64(head.ContentLength) != int64(len(data)) || aws.ToString(head.ETag) != multipartETag(parts) {
			t.Errorf("HeadObject = ContentLength %d, ETag %s", aws.ToInt64(head.ContentLength), aws.ToString(head.ETag))
		}
		if aws.ToString(head.ChecksumCRC32) != compositeCRC32(parts) || head.ChecksumType != types.ChecksumTypeComposite {
			t.Errorf("HeadObject checksum = %q (%q), want composite %q", aws.ToString(head.ChecksumCRC32), head.ChecksumType, compositeCRC32(parts))
		}
		part, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("big.bin"), PartNumber: aws.Int32(3)})
		ok(t, err, "HeadObject partNumber 3")
		if aws.ToInt32(part.PartsCount) != 3 || aws.ToInt64(part.ContentLength) != int64(len(parts[2])) {
			t.Errorf("HeadObject partNumber 3 = PartsCount %d, ContentLength %d; want 3, %d", aws.ToInt32(part.PartsCount), aws.ToInt64(part.ContentLength), len(parts[2]))
		}
		_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("big.bin"), PartNumber: aws.Int32(4)})
		wantStatus(t, err, "HeadObject partNumber beyond the part count", 416)
		down, err := tm.GetObject(ctx, &transfermanager.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("big.bin")})
		ok(t, err, "transfermanager GetObject")
		got, err := io.ReadAll(down.Body)
		ok(t, err, "reading the download")
		if !bytes.Equal(got, data) {
			t.Errorf("downloaded %d bytes that differ from the %d uploaded", len(got), len(data))
		}
	})

	t.Run("low-level lifecycle", func(t *testing.T) {
		create, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("mpu"), ContentType: aws.String("text/plain")})
		ok(t, err, "CreateMultipartUpload")
		id := create.UploadId
		if aws.ToString(create.Bucket) != bucket || aws.ToString(create.Key) != "mpu" || aws.ToString(id) == "" {
			t.Errorf("CreateMultipartUpload = Bucket %q, Key %q, UploadId %q", aws.ToString(create.Bucket), aws.ToString(create.Key), aws.ToString(id))
		}
		p1, err := client.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String("mpu"), UploadId: id, PartNumber: aws.Int32(1), Body: strings.NewReader("abc")})
		ok(t, err, "UploadPart 1")
		if aws.ToString(p1.ETag) != quotedMD5([]byte("abc")) {
			t.Errorf("UploadPart ETag = %s, want the part MD5", aws.ToString(p1.ETag))
		}
		p2, err := client.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String("mpu"), UploadId: id, PartNumber: aws.Int32(2), Body: strings.NewReader("def")})
		ok(t, err, "UploadPart 2")
		_, err = client.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String("mpu"), UploadId: id, PartNumber: aws.Int32(10001), Body: strings.NewReader("x")})
		apiErr(t, err, "UploadPart partNumber 10001", "InvalidArgument")

		parts, err := client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String("mpu"), UploadId: id})
		ok(t, err, "ListParts")
		if len(parts.Parts) != 2 || aws.ToInt32(parts.Parts[0].PartNumber) != 1 || aws.ToInt64(parts.Parts[1].Size) != 3 {
			t.Errorf("ListParts = %+v, want parts 1 and 2 of 3 bytes", parts.Parts)
		}
		uploads, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket)})
		ok(t, err, "ListMultipartUploads")
		if !slices.ContainsFunc(uploads.Uploads, func(u types.MultipartUpload) bool { return aws.ToString(u.UploadId) == aws.ToString(id) }) {
			t.Errorf("ListMultipartUploads does not list the open upload: %+v", uploads.Uploads)
		}
		listed, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String("mpu")})
		ok(t, err, "ListObjectsV2")
		if len(listed.Contents) != 0 {
			t.Errorf("an open multipart upload appears in ListObjectsV2: %+v", listed.Contents)
		}

		complete := func(ps ...types.CompletedPart) error {
			_, err := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("mpu"), UploadId: id, MultipartUpload: &types.CompletedMultipartUpload{Parts: ps}})
			return err
		}
		part1 := types.CompletedPart{PartNumber: aws.Int32(1), ETag: p1.ETag}
		part2 := types.CompletedPart{PartNumber: aws.Int32(2), ETag: p2.ETag}
		err = complete(part1, part2)
		apiErr(t, err, "CompleteMultipartUpload with a 3-byte non-final part", "EntityTooSmall")
		err = complete(part2, part1)
		apiErr(t, err, "CompleteMultipartUpload with parts out of order", "InvalidPartOrder")
		err = complete(types.CompletedPart{PartNumber: aws.Int32(1), ETag: aws.String(`"0123456789abcdef0123456789abcdef"`)})
		apiErr(t, err, "CompleteMultipartUpload with a wrong ETag", "InvalidPart")
		err = complete(part2)
		ok(t, err, "CompleteMultipartUpload with only part 2 (a single last part may be small)")
		if got := getBody(t, client, bucket, "mpu"); got != "def" {
			t.Errorf("completed object = %q, want def (unlisted parts are discarded)", got)
		}
		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("mpu")})
		ok(t, err, "HeadObject")
		if aws.ToString(head.ETag) != multipartETag([][]byte{[]byte("def")}) || aws.ToString(head.ContentType) != "text/plain" {
			t.Errorf("completed object = ETag %s, ContentType %q; want %s and the type given at create", aws.ToString(head.ETag), aws.ToString(head.ContentType), multipartETag([][]byte{[]byte("def")}))
		}
		_, err = client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String("mpu"), UploadId: id})
		apiErr(t, err, "ListParts after completion", "NoSuchUpload")
	})

	t.Run("abort", func(t *testing.T) {
		create, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("aborted")})
		ok(t, err, "CreateMultipartUpload")
		out, err := client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("aborted"), UploadId: create.UploadId})
		ok(t, err, "AbortMultipartUpload")
		if s := httpStatus(t, out.ResultMetadata); s != 204 {
			t.Errorf("AbortMultipartUpload status = %d, want 204", s)
		}
		_, err = client.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String("aborted"), UploadId: create.UploadId, PartNumber: aws.Int32(1), Body: strings.NewReader("x")})
		apiErr(t, err, "UploadPart after abort", "NoSuchUpload")
		_, err = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("aborted"), UploadId: aws.String("no-such-upload")})
		errAs[*types.NoSuchUpload](t, err, "AbortMultipartUpload of an unknown id")
		wantStatus(t, err, "AbortMultipartUpload of an unknown id", 404)
	})
}

// TestS3TrailerChecksums drives the TLS listener, where the Go SDK sends
// request checksums as an aws-chunked body with an unsigned trailer
// (s3.md 6.3).
func TestS3TrailerChecksums(t *testing.T) {
	t.Parallel()
	if tlsEndpoint == nil {
		t.Skip(tlsEndpointEnv + " is not set; run.sh sets it to understudy's TLS listener")
	}
	ctx := t.Context()
	rec := newRecordingClient()
	client := s3Client(func(o *s3.Options) {
		o.BaseEndpoint = aws.String(tlsEndpoint.String())
		o.HTTPClient = rec
	})
	bucket := createBucket(t, client)

	t.Run("PutObject", func(t *testing.T) {
		body := []byte(strings.Repeat("streamed-body-", 10000))
		out, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("trailer"), Body: bytes.NewReader(body)})
		ok(t, err, "PutObject over HTTPS")
		req := rec.last(t, http.MethodPut)
		if req.Header.Get("X-Amz-Content-Sha256") != "STREAMING-UNSIGNED-PAYLOAD-TRAILER" || req.Header.Get("X-Amz-Trailer") != "x-amz-checksum-crc32" {
			t.Fatalf("precondition: the SDK did not send a trailer checksum (x-amz-content-sha256 %q, x-amz-trailer %q)",
				req.Header.Get("X-Amz-Content-Sha256"), req.Header.Get("X-Amz-Trailer"))
		}
		if aws.ToString(out.ChecksumCRC32) != crc32B64(body) || aws.ToString(out.ETag) != quotedMD5(body) {
			t.Errorf("PutObject = ChecksumCRC32 %q, ETag %s; want %q and the MD5 of the decoded body", aws.ToString(out.ChecksumCRC32), aws.ToString(out.ETag), crc32B64(body))
		}
		get, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("trailer"), ChecksumMode: types.ChecksumModeEnabled})
		ok(t, err, "GetObject")
		got := readAll(t, get.Body, "the body (the SDK validates the CRC32)")
		if !bytes.Equal(got, body) || aws.ToInt64(get.ContentLength) != int64(len(body)) {
			t.Errorf("stored object is %d bytes (ContentLength %d), want the %d decoded bytes", len(got), aws.ToInt64(get.ContentLength), len(body))
		}
		if strings.Contains(aws.ToString(get.ContentEncoding), "aws-chunked") {
			t.Errorf("stored Content-Encoding %q still names aws-chunked", aws.ToString(get.ContentEncoding))
		}
	})

	t.Run("multipart", func(t *testing.T) {
		data := randomBytes(t, 6*mib)
		tm := transfermanager.New(client, func(o *transfermanager.Options) {
			o.PartSizeBytes = 5 * mib
			o.MultipartUploadThreshold = 5 * mib
		})
		up, err := tm.UploadObject(ctx, &transfermanager.UploadObjectInput{Bucket: aws.String(bucket), Key: aws.String("mpu"), Body: bytes.NewReader(data)})
		ok(t, err, "transfermanager UploadObject over HTTPS")
		parts := [][]byte{data[:5*mib], data[5*mib:]}
		if aws.ToString(up.ChecksumCRC32) != compositeCRC32(parts) || aws.ToString(up.ETag) != multipartETag(parts) {
			t.Errorf("Complete = ChecksumCRC32 %q, ETag %s; want %q, %s", aws.ToString(up.ChecksumCRC32), aws.ToString(up.ETag), compositeCRC32(parts), multipartETag(parts))
		}
	})

	t.Run("wrong trailer value", func(t *testing.T) {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("bad"), Body: strings.NewReader("hello"), ChecksumCRC32: aws.String("AAAAAA==")})
		apiErr(t, err, "PutObject over HTTPS with a wrong CRC32", "BadDigest")
	})
}

func TestS3Addressing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("virtual-hosted", func(t *testing.T) {
		rec := newRecordingClient()
		client := s3Client(func(o *s3.Options) {
			o.BaseEndpoint = aws.String(localhostURL(endpoint))
			o.HTTPClient = rec
		})
		bucket := createBucket(t, client)
		putObject(t, client, bucket, "dir/key with space.txt", "hello")
		req := rec.last(t, http.MethodPut)
		if want := bucket + ".localhost:" + endpoint.Port(); req.Host != want || !strings.HasPrefix(req.Path, "/dir/") {
			t.Fatalf("precondition: request went to Host %q path %q, want virtual-hosted %q", req.Host, req.Path, want)
		}
		if got := getBody(t, client, bucket, "dir/key with space.txt"); got != "hello" {
			t.Errorf("virtual-hosted GetObject = %q, want hello", got)
		}
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		ok(t, err, "virtual-hosted ListObjectsV2")
		if len(out.Contents) != 1 || aws.ToString(out.Contents[0].Key) != "dir/key with space.txt" || aws.ToString(out.Name) != bucket {
			t.Errorf("virtual-hosted ListObjectsV2 = Name %q, %+v", aws.ToString(out.Name), out.Contents)
		}
		pathClient := s3Client(func(o *s3.Options) { o.BaseEndpoint = aws.String(localhostURL(endpoint)); o.UsePathStyle = true })
		if got := getBody(t, pathClient, bucket, "dir/key with space.txt"); got != "hello" {
			t.Errorf("path-style read of a virtual-hosted write = %q, want hello", got)
		}
		_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(name("missing"))})
		errAs[*types.NotFound](t, err, "virtual-hosted HeadBucket of a missing bucket")
	})

	t.Run("path-style", func(t *testing.T) {
		rec := newRecordingClient()
		client := s3Client(func(o *s3.Options) {
			o.BaseEndpoint = aws.String(localhostURL(endpoint))
			o.UsePathStyle = true
			o.HTTPClient = rec
		})
		bucket := createBucket(t, client)
		putObject(t, client, bucket, "a+b/c%d", "hello")
		req := rec.last(t, http.MethodPut)
		if req.Host != "localhost:"+endpoint.Port() || !strings.HasPrefix(req.Path, "/"+bucket+"/") {
			t.Fatalf("precondition: request went to Host %q path %q, want path-style", req.Host, req.Path)
		}
		if got := getBody(t, client, bucket, "a+b/c%d"); got != "hello" {
			t.Errorf("path-style GetObject of a key with + and %% = %q, want hello", got)
		}
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		ok(t, err, "path-style ListObjectsV2")
		if len(out.Contents) != 1 || aws.ToString(out.Contents[0].Key) != "a+b/c%d" {
			t.Errorf("path-style ListObjectsV2 = %+v, want the key a+b/c%%d", out.Contents)
		}
	})
}

func TestS3PresignedURLs(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := createBucket(t, client)
	presign := s3.NewPresignClient(client)

	put, err := presign.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("presigned")}, s3.WithPresignExpires(5*time.Minute))
	ok(t, err, "PresignPutObject")
	req, err := http.NewRequestWithContext(ctx, put.Method, put.URL, strings.NewReader("hello"))
	ok(t, err, "building the presigned PUT")
	for k, v := range put.SignedHeader {
		if !strings.EqualFold(k, "host") {
			req.Header[k] = v
		}
	}
	resp, err := http.DefaultClient.Do(req)
	ok(t, err, "presigned PUT")
	respBody := readAll(t, resp.Body, "the presigned PUT response")
	if resp.StatusCode != 200 {
		t.Fatalf("presigned PUT status %d, body %s", resp.StatusCode, respBody)
	}

	get, err := presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("presigned")})
	ok(t, err, "PresignGetObject")
	resp, err = http.Get(get.URL)
	ok(t, err, "presigned GET")
	b := readAll(t, resp.Body, "the presigned GET response")
	if resp.StatusCode != 200 || string(b) != "hello" {
		t.Errorf("presigned GET = %d %q, want 200 hello", resp.StatusCode, b)
	}
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("presigned"), ChecksumMode: types.ChecksumModeEnabled})
	ok(t, err, "HeadObject of the presigned upload")
	if head.ChecksumCRC64NVME == nil || head.ChecksumType != types.ChecksumTypeFullObject {
		t.Errorf("presigned PUT without a checksum: ChecksumCRC64NVME %q, ChecksumType %q; want a server-computed CRC64NVME FULL_OBJECT",
			aws.ToString(head.ChecksumCRC64NVME), head.ChecksumType)
	}
}

func TestS3SSEKMS(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := createBucket(t, client)
	key := createKey(t, "s3 sse-kms conformance")
	keyArn := aws.ToString(key.Arn)

	_, err := client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{Bucket: aws.String(bucket), ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
		Rules: []types.ServerSideEncryptionRule{{ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{SSEAlgorithm: types.ServerSideEncryptionAwsKms, KMSMasterKeyID: key.KeyId}}},
	}})
	ok(t, err, "PutBucketEncryption aws:kms")
	enc, err := client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(bucket)})
	ok(t, err, "GetBucketEncryption")
	if d := enc.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault; d.SSEAlgorithm != types.ServerSideEncryptionAwsKms || aws.ToString(d.KMSMasterKeyID) != aws.ToString(key.KeyId) {
		t.Errorf("GetBucketEncryption default = %q %q, want aws:kms and the key id as sent", d.SSEAlgorithm, aws.ToString(d.KMSMasterKeyID))
	}

	out := putObject(t, client, bucket, "secret", "test-sse")
	if out.ServerSideEncryption != types.ServerSideEncryptionAwsKms || aws.ToString(out.SSEKMSKeyId) != keyArn {
		t.Errorf("PutObject under a KMS default = %q, key %q; want aws:kms and the full key ARN %q", out.ServerSideEncryption, aws.ToString(out.SSEKMSKeyId), keyArn)
	}
	if aws.ToString(out.ETag) == quotedMD5([]byte("test-sse")) {
		t.Errorf("SSE-KMS ETag %s is the MD5 of the content; AWS documents it is not", aws.ToString(out.ETag))
	}
	if got := getBody(t, client, bucket, "secret"); got != "test-sse" {
		t.Errorf("GetObject of an SSE-KMS object = %q", got)
	}

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("bad-key"), Body: strings.NewReader("x"),
		ServerSideEncryption: types.ServerSideEncryptionAwsKms, SSEKMSKeyId: aws.String("fake-key-id"),
	})
	apiErr(t, err, "PutObject with an invalid KMS key id", "KMS.NotFoundException")
	wantStatus(t, err, "PutObject with an invalid KMS key id", 400)

	_, err = kms.NewFromConfig(awsCfg).DisableKey(ctx, &kms.DisableKeyInput{KeyId: key.KeyId})
	ok(t, err, "kms DisableKey")
	_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("secret")})
	apiErr(t, err, "GetObject of an SSE-KMS object whose key is disabled", "KMS.DisabledException")
}

func TestS3Notifications(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := createBucket(t, client)
	queueURL := createQueue(t, name("s3-events"), nil)
	arn := queueArn(t, queueURL)

	_, err := client.PutBucketNotificationConfiguration(ctx, &s3.PutBucketNotificationConfigurationInput{Bucket: aws.String(bucket), NotificationConfiguration: &types.NotificationConfiguration{
		QueueConfigurations: []types.QueueConfiguration{{QueueArn: aws.String("not-an-arn"), Events: []types.Event{"s3:ObjectCreated:*"}}},
	}})
	apiErr(t, err, "PutBucketNotificationConfiguration with a malformed queue ARN", "InvalidArgument")

	_, err = client.PutBucketNotificationConfiguration(ctx, &s3.PutBucketNotificationConfigurationInput{Bucket: aws.String(bucket), NotificationConfiguration: &types.NotificationConfiguration{
		QueueConfigurations: []types.QueueConfiguration{{
			Id: aws.String("created"), QueueArn: aws.String(arn), Events: []types.Event{"s3:ObjectCreated:*"},
			Filter: &types.NotificationConfigurationFilter{Key: &types.S3KeyFilter{FilterRules: []types.FilterRule{{Name: types.FilterRuleNamePrefix, Value: aws.String("in/")}}}},
		}},
	}})
	ok(t, err, "PutBucketNotificationConfiguration")
	got, err := client.GetBucketNotificationConfiguration(ctx, &s3.GetBucketNotificationConfigurationInput{Bucket: aws.String(bucket)})
	ok(t, err, "GetBucketNotificationConfiguration")
	if len(got.QueueConfigurations) != 1 || aws.ToString(got.QueueConfigurations[0].QueueArn) != arn || aws.ToString(got.QueueConfigurations[0].Id) != "created" {
		t.Errorf("GetBucketNotificationConfiguration = %+v", got.QueueConfigurations)
	}

	msgs := receive(t, queueURL, 1, 20*time.Second)
	if len(msgs) == 0 {
		t.Fatalf("no s3:TestEvent arrived after the configuration was stored")
	}
	var test map[string]any
	ok(t, json.Unmarshal([]byte(aws.ToString(msgs[0].Body)), &test), "decoding the test event")
	if test["Event"] != "s3:TestEvent" || test["Bucket"] != bucket || test["Service"] != "Amazon S3" {
		t.Errorf("first message = %v, want an s3:TestEvent for %s", test, bucket)
	}

	putObject(t, client, bucket, "skip/no-match", "x")
	put := putObject(t, client, bucket, "in/a b@c.txt", "hello")
	var event struct {
		Records []struct {
			EventVersion string `json:"eventVersion"`
			EventSource  string `json:"eventSource"`
			AwsRegion    string `json:"awsRegion"`
			EventName    string `json:"eventName"`
			S3           struct {
				ConfigurationID string `json:"configurationId"`
				Bucket          struct {
					Name string `json:"name"`
					Arn  string `json:"arn"`
				} `json:"bucket"`
				Object struct {
					Key       string `json:"key"`
					Size      int    `json:"size"`
					ETag      string `json:"eTag"`
					Sequencer string `json:"sequencer"`
				} `json:"object"`
			} `json:"s3"`
		} `json:"Records"`
	}
	msgs = receive(t, queueURL, 1, 20*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("received %d messages after two PutObjects, want 1 (the prefix filter drops skip/)", len(msgs))
	}
	ok(t, json.Unmarshal([]byte(aws.ToString(msgs[0].Body)), &event), "decoding the event")
	if len(event.Records) != 1 {
		t.Fatalf("event has %d records, want 1", len(event.Records))
	}
	r := event.Records[0]
	if r.EventSource != "aws:s3" || r.EventName != "ObjectCreated:Put" || r.AwsRegion != region() || r.S3.ConfigurationID != "created" ||
		r.S3.Bucket.Name != bucket || r.S3.Bucket.Arn != "arn:aws:s3:::"+bucket {
		t.Errorf("record = %+v", r)
	}
	if r.S3.Object.Key != "in/a+b%40c.txt" || r.S3.Object.Size != 5 || r.S3.Object.ETag != strings.Trim(aws.ToString(put.ETag), `"`) || r.S3.Object.Sequencer == "" {
		t.Errorf("record object = %+v; want the URL-encoded key in/a+b%%40c.txt, size 5, the unquoted ETag and a sequencer", r.S3.Object)
	}
	if !strings.HasPrefix(r.EventVersion, "2.") {
		t.Errorf("eventVersion = %q, want major version 2", r.EventVersion)
	}
}

// TestS3BucketConfigurations covers the remaining bucket subresources
// Terraform writes and reads (s3.md section 4).
func TestS3BucketConfigurations(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := createBucket(t, client)
	b := aws.String(bucket)

	t.Run("website", func(t *testing.T) {
		_, err := client.PutBucketWebsite(ctx, &s3.PutBucketWebsiteInput{Bucket: b, WebsiteConfiguration: &types.WebsiteConfiguration{
			IndexDocument: &types.IndexDocument{Suffix: aws.String("index.html")}, ErrorDocument: &types.ErrorDocument{Key: aws.String("error.html")},
		}})
		ok(t, err, "PutBucketWebsite")
		got, err := client.GetBucketWebsite(ctx, &s3.GetBucketWebsiteInput{Bucket: b})
		ok(t, err, "GetBucketWebsite")
		if got.IndexDocument == nil || aws.ToString(got.IndexDocument.Suffix) != "index.html" || got.ErrorDocument == nil || aws.ToString(got.ErrorDocument.Key) != "error.html" {
			t.Errorf("GetBucketWebsite = index %+v, error %+v", got.IndexDocument, got.ErrorDocument)
		}
		del, err := client.DeleteBucketWebsite(ctx, &s3.DeleteBucketWebsiteInput{Bucket: b})
		ok(t, err, "DeleteBucketWebsite")
		if s := httpStatus(t, del.ResultMetadata); s != 204 {
			t.Errorf("DeleteBucketWebsite status = %d, want 204", s)
		}
		_, err = client.DeleteBucketWebsite(ctx, &s3.DeleteBucketWebsiteInput{Bucket: b})
		ok(t, err, "DeleteBucketWebsite when unset (204)")
		_, err = client.GetBucketWebsite(ctx, &s3.GetBucketWebsiteInput{Bucket: b})
		apiErr(t, err, "GetBucketWebsite after delete", "NoSuchWebsiteConfiguration")
	})

	t.Run("request payment", func(t *testing.T) {
		got, err := client.GetBucketRequestPayment(ctx, &s3.GetBucketRequestPaymentInput{Bucket: b})
		ok(t, err, "GetBucketRequestPayment")
		if got.Payer != types.PayerBucketOwner {
			t.Errorf("default Payer = %q, want BucketOwner", got.Payer)
		}
		_, err = client.PutBucketRequestPayment(ctx, &s3.PutBucketRequestPaymentInput{Bucket: b, RequestPaymentConfiguration: &types.RequestPaymentConfiguration{Payer: types.PayerRequester}})
		ok(t, err, "PutBucketRequestPayment Requester")
		got, err = client.GetBucketRequestPayment(ctx, &s3.GetBucketRequestPaymentInput{Bucket: b})
		ok(t, err, "GetBucketRequestPayment")
		if got.Payer != types.PayerRequester {
			t.Errorf("Payer = %q, want Requester", got.Payer)
		}
	})

	t.Run("accelerate and logging", func(t *testing.T) {
		_, err := client.PutBucketAccelerateConfiguration(ctx, &s3.PutBucketAccelerateConfigurationInput{Bucket: b, AccelerateConfiguration: &types.AccelerateConfiguration{Status: types.BucketAccelerateStatusEnabled}})
		ok(t, err, "PutBucketAccelerateConfiguration")
		acc, err := client.GetBucketAccelerateConfiguration(ctx, &s3.GetBucketAccelerateConfigurationInput{Bucket: b})
		ok(t, err, "GetBucketAccelerateConfiguration")
		if acc.Status != types.BucketAccelerateStatusEnabled {
			t.Errorf("accelerate Status = %q, want Enabled", acc.Status)
		}
		target := createBucket(t, client)
		_, err = client.PutBucketLogging(ctx, &s3.PutBucketLoggingInput{Bucket: b, BucketLoggingStatus: &types.BucketLoggingStatus{LoggingEnabled: &types.LoggingEnabled{TargetBucket: aws.String(target), TargetPrefix: aws.String("logs/")}}})
		ok(t, err, "PutBucketLogging")
		lg, err := client.GetBucketLogging(ctx, &s3.GetBucketLoggingInput{Bucket: b})
		ok(t, err, "GetBucketLogging")
		if lg.LoggingEnabled == nil || aws.ToString(lg.LoggingEnabled.TargetBucket) != target || aws.ToString(lg.LoggingEnabled.TargetPrefix) != "logs/" {
			t.Errorf("GetBucketLogging = %+v", lg.LoggingEnabled)
		}
		_, err = client.PutBucketLogging(ctx, &s3.PutBucketLoggingInput{Bucket: b, BucketLoggingStatus: &types.BucketLoggingStatus{}})
		ok(t, err, "PutBucketLogging empty (disables)")
		lg, err = client.GetBucketLogging(ctx, &s3.GetBucketLoggingInput{Bucket: b})
		ok(t, err, "GetBucketLogging")
		if lg.LoggingEnabled != nil {
			t.Errorf("logging after an empty put = %+v, want disabled", lg.LoggingEnabled)
		}
	})

	t.Run("encryption delete", func(t *testing.T) {
		out, err := client.DeleteBucketEncryption(ctx, &s3.DeleteBucketEncryptionInput{Bucket: b})
		ok(t, err, "DeleteBucketEncryption")
		if s := httpStatus(t, out.ResultMetadata); s != 204 {
			t.Errorf("DeleteBucketEncryption status = %d, want 204", s)
		}
		_, err = client.DeleteBucketEncryption(ctx, &s3.DeleteBucketEncryptionInput{Bucket: b})
		ok(t, err, "DeleteBucketEncryption again (idempotent)")
	})

	t.Run("ownership and ACLs", func(t *testing.T) {
		own := createBucket(t, client)
		_, err := client.PutBucketAcl(ctx, &s3.PutBucketAclInput{Bucket: aws.String(own), ACL: types.BucketCannedACLPublicRead})
		apiErr(t, err, "PutBucketAcl while ownership is BucketOwnerEnforced", "AccessControlListNotSupported")
		wantStatus(t, err, "PutBucketAcl while ownership is BucketOwnerEnforced", 400)
		_, err = client.PutBucketOwnershipControls(ctx, &s3.PutBucketOwnershipControlsInput{Bucket: aws.String(own), OwnershipControls: &types.OwnershipControls{
			Rules: []types.OwnershipControlsRule{{ObjectOwnership: types.ObjectOwnershipBucketOwnerPreferred}},
		}})
		ok(t, err, "PutBucketOwnershipControls BucketOwnerPreferred")
		got, err := client.GetBucketOwnershipControls(ctx, &s3.GetBucketOwnershipControlsInput{Bucket: aws.String(own)})
		ok(t, err, "GetBucketOwnershipControls")
		if r := got.OwnershipControls.Rules; len(r) != 1 || r[0].ObjectOwnership != types.ObjectOwnershipBucketOwnerPreferred {
			t.Errorf("ownership = %+v, want BucketOwnerPreferred", r)
		}
		_, err = client.PutBucketAcl(ctx, &s3.PutBucketAclInput{Bucket: aws.String(own), ACL: types.BucketCannedACLPrivate})
		ok(t, err, "PutBucketAcl private once ACLs are enabled")
		acl, err := client.GetBucketAcl(ctx, &s3.GetBucketAclInput{Bucket: aws.String(own)})
		ok(t, err, "GetBucketAcl")
		if len(acl.Grants) != 1 || acl.Grants[0].Permission != types.PermissionFullControl {
			t.Errorf("ACL after private = %+v, want the owner FULL_CONTROL grant", acl.Grants)
		}
	})

	t.Run("replication", func(t *testing.T) {
		src := createBucket(t, client)
		cfg := &types.ReplicationConfiguration{
			Role: aws.String("arn:aws:iam::" + account(t) + ":role/replication"),
			Rules: []types.ReplicationRule{{
				ID: aws.String("all"), Status: types.ReplicationRuleStatusEnabled, Priority: aws.Int32(1),
				Filter:                  &types.ReplicationRuleFilter{Prefix: aws.String("")},
				DeleteMarkerReplication: &types.DeleteMarkerReplication{Status: types.DeleteMarkerReplicationStatusDisabled},
				Destination:             &types.Destination{Bucket: aws.String("arn:aws:s3:::" + bucket)},
			}},
		}
		_, err := client.PutBucketReplication(ctx, &s3.PutBucketReplicationInput{Bucket: aws.String(src), ReplicationConfiguration: cfg})
		apiErr(t, err, "PutBucketReplication on an unversioned bucket", "InvalidRequest")
		wantMessage(t, err, "PutBucketReplication on an unversioned bucket", "Versioning must be 'Enabled' on the bucket to apply a replication configuration")
		_, err = client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(src), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}})
		ok(t, err, "PutBucketVersioning")
		_, err = client.PutBucketReplication(ctx, &s3.PutBucketReplicationInput{Bucket: aws.String(src), ReplicationConfiguration: cfg})
		ok(t, err, "PutBucketReplication")
		got, err := client.GetBucketReplication(ctx, &s3.GetBucketReplicationInput{Bucket: aws.String(src)})
		ok(t, err, "GetBucketReplication")
		if got.ReplicationConfiguration == nil || len(got.ReplicationConfiguration.Rules) != 1 || aws.ToString(got.ReplicationConfiguration.Rules[0].ID) != "all" {
			t.Errorf("GetBucketReplication = %+v", got.ReplicationConfiguration)
		}
		_, err = client.DeleteBucketReplication(ctx, &s3.DeleteBucketReplicationInput{Bucket: aws.String(src)})
		ok(t, err, "DeleteBucketReplication")
		_, err = client.GetBucketReplication(ctx, &s3.GetBucketReplicationInput{Bucket: aws.String(src)})
		apiErr(t, err, "GetBucketReplication after delete", "ReplicationConfigurationNotFoundError")
	})

	t.Run("object lock", func(t *testing.T) {
		lock := createBucket(t, client)
		cfg := &types.ObjectLockConfiguration{ObjectLockEnabled: types.ObjectLockEnabledEnabled}
		_, err := client.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{Bucket: aws.String(lock), ObjectLockConfiguration: cfg})
		apiErr(t, err, "PutObjectLockConfiguration on an unversioned bucket", "InvalidBucketState")
		wantStatus(t, err, "PutObjectLockConfiguration on an unversioned bucket", 409)
		_, err = client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(lock), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}})
		ok(t, err, "PutBucketVersioning")
		_, err = client.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{Bucket: aws.String(lock), ObjectLockConfiguration: cfg})
		ok(t, err, "PutObjectLockConfiguration on a versioned bucket")
		got, err := client.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(lock)})
		ok(t, err, "GetObjectLockConfiguration")
		if got.ObjectLockConfiguration == nil || got.ObjectLockConfiguration.ObjectLockEnabled != types.ObjectLockEnabledEnabled {
			t.Errorf("GetObjectLockConfiguration = %+v", got.ObjectLockConfiguration)
		}
		_, err = client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(lock), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusSuspended}})
		apiErr(t, err, "suspending versioning on a lock-enabled bucket", "InvalidBucketState")
		wantMessage(t, err, "suspending versioning on a lock-enabled bucket", "An Object Lock configuration is present on this bucket, so the versioning state cannot be changed.")
	})

	t.Run("metrics configurations", func(t *testing.T) {
		_, err := client.GetBucketMetricsConfiguration(ctx, &s3.GetBucketMetricsConfigurationInput{Bucket: b, Id: aws.String("nope")})
		apiErr(t, err, "GetBucketMetricsConfiguration unknown id", "NoSuchConfiguration")
		wantStatus(t, err, "GetBucketMetricsConfiguration unknown id", 404)
		wantMessage(t, err, "GetBucketMetricsConfiguration unknown id", "The specified configuration does not exist.")
		put, err := client.PutBucketMetricsConfiguration(ctx, &s3.PutBucketMetricsConfigurationInput{Bucket: b, Id: aws.String("all"), MetricsConfiguration: &types.MetricsConfiguration{Id: aws.String("all")}})
		ok(t, err, "PutBucketMetricsConfiguration")
		if s := httpStatus(t, put.ResultMetadata); s != 204 {
			t.Errorf("PutBucketMetricsConfiguration status = %d, want 204", s)
		}
		got, err := client.GetBucketMetricsConfiguration(ctx, &s3.GetBucketMetricsConfigurationInput{Bucket: b, Id: aws.String("all")})
		ok(t, err, "GetBucketMetricsConfiguration")
		if got.MetricsConfiguration == nil || aws.ToString(got.MetricsConfiguration.Id) != "all" {
			t.Errorf("GetBucketMetricsConfiguration = %+v", got.MetricsConfiguration)
		}
		list, err := client.ListBucketMetricsConfigurations(ctx, &s3.ListBucketMetricsConfigurationsInput{Bucket: b})
		ok(t, err, "ListBucketMetricsConfigurations")
		if len(list.MetricsConfigurationList) != 1 || aws.ToBool(list.IsTruncated) {
			t.Errorf("ListBucketMetricsConfigurations = %+v", list.MetricsConfigurationList)
		}
		_, err = client.DeleteBucketMetricsConfiguration(ctx, &s3.DeleteBucketMetricsConfigurationInput{Bucket: b, Id: aws.String("all")})
		ok(t, err, "DeleteBucketMetricsConfiguration")
		_, err = client.DeleteBucketMetricsConfiguration(ctx, &s3.DeleteBucketMetricsConfigurationInput{Bucket: b, Id: aws.String("all")})
		apiErr(t, err, "DeleteBucketMetricsConfiguration twice", "NoSuchConfiguration")
	})

	t.Run("inventory, analytics and intelligent-tiering configurations", func(t *testing.T) {
		_, err := client.PutBucketInventoryConfiguration(ctx, &s3.PutBucketInventoryConfigurationInput{Bucket: b, Id: aws.String("inv"), InventoryConfiguration: &types.InventoryConfiguration{
			Id: aws.String("inv"), IsEnabled: aws.Bool(true), IncludedObjectVersions: types.InventoryIncludedObjectVersionsCurrent,
			Destination: &types.InventoryDestination{S3BucketDestination: &types.InventoryS3BucketDestination{Bucket: aws.String("arn:aws:s3:::" + bucket), Format: types.InventoryFormatCsv}},
			Schedule:    &types.InventorySchedule{Frequency: types.InventoryFrequencyDaily},
		}})
		ok(t, err, "PutBucketInventoryConfiguration")
		inv, err := client.GetBucketInventoryConfiguration(ctx, &s3.GetBucketInventoryConfigurationInput{Bucket: b, Id: aws.String("inv")})
		ok(t, err, "GetBucketInventoryConfiguration")
		if inv.InventoryConfiguration == nil || inv.InventoryConfiguration.Schedule == nil || inv.InventoryConfiguration.Schedule.Frequency != types.InventoryFrequencyDaily {
			t.Errorf("GetBucketInventoryConfiguration = %+v", inv.InventoryConfiguration)
		}
		invList, err := client.ListBucketInventoryConfigurations(ctx, &s3.ListBucketInventoryConfigurationsInput{Bucket: b})
		ok(t, err, "ListBucketInventoryConfigurations")
		if len(invList.InventoryConfigurationList) != 1 {
			t.Errorf("ListBucketInventoryConfigurations = %d entries", len(invList.InventoryConfigurationList))
		}
		_, err = client.DeleteBucketInventoryConfiguration(ctx, &s3.DeleteBucketInventoryConfigurationInput{Bucket: b, Id: aws.String("inv")})
		ok(t, err, "DeleteBucketInventoryConfiguration")
		_, err = client.GetBucketInventoryConfiguration(ctx, &s3.GetBucketInventoryConfigurationInput{Bucket: b, Id: aws.String("inv")})
		wantStatus(t, err, "GetBucketInventoryConfiguration after delete", 404)

		_, err = client.PutBucketAnalyticsConfiguration(ctx, &s3.PutBucketAnalyticsConfigurationInput{Bucket: b, Id: aws.String("an"), AnalyticsConfiguration: &types.AnalyticsConfiguration{
			Id: aws.String("an"), StorageClassAnalysis: &types.StorageClassAnalysis{},
		}})
		ok(t, err, "PutBucketAnalyticsConfiguration")
		an, err := client.GetBucketAnalyticsConfiguration(ctx, &s3.GetBucketAnalyticsConfigurationInput{Bucket: b, Id: aws.String("an")})
		ok(t, err, "GetBucketAnalyticsConfiguration")
		if an.AnalyticsConfiguration == nil || aws.ToString(an.AnalyticsConfiguration.Id) != "an" {
			t.Errorf("GetBucketAnalyticsConfiguration = %+v", an.AnalyticsConfiguration)
		}
		anList, err := client.ListBucketAnalyticsConfigurations(ctx, &s3.ListBucketAnalyticsConfigurationsInput{Bucket: b})
		ok(t, err, "ListBucketAnalyticsConfigurations")
		if len(anList.AnalyticsConfigurationList) != 1 {
			t.Errorf("ListBucketAnalyticsConfigurations = %d entries", len(anList.AnalyticsConfigurationList))
		}
		_, err = client.DeleteBucketAnalyticsConfiguration(ctx, &s3.DeleteBucketAnalyticsConfigurationInput{Bucket: b, Id: aws.String("an")})
		ok(t, err, "DeleteBucketAnalyticsConfiguration")
		_, err = client.GetBucketAnalyticsConfiguration(ctx, &s3.GetBucketAnalyticsConfigurationInput{Bucket: b, Id: aws.String("an")})
		wantStatus(t, err, "GetBucketAnalyticsConfiguration after delete", 404)

		_, err = client.PutBucketIntelligentTieringConfiguration(ctx, &s3.PutBucketIntelligentTieringConfigurationInput{Bucket: b, Id: aws.String("it"), IntelligentTieringConfiguration: &types.IntelligentTieringConfiguration{
			Id: aws.String("it"), Status: types.IntelligentTieringStatusEnabled,
			Tierings: []types.Tiering{{AccessTier: types.IntelligentTieringAccessTierArchiveAccess, Days: aws.Int32(90)}},
		}})
		ok(t, err, "PutBucketIntelligentTieringConfiguration")
		it, err := client.GetBucketIntelligentTieringConfiguration(ctx, &s3.GetBucketIntelligentTieringConfigurationInput{Bucket: b, Id: aws.String("it")})
		ok(t, err, "GetBucketIntelligentTieringConfiguration")
		if it.IntelligentTieringConfiguration == nil || len(it.IntelligentTieringConfiguration.Tierings) != 1 {
			t.Errorf("GetBucketIntelligentTieringConfiguration = %+v", it.IntelligentTieringConfiguration)
		}
		itList, err := client.ListBucketIntelligentTieringConfigurations(ctx, &s3.ListBucketIntelligentTieringConfigurationsInput{Bucket: b})
		ok(t, err, "ListBucketIntelligentTieringConfigurations")
		if len(itList.IntelligentTieringConfigurationList) != 1 {
			t.Errorf("ListBucketIntelligentTieringConfigurations = %d entries", len(itList.IntelligentTieringConfigurationList))
		}
		_, err = client.DeleteBucketIntelligentTieringConfiguration(ctx, &s3.DeleteBucketIntelligentTieringConfigurationInput{Bucket: b, Id: aws.String("it")})
		ok(t, err, "DeleteBucketIntelligentTieringConfiguration")
		_, err = client.GetBucketIntelligentTieringConfiguration(ctx, &s3.GetBucketIntelligentTieringConfigurationInput{Bucket: b, Id: aws.String("it")})
		wantStatus(t, err, "GetBucketIntelligentTieringConfiguration after delete", 404)
	})
}

func TestS3UploadPartCopy(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := createBucket(t, client)
	putObject(t, client, bucket, "source", "0123456789")
	create, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("copied")})
	ok(t, err, "CreateMultipartUpload")
	part := func(rng string) (*s3.UploadPartCopyOutput, error) {
		in := &s3.UploadPartCopyInput{Bucket: aws.String(bucket), Key: aws.String("copied"), UploadId: create.UploadId, PartNumber: aws.Int32(1), CopySource: aws.String(bucket + "/source")}
		if rng != "" {
			in.CopySourceRange = aws.String(rng)
		}
		return client.UploadPartCopy(ctx, in)
	}
	for _, bad := range []string{"0-8", "bytes=1-0", "bytes=-1-"} {
		_, err := part(bad)
		apiErr(t, err, "UploadPartCopy x-amz-copy-source-range "+bad, "InvalidArgument")
	}
	out, err := part("bytes=2-6")
	ok(t, err, "UploadPartCopy bytes=2-6")
	if out.CopyPartResult == nil || aws.ToString(out.CopyPartResult.ETag) != quotedMD5([]byte("23456")) {
		t.Errorf("UploadPartCopy result = %+v, want the MD5 of the copied bytes", out.CopyPartResult)
	}
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String("copied"), UploadId: create.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{{PartNumber: aws.Int32(1), ETag: out.CopyPartResult.ETag}}},
	})
	ok(t, err, "CompleteMultipartUpload")
	if got := getBody(t, client, bucket, "copied"); got != "23456" {
		t.Errorf("object built by UploadPartCopy = %q, want 23456", got)
	}
}
