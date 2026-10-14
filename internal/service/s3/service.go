package s3

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is Amazon S3: a partition-wide bucket name index and one cell
// per bucket (ARCHITECTURE.md 8 rule 3).
type Service struct {
	k *kernel.Kernel
	// blobs holds object bodies; nil until wire.go hands the service a
	// store, and object writes then answer InternalError.
	blobs   *blob.Store
	log     *slog.Logger
	opts    Options
	codec   store.Codec
	index   *store.Partitioned[indexKey, Index]
	buckets *store.Partitioned[string, BucketCell]
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// New binds the bucket, subresource and S3 Control tag operations. The
// object, multipart and notification families bind what this build
// implements of them; every other S3 operation answers 501.
func New(d kernel.ServiceDeps, o Options) *Service {
	codec := stateCodec()
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		k:       d.Kernel,
		blobs:   d.Blob,
		log:     log,
		opts:    o,
		codec:   codec,
		index:   store.NewPartitioned(d.Kernel.Gate(), codec, newIndex),
		buckets: store.NewPartitioned(d.Kernel.Gate(), codec, newBucketCell),
	}
	b := s3api.NewBinder(d.Binder)
	s.bindBuckets(b)
	s.bindSubresources(b)
	s.bindTagging(b)
	s.bindControl(b)
	s.bindObjects(b)
	s.bindMultipart(b)
	s.bindNotification(b)
	declareAccess(d.Binder)
	return s
}

