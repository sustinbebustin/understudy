package s3

import (
	"maps"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of Index and BucketCell in snapshot
// sections. The subresource documents are the S3 model's own shapes, so a
// model pin that changes one of them bumps it.
const stateVersion = 1

// Index is the partition-wide bucket name index (ARCHITECTURE.md 8 rule 3):
// every bucket's owner and home region, by name. Bucket names are global
// across accounts, so CreateBucket reserves a name here before it writes
// the bucket's own cell, and DeleteBucket releases it after.
type Index struct {
	Buckets map[string]Home `json:"buckets"`
}

func newIndex(indexKey) Index { return Index{Buckets: map[string]Home{}} }

// IsZero reports whether the index holds no buckets, which snapshots skip.
func (x *Index) IsZero() bool { return len(x.Buckets) == 0 }

// Home is where a bucket lives and who owns it.
type Home struct {
	OwnerAccount aws.AccountID `json:"ownerAccount"`
	Region       aws.Region    `json:"region"`
	Created      clock.Instant `json:"created"`
}

// scope is the account and region the bucket's resources belong to.
func (h Home) scope() aws.Scope {
	return aws.Scope{Partition: aws.PartitionAWS(), Account: h.OwnerAccount, Region: h.Region}
}

// indexKey is the one key of the index partition.
type indexKey struct{}

// The section paths of the two partitions: the index at "index", each
// bucket at "buckets/<name>". Bucket names hold no slash, so the layouts
// cannot collide.
const (
	indexPath     = "index"
	bucketsPrefix = "buckets/"
)

// indexKeys is the index partition's section codec.
func indexKeys() store.KeyCodec[indexKey] {
	return store.KeyCodec[indexKey]{
		Encode: func(indexKey) string { return indexPath },
		Decode: func(enc string) (indexKey, error) {
			if enc != indexPath {
				return indexKey{}, &store.KeyLayoutError{Encoded: enc, Layout: indexPath}
			}
			return indexKey{}, nil
		},
	}
}

// bucketKeys is the bucket partition's section codec.
func bucketKeys() store.KeyCodec[string] {
	return store.KeyCodec[string]{
		Encode: func(name string) string { return bucketsPrefix + name },
		Decode: func(enc string) (string, error) {
			name, ok := strings.CutPrefix(enc, bucketsPrefix)
			if !ok || name == "" || strings.Contains(name, "/") {
				return "", &store.KeyLayoutError{Encoded: enc, Layout: bucketsPrefix + "<bucket>"}
			}
			return name, nil
		},
	}
}

// BucketCell is one bucket's cell. Bucket is nil only between the two
// commits of CreateBucket (the name reserved in the index, the bucket not
// yet written) and of DeleteBucket (the bucket gone, the name not yet
// released); readers answer both as NoSuchBucket. A snapshot cut between
// them saves the name without its bucket, and Import drops that name.
type BucketCell struct {
	Bucket *Bucket `json:"bucket,omitempty"`
}

func newBucketCell(string) BucketCell { return BucketCell{} }

// IsZero reports whether the cell holds no bucket, which snapshots skip.
func (c *BucketCell) IsZero() bool { return c.Bucket == nil }

// Partitioned finds IsZero and BlobRefs by type assertions; these keep a
// rename from silently dropping the skip or the blobs.
var (
	_ store.Zeroer     = (*Index)(nil)
	_ store.Zeroer     = (*BucketCell)(nil)
	_ store.BlobHolder = (*BucketCell)(nil)
)

// Bucket is one bucket and its subresources. Records are values replaced on
// change: a write builds a new Bucket and never mutates a document a reader
// may hold, so the configuration pointers, slices and maps below are shared
// and read-only. Objects and Uploads are the exception: their maps change
// in place inside the bucket's write, and no read returns them (Objects).
// A nil document is a subresource never configured, or deleted since.
type Bucket struct {
	Name string `json:"name"`
	Home Home   `json:"home"`
	// Location is the LocationConstraint GetBucketLocation returns: empty
	// for us-east-1, EU for a bucket created with the EU alias, else the
	// region name.
	Location string `json:"location,omitempty"`
	// Grants is the access control list; GetBucketAcl reports the owner
	// beside it.
	Grants []s3api.Grant `json:"grants"`
	// Versioning is nil until the first PutBucketVersioning: the status can
	// be suspended but never returns to "never versioned".
	Versioning *s3api.VersioningConfiguration `json:"versioning,omitempty"`
	CORS       *s3api.CORSConfiguration       `json:"cors,omitempty"`
	Website    *s3api.WebsiteConfiguration    `json:"website,omitempty"`
	// Policy is the policy document as sent; empty when none is set.
	Policy  string                `json:"policy,omitempty"`
	Logging *s3api.LoggingEnabled `json:"logging,omitempty"`
	// Lifecycle is stored exactly as sent: Terraform deep-compares what it
	// reads back with what it wrote (tf-storage.md 2.6).
	Lifecycle   *Lifecycle                      `json:"lifecycle,omitempty"`
	Replication *s3api.ReplicationConfiguration `json:"replication,omitempty"`
	// Encryption is nil for the default rule every bucket has: SSE-S3 with
	// no bucket key (s3.md 4.1).
	Encryption        *s3api.ServerSideEncryptionConfiguration `json:"encryption,omitempty"`
	ObjectLock        *s3api.ObjectLockConfiguration           `json:"objectLock,omitempty"`
	RequestPayment    *s3api.RequestPaymentConfiguration       `json:"requestPayment,omitempty"`
	Accelerate        *s3api.AccelerateConfiguration           `json:"accelerate,omitempty"`
	PublicAccessBlock *s3api.PublicAccessBlockConfiguration    `json:"publicAccessBlock,omitempty"`
	Ownership         *s3api.OwnershipControls                 `json:"ownership,omitempty"`
	// Tags is the bucket's tag set, which the S3 tagging operations and the
	// S3 Control tag operations share; empty is no tag set.
	Tags         map[string]string                `json:"tags,omitempty"`
	Notification *s3api.NotificationConfiguration `json:"notification,omitempty"`
	// Sequencer is the last sequencer an event notification of the bucket
	// carried, zero before the first. One per bucket is strictly
	// increasing per key, which is all S3 promises (s3.md 13.2).
	Sequencer notify.Sequencer `json:"sequencer,omitempty"`
	// VersionSeq is the last Seq a version or an upload of the bucket took,
	// zero before the first (nextSeq).
	VersionSeq uint64 `json:"versionSeq,omitempty"`
	// The configuration families are keyed by their Id.
	Metrics            map[string]s3api.MetricsConfiguration            `json:"metrics,omitempty"`
	Analytics          map[string]s3api.AnalyticsConfiguration          `json:"analytics,omitempty"`
	Inventory          map[string]s3api.InventoryConfiguration          `json:"inventory,omitempty"`
	IntelligentTiering map[string]s3api.IntelligentTieringConfiguration `json:"intelligentTiering,omitempty"`
	Objects            Objects                                          `json:"objects"`
	Uploads            Uploads                                          `json:"uploads"`
}

// Lifecycle is the stored lifecycle configuration and the transition
// default the request carried, which GetBucketLifecycleConfiguration echoes
// as x-amz-transition-default-minimum-object-size.
type Lifecycle struct {
	Rules                              []s3api.LifecycleRule                    `json:"rules"`
	TransitionDefaultMinimumObjectSize s3api.TransitionDefaultMinimumObjectSize `json:"transitionDefaultMinimumObjectSize"`
}

// bucketARN is a bucket's ARN; S3 bucket ARNs carry no region or account.
func bucketARN(name string) aws.ARN {
	return aws.ARN{Partition: aws.PartitionAWS().String(), Service: "s3", Resource: name}
}

// stateCodec is the snapshot codec of the index and the buckets, with the
// S3 model's unions that subresource documents hold in the tagged-union
// form. A table jsonx refuses is a bug in this file; the codec then fails
// every Export and Import with that error.
func stateCodec() store.Codec {
	c, err := unionCodec()
	if err != nil {
		return store.Codec{
			Marshal:   func(any) ([]byte, error) { return nil, err },
			Unmarshal: func([]byte, any) error { return err },
		}
	}
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}

func unionCodec() (jsonx.Codec, error) {
	metrics, err := jsonx.NewUnion("s3api.MetricsFilter",
		jsonx.Variant[s3api.MetricsFilter, s3api.MetricsFilterMemberPrefix]("prefix"),
		jsonx.Variant[s3api.MetricsFilter, s3api.MetricsFilterMemberTag]("tag"),
		jsonx.Variant[s3api.MetricsFilter, s3api.MetricsFilterMemberAccessPointArn]("accessPointArn"),
		jsonx.Variant[s3api.MetricsFilter, s3api.MetricsFilterMemberAnd]("and"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	analytics, err := jsonx.NewUnion("s3api.AnalyticsFilter",
		jsonx.Variant[s3api.AnalyticsFilter, s3api.AnalyticsFilterMemberPrefix]("prefix"),
		jsonx.Variant[s3api.AnalyticsFilter, s3api.AnalyticsFilterMemberTag]("tag"),
		jsonx.Variant[s3api.AnalyticsFilter, s3api.AnalyticsFilterMemberAnd]("and"),
	)
	if err != nil {
		return jsonx.Codec{}, err
	}
	return jsonx.NewCodec(metrics, analytics), nil
}

// with returns a copy of m with key set to v; m is never changed.
func with[V any](m map[string]V, key string, v V) map[string]V {
	out := maps.Clone(m)
	if out == nil {
		out = map[string]V{}
	}
	out[key] = v
	return out
}

// without returns a copy of m without key; m is never changed.
func without[V any](m map[string]V, key string) map[string]V {
	out := maps.Clone(m)
	delete(out, key)
	return out
}
