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
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is Amazon S3: a partition-wide bucket name index and one cell
// per bucket (ARCHITECTURE.md 8 rule 3).
type Service struct {
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
	return s
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
		Ref:   "docs/research/phase1/s3.md#41-state-of-a-freshly-created-bucket-and-what-unset-means",
	}
}

// crossAccountAccess is a bucket request from an account that does not own
// the bucket: phase 1 evaluates no bucket policy or ACL, so it is served.
func crossAccountAccess() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:  "s3.bucket.cross-account",
		Value: "served",
		Label: evidence.Inferred,
		Ref:   "docs/brief/phase1-brief.md#services-and-depth",
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
		Ref:   "docs/research/phase1/s3.md#32-createbucket",
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
		Ref:   "docs/research/phase1/s3.md#429-bucket-tagging",
	}
}