// declareAccess states, per input, the secondary IAM actions of every
// bound operation whose generated list (third_party/aws-service-reference)
// holds more than one s3 action; the access record keeps the primary
// action always and a secondary only when declared here (ARCHITECTURE.md
// 17). An operation none of whose secondaries any input needs says so
// with WithoutSecondaryActions. The s3-object-lambda and s3express actions
// need no decision: they act on Object Lambda access points and directory
// buckets, which no call touches. It also names the role PutBucketReplication
// passes. It runs after the bind sites, whose bindings it extends.
func declareAccess(b *kernel.Binder) {
	// PutObject, CopyObject and CreateMultipartUpload need s3:PutObjectAcl
	// for an ACL or grant, s3:PutObjectTagging for tags,
	// s3:PutObjectLegalHold for a legal hold and s3:PutObjectRetention for a
	// retention mode or date (PutObject, Permissions).
	kernel.Declare(b, s3api.OpPutObject, kernel.WithActions(func(_ *kernel.Call, in *s3api.PutObjectInput) []registry.IAMAction {
		return objectWriteActions(objectWriteMembers{
			acl:       in.ACL != nil || anySet(in.GrantFullControl, in.GrantRead, in.GrantReadACP, in.GrantWriteACP),
			tagging:   anySet(in.Tagging),
			legalHold: in.ObjectLockLegalHoldStatus != nil,
			retention: in.ObjectLockMode != nil || in.ObjectLockRetainUntilDate != nil,
		})
	}))
	kernel.Declare(b, s3api.OpCreateMultipartUpload, kernel.WithActions(func(_ *kernel.Call, in *s3api.CreateMultipartUploadInput) []registry.IAMAction {
		return objectWriteActions(objectWriteMembers{
			acl:       in.ACL != nil || anySet(in.GrantFullControl, in.GrantRead, in.GrantReadACP, in.GrantWriteACP),
			tagging:   anySet(in.Tagging),
			legalHold: in.ObjectLockLegalHoldStatus != nil,
			retention: in.ObjectLockMode != nil || in.ObjectLockRetainUntilDate != nil,
		})
	}))
	// A copy always reads its source, s3:GetObject, or
	// s3:GetObjectVersion when the source names a version.
	kernel.Declare(b, s3api.OpCopyObject, kernel.WithActions(func(_ *kernel.Call, in *s3api.CopyObjectInput) []registry.IAMAction {
		return append(objectWriteActions(objectWriteMembers{
			acl:       in.ACL != nil || anySet(in.GrantFullControl, in.GrantRead, in.GrantReadACP, in.GrantWriteACP),
			tagging:   anySet(in.Tagging),
			legalHold: in.ObjectLockLegalHoldStatus != nil,
			retention: in.ObjectLockMode != nil || in.ObjectLockRetainUntilDate != nil,
		}), copySourceRead(in.CopySource))
	}))
	kernel.Declare(b, s3api.OpUploadPartCopy, kernel.WithActions(func(_ *kernel.Call, in *s3api.UploadPartCopyInput) []registry.IAMAction {
		return []registry.IAMAction{copySourceRead(in.CopySource)}
	}))
	// Reading a version needs s3:GetObjectVersion. Never declared:
	// s3:GetObjectLegalHold, s3:GetObjectRetention and s3:GetObjectTagging,
	// which only decide whether the response carries the object's lock
	// headers; no input member asks for them.
	kernel.Declare(b, s3api.OpGetObject, kernel.WithActions(func(_ *kernel.Call, in *s3api.GetObjectInput) []registry.IAMAction {
		return versioned(in.VersionId, s3Action("GetObjectVersion"))
	}))
	// HeadObject declares no secondary, for the same reason as GetObject's
	// lock actions. Heading a version needs s3:GetObjectVersion on AWS
	// (HeadObject, Permissions), but its generated list has no
	// s3:GetObjectVersion, so a versioned call's record lacks it; the
	// policy guide lists the gap.
	kernel.Declare(b, s3api.OpHeadObject, kernel.WithoutSecondaryActions())
	// GetObjectAttributes needs s3:GetObject and s3:GetObjectAttributes
	// without a version, s3:GetObjectVersion and
	// s3:GetObjectVersionAttributes with one (GetObjectAttributes,
	// Permissions). The primary s3:GetObjectAttributes is recorded either
	// way; s3:GetObjectVersionAttributes is not in the generated list, so a
	// versioned call's record lacks it.
	kernel.Declare(b, s3api.OpGetObjectAttributes, kernel.WithActions(func(_ *kernel.Call, in *s3api.GetObjectAttributesInput) []registry.IAMAction {
		if anySet(in.VersionId) {
			return []registry.IAMAction{s3Action("GetObjectVersion")}
		}
		return []registry.IAMAction{s3Action("GetObject")}
	}))
	kernel.Declare(b, s3api.OpGetObjectTagging, kernel.WithActions(func(_ *kernel.Call, in *s3api.GetObjectTaggingInput) []registry.IAMAction {
		return versioned(in.VersionId, s3Action("GetObjectVersionTagging"))
	}))
	kernel.Declare(b, s3api.OpPutObjectTagging, kernel.WithActions(func(_ *kernel.Call, in *s3api.PutObjectTaggingInput) []registry.IAMAction {
		return versioned(in.VersionId, s3Action("PutObjectVersionTagging"))
	}))
	kernel.Declare(b, s3api.OpDeleteObjectTagging, kernel.WithActions(func(_ *kernel.Call, in *s3api.DeleteObjectTaggingInput) []registry.IAMAction {
		return versioned(in.VersionId, s3Action("DeleteObjectVersionTagging"))
	}))
	// Deleting a version needs s3:DeleteObjectVersion, and overriding
	// governance-mode retention s3:BypassGovernanceRetention.
	kernel.Declare(b, s3api.OpDeleteObject, kernel.WithActions(func(_ *kernel.Call, in *s3api.DeleteObjectInput) []registry.IAMAction {
		return append(versioned(in.VersionId, s3Action("DeleteObjectVersion")), bypassing(in.BypassGovernanceRetention)...)
	}))
	kernel.Declare(b, s3api.OpDeleteObjects, kernel.WithActions(func(_ *kernel.Call, in *s3api.DeleteObjectsInput) []registry.IAMAction {
		var out []registry.IAMAction
		if slices.ContainsFunc(in.Delete.Objects, func(o s3api.ObjectIdentifier) bool { return anySet(o.VersionId) }) {
			out = append(out, s3Action("DeleteObjectVersion"))
		}
		return append(out, bypassing(in.BypassGovernanceRetention)...)
	}))
	// CreateBucket needs s3:PutBucketAcl for an ACL or grant,
	// s3:PutBucketObjectLockConfiguration and s3:PutBucketVersioning to
	// enable Object Lock, and s3:PutBucketOwnershipControls to set object
	// ownership (CreateBucket, Permissions).
	kernel.Declare(b, s3api.OpCreateBucket, kernel.WithActions(func(_ *kernel.Call, in *s3api.CreateBucketInput) []registry.IAMAction {
		var out []registry.IAMAction
		if in.ACL != nil || anySet(in.GrantFullControl, in.GrantRead, in.GrantReadACP, in.GrantWrite, in.GrantWriteACP) {
			out = append(out, s3Action("PutBucketAcl"))
		}
		if in.ObjectLockEnabledForBucket != nil && *in.ObjectLockEnabledForBucket {
			out = append(out, s3Action("PutBucketObjectLockConfiguration"), s3Action("PutBucketVersioning"))
		}
		if in.ObjectOwnership != nil {
			out = append(out, s3Action("PutBucketOwnershipControls"))
		}
		return out
	}))
	// ListObjects and ListObjectsV2 declare no secondary: their Permissions
	// sections name s3:ListBucket alone, and no input member selects
	// s3:GetObjectAcl.
	kernel.Declare(b, s3api.OpListObjects, kernel.WithoutSecondaryActions())
	kernel.Declare(b, s3api.OpListObjectsV2, kernel.WithoutSecondaryActions())
	// PutBucketReplication passes the replication role, on which the access
	// record keeps iam:PassRole (PutBucketReplication, Permissions).
	kernel.Declare(b, s3api.OpPutBucketReplication, kernel.WithResources(func(_ *kernel.Call, in *s3api.PutBucketReplicationInput) []aws.ARN {
		return passedRole(in.ReplicationConfiguration.Role)
	}))
}

// passedRole is the IAM role a configuration passes; nothing when the
// value is not an IAM ARN, since no role is then passed.
func passedRole(role string) []aws.ARN {
	a, err := aws.ParseARN(role)
	if err != nil || a.Service != "iam" {
		return nil
	}
	return []aws.ARN{a}
}

// objectWriteMembers are the members of an object write that need an
// action besides s3:PutObject.
type objectWriteMembers struct {
	acl, tagging, legalHold, retention bool
}

// objectWriteActions returns the secondary actions m needs.
func objectWriteActions(m objectWriteMembers) []registry.IAMAction {
	var out []registry.IAMAction
	for _, need := range []struct {
		set    bool
		action string
	}{{m.acl, "PutObjectAcl"}, {m.tagging, "PutObjectTagging"}, {m.legalHold, "PutObjectLegalHold"}, {m.retention, "PutObjectRetention"}} {
		if need.set {
			out = append(out, s3Action(need.action))
		}
	}
	return out
}

// copySourceRead is the action reading a copy source needs:
// s3:GetObjectVersion for a source naming a version, s3:GetObject
// otherwise. A source that does not parse reads as unversioned; the
// handler refuses it.
func copySourceRead(header string) registry.IAMAction {
	if src, err := parseCopySource(header); err == nil && src.versionID != nil {
		return s3Action("GetObjectVersion")
	}
	return s3Action("GetObject")
}

// versioned returns action when the input names a version.
func versioned(versionID *string, action registry.IAMAction) []registry.IAMAction {
	if anySet(versionID) {
		return []registry.IAMAction{action}
	}
	return nil
}

// bypassing returns s3:BypassGovernanceRetention when the input asks to
// bypass governance-mode retention.
func bypassing(bypass *bool) []registry.IAMAction {
	if bypass != nil && *bypass {
		return []registry.IAMAction{s3Action("BypassGovernanceRetention")}
	}
	return nil
}

// anySet reports whether any of the optional members is present and not
// empty.
func anySet(members ...*string) bool {
	return slices.ContainsFunc(members, func(m *string) bool { return m != nil && *m != "" })
}

// s3Action is the s3 action name.
func s3Action(name string) registry.IAMAction {
	return registry.IAMAction{Prefix: "s3", Name: name}
}

// BucketExists reports whether a bucket of that name exists in any account
// or region: the one state read the edge makes, to tell an unsigned Lambda
// date path from a path-style S3 request (ARCHITECTURE.md 4.1 rule 8).
func (s *Service) BucketExists(name string) bool {
	cell, ok := s.buckets.Lookup(name)
	if !ok {
		return false
	}
	return store.Read(cell, func(c *BucketCell) bool { return c.Bucket != nil })
}

// Name returns "s3".
func (*Service) Name() ref.ServiceID { return "s3" }

// SchemaVersion returns the version of Index and BucketCell.
func (*Service) SchemaVersion() int { return stateVersion }

// Export serializes the index and every bucket, one section each.
func (s *Service) Export() ([]store.Section, error) {
	idx, err := s.index.Export(indexKeys(), stateVersion)
	if err != nil {
		return nil, err
	}
	bkts, err := s.buckets.Export(bucketKeys(), stateVersion)
	if err != nil {
		return nil, err
	}
	return append(idx, bkts...), nil
}

// Import loads sections written by Export of this schema version, without
// the reserved names whose bucket the sections do not hold.
func (s *Service) Import(ss []store.Section) error {
	idx, bkts := splitSections(ss)
	idx, err := s.reconcile(idx, bkts)
	if err != nil {
		return err
	}
	if err := s.index.Import(indexKeys(), stateVersion, idx); err != nil {
		return err
	}
	return s.buckets.Import(bucketKeys(), stateVersion, bkts)
}

// splitSections separates the index section from the bucket sections; a
// path in neither layout goes with the buckets, whose codec refuses it.
func splitSections(ss []store.Section) (idx, bkts []store.Section) {
	for _, sec := range ss {
		if sec.Path == indexPath+".json" {
			idx = append(idx, sec)
			continue
		}
		bkts = append(bkts, sec)
	}
	return idx, bkts
}

// Reset drops the buckets whose home scope rs covers and releases their
// names. It runs under the gate's exclusive side, where no cell can be
// read, so it reads the index through its own section and writes the kept
// entries back through Import. A codec failure there is a bug in this
// package; Reset then logs it and leaves the S3 state as it was.
func (s *Service) Reset(rs kernel.ResetScope) {
	if err := s.reset(rs); err != nil {
		s.log.LogAttrs(context.Background(), slog.LevelError, "s3 reset could not read or rewrite the bucket index; no bucket was dropped",
			slog.String("error", err.Error()))
	}
}

// Rearm does nothing: no bucket state arms a timer.
func (*Service) Rearm(clock.Instant) {}

// Resources lists the buckets whose home is scope, by name.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	homes := s.homes()
	var out []kernel.Resource
	for _, name := range slices.Sorted(maps.Keys(homes)) {
		if h := homes[name]; h.scope() == scope {
			out = append(out, kernel.Resource{ARN: bucketARN(name), Summary: "created " + h.Created.String()})
		}
	}
	return out
}

// Scopes returns the home scopes of every bucket.
func (s *Service) Scopes() []aws.Scope {
	var out []aws.Scope
	for _, h := range s.homes() {
		if sc := h.scope(); !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	slices.SortFunc(out, func(a, b aws.Scope) int {
		return cmp.Or(cmp.Compare(a.Account.String(), b.Account.String()), cmp.Compare(a.Region.String(), b.Region.String()))
	})
	return out
}

// Switches lists the behaviors chosen without AWS-recorded evidence.
func (*Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		encryptionAfterDelete().Entry(),
		crossAccountAccess().Entry(),
		publicACLAtCreate().Entry(),
		tagLimits().Entry(),
		lifecycleRuleLimits().Entry(),
	}
}

func (s *Service) reset(rs kernel.ResetScope) error {
	sections, err := s.index.Export(indexKeys(), stateVersion)
	if err != nil {
		return err
	}
	kept := newIndex(indexKey{})
	for _, sec := range sections {
		if err := s.codec.Unmarshal(sec.Data, &kept); err != nil {
			return err
		}
	}
	dropped := map[string]bool{}
	for name, h := range kept.Buckets {
		if rs.Covers(h.scope()) {
			dropped[name] = true
		}
	}
	maps.DeleteFunc(kept.Buckets, func(name string, _ Home) bool { return dropped[name] })
	section, err := s.indexSection(kept)
	if err != nil {
		return err
	}
	s.index.Reset(func(indexKey) bool { return false })
	if err := s.index.Import(indexKeys(), stateVersion, section); err != nil {
		return err
	}
	s.buckets.Reset(func(name string) bool { return !dropped[name] })
	return nil
}

// indexSection is x as the index partition exports it: one section, or
// none for an index without buckets.
func (s *Service) indexSection(x Index) ([]store.Section, error) {
	if x.IsZero() {
		return nil, nil
	}
	data, err := s.codec.Marshal(&x)
	if err != nil {
		return nil, err
	}
	return []store.Section{{Path: indexPath + ".json", Version: stateVersion, Data: data}}, nil
}

// reconcile drops from the index sections every name no bucket section
// holds. A snapshot cut between the two writes of CreateBucket or
// DeleteBucket saves a reserved name without its bucket, since an empty
// cell is not exported; loaded as is, the name could be neither created
// nor deleted again. A section this version cannot decode is returned
// unchanged, for the index's Import to refuse with its path.
func (s *Service) reconcile(idx, bkts []store.Section) ([]store.Section, error) {
	held := map[string]bool{}
	for _, sec := range bkts {
		if name, err := bucketKeys().Decode(strings.TrimSuffix(sec.Path, ".json")); err == nil {
			held[name] = true
		}
	}
	out := make([]store.Section, 0, len(idx))
	for _, sec := range idx {
		x := newIndex(indexKey{})
		if sec.Version != stateVersion || s.codec.Unmarshal(sec.Data, &x) != nil {
			out = append(out, sec)
			continue
		}
		maps.DeleteFunc(x.Buckets, func(name string, _ Home) bool { return !held[name] })
		kept, err := s.indexSection(x)
		if err != nil {
			return nil, err
		}
		out = append(out, kept...)
	}
	return out, nil
}

// homes returns a copy of the index: every bucket's home, by name.
func (s *Service) homes() map[string]Home {
	cell, ok := s.index.Lookup(indexKey{})
	if !ok {
		return nil
	}
	return store.Read(cell, func(x *Index) map[string]Home { return maps.Clone(x.Buckets) })
}

// encryptionAfterDelete is GetBucketEncryption after DeleteBucketEncryption:
// AWS documents that every bucket always has the SSE-S3 default, and no
// recording shows the read (s3.md 4.1).
func encryptionAfterDelete() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "s3.encryption.after-delete",
		Value: "default-sse-s3",
		Label: evidence.AWSDoc,
		Ref:   "docs/research/s3.md#41-state-of-a-freshly-created-bucket-and-what-unset-means",
	}
}

// crossAccountAccess is a bucket request from an account that does not own
// the bucket: understudy evaluates no bucket policy or ACL, so it is served.
// What AWS answers is UNVERIFIED: no research note or recording covers it.
func crossAccountAccess() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "s3.bucket.cross-account",
		Value: "served",
		Label: evidence.Inferred,
		Ref:   "UNVERIFIED: no research note or recording shows AWS's answer to a bucket request from an account that does not own the bucket",
	}
}

// publicACLAtCreate is CreateBucket with a canned ACL other than private
// while ownership is BucketOwnerEnforced: AWS documents a failure, not its
// code (s3.md 3.2).
func publicACLAtCreate() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "s3.create-bucket.public-acl",
		Value: "InvalidBucketAclWithObjectOwnership",
		Label: evidence.AWSDoc,
		Ref:   "docs/research/s3.md#32-createbucket",
	}
}

// tagLimits are the 50-tag, 128-character key and 256-character value
// limits of a bucket tag set: documented, with error texts no recording
// shows (s3.md 4.2.9).
func tagLimits() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "s3.tagging.limits",
		Value: "InvalidTag",
		Label: evidence.AWSDoc,
		Ref:   "docs/research/s3.md#429-bucket-tagging",
	}
}

// lifecycleRuleLimits are the unique rule IDs, the 1,000-rule limit and the
// day limits of a lifecycle configuration's actions: documented, with
// error texts no recording shows (checkLifecycle, checkLifecycleDays).
func lifecycleRuleLimits() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "s3.lifecycle.rule-limits",
		Value: "InvalidArgument",
		Label: evidence.AWSDoc,
		Ref:   "https://docs.aws.amazon.com/AmazonS3/latest/API/API_LifecycleRule.html",
	}
}
