package s3

import (
	"context"
	"net"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// access is how a bucket-addressed request names its bucket: the name and
// the owner it asserts, nil when it asserts none.
type access struct {
	name  string
	owner *ownerClaim
}

// ownerClaim is the account a request asserts owns the bucket, and the
// header that carried it, which an error about the value names.
type ownerClaim struct {
	header  ownerHeader
	account string
}

// ownerHeader is a header that asserts the bucket's owner.
type ownerHeader string

// The owner headers: S3's, and S3 Control's for the tag operations.
const (
	expectedOwnerHeader ownerHeader = "x-amz-expected-bucket-owner"
	accountIDHeader     ownerHeader = "x-amz-account-id"
)

// at builds the access of an input's Bucket and ExpectedBucketOwner.
func at(name string, owner *string) access {
	a := access{name: name}
	if owner != nil {
		a.owner = &ownerClaim{header: expectedOwnerHeader, account: *owner}
	}
	return a
}

// touches names what an input addresses as the call's resources, so fault
// rules scoped to them match before the handler runs and the access record
// pairs the call's actions with them: the objects of an object operation
// (objectsAddressed), the bucket of any other.
func touches[In any](of func(*In) access) kernel.BindOption {
	return kernel.WithResources(func(_ *kernel.Call, in *In) []aws.ARN {
		if objects, ok := objectsAddressed(in); ok {
			return objects
		}
		return []aws.ARN{bucketARN(of(in).name)}
	})
}

// objectResource is the ARN of the object key in bucket,
// arn:aws:s3:::<bucket>/<key>, which IAM authorizes object actions against
// and object operations touch, and which SSE-KMS adds to the encryption
// context.
func objectResource(bucket, key string) aws.ARN {
	return aws.ARN{Partition: aws.PartitionAWS().String(), Service: "s3", Resource: bucket + "/" + key}
}

// objectsAddressed returns the objects an object operation's input
// addresses: the key it names, both objects of a copy, each key of a
// DeleteObjects. false for a bucket operation, whose resource is the
// bucket. A copy source that does not parse adds nothing; the handler
// answers it.
func objectsAddressed(in any) ([]aws.ARN, bool) {
	switch in := in.(type) {
	case *s3api.GetObjectInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.HeadObjectInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.GetObjectAttributesInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.PutObjectInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.DeleteObjectInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.GetObjectTaggingInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.PutObjectTaggingInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.DeleteObjectTaggingInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.CreateMultipartUploadInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.UploadPartInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.CompleteMultipartUploadInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.AbortMultipartUploadInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.ListPartsInput:
		return []aws.ARN{objectResource(in.Bucket, in.Key)}, true
	case *s3api.CopyObjectInput:
		return withCopySource(objectResource(in.Bucket, in.Key), in.CopySource), true
	case *s3api.UploadPartCopyInput:
		return withCopySource(objectResource(in.Bucket, in.Key), in.CopySource), true
	case *s3api.DeleteObjectsInput:
		out := make([]aws.ARN, 0, len(in.Delete.Objects))
		for _, o := range in.Delete.Objects {
			out = append(out, objectResource(in.Bucket, o.Key))
		}
		return out, true
	}
	return nil, false
}

// withCopySource returns the destination object and the object the
// x-amz-copy-source header names, when it parses.
func withCopySource(dst aws.ARN, header string) []aws.ARN {
	src, err := parseCopySource(header)
	if err != nil {
		return []aws.ARN{dst}
	}
	return []aws.ARN{dst, objectResource(src.bucket, src.key)}
}

// regionSource is the region a call addressed to bucket asserts
// (ARCHITECTURE.md 7): the call's own source, the SigV4 credential scope or
// none for SigV2 and internal calls, except that an anonymous request to an
// s3.<region>.<base> host asserts the host's region.
func (s *Service) regionSource(c *kernel.Call, bucket string) aws.RegionSource {
	if _, anonymous := c.Principal().(identity.Anonymous); anonymous {
		if _, external := c.Origin().(ref.External); external {
			if r, ok := hostRegion(c.Endpoint().Host, bucket, s.opts.Regions); ok {
				return aws.HostRegion{Region: r}
			}
		}
	}
	return c.RegionSource()
}

// endpointRegion is the region a request is sent to: the asserted one, or
// us-east-1, the global endpoint's, when it asserts none.
func endpointRegion(src aws.RegionSource) aws.Region {
	switch r := src.(type) {
	case aws.CredentialScope:
		return r.Region
	case aws.HostRegion:
		return r.Region
	case aws.None:
	}
	return aws.DefaultRegion()
}

// hostRegion is the region of a host addressed to bucket: s3.<region>.<base>
// (path-style) or <bucket>.s3.<region>.<base> (virtual-hosted), when the
// table names the region. Only the labels after the bucket's are read, so a
// dotted bucket name holding s3.<region> asserts nothing.
func hostRegion(host, bucket string, regions aws.Regions) (aws.Region, bool) {
	name := hostname(host)
	if rest, virtual := strings.CutPrefix(name, bucket+"."); bucket != "" && virtual {
		name = rest
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 || labels[0] != "s3" {
		return aws.Region{}, false
	}
	r, err := regions.Parse(labels[1])
	if err != nil {
		return aws.Region{}, false
	}
	return r, true
}

// hostname is host without its port.
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// redirectEndpoint is the host a PermanentRedirect names: the request's
// host with the region label after "s3" replaced by the bucket's home
// region, port kept. As in hostRegion, only the labels after the bucket's
// are read, so a dotted bucket name holding s3.<from> is left as it is.
func redirectEndpoint(host, bucket string, from, home aws.Region) string {
	prefix, rest := "", host
	if after, virtual := strings.CutPrefix(host, bucket+"."); bucket != "" && virtual {
		prefix, rest = bucket+".", after
	}
	after, ok := strings.CutPrefix(rest, "s3."+from.String()+".")
	if !ok {
		return host
	}
	return prefix + "s3." + home.String() + "." + after
}

// crossRegion applies ADR 0013 to a request reaching a bucket whose home
// is h. It returns the error AWS answers, or the region a lenient serve
// crossed from (zero when the request is in the bucket's region or asserts
// none, which serves the home region).
func crossRegion(mode CrossRegion, src aws.RegionSource, host, name string, h Home) (aws.Region, *awserr.Error) {
	switch r := src.(type) {
	case aws.None:
		return aws.Region{}, nil
	case aws.CredentialScope:
		if r.Region == h.Region {
			return aws.Region{}, nil
		}
		if mode == CrossRegionLenient {
			return r.Region, nil
		}
		return aws.Region{}, errWrongRegion(r.Region.String(), h)
	case aws.HostRegion:
		if r.Region == h.Region {
			return aws.Region{}, nil
		}
		if mode == CrossRegionLenient {
			return r.Region, nil
		}
		return aws.Region{}, errPermanentRedirect(name, redirectEndpoint(host, name, r.Region, h.Region))
	}
	return aws.Region{}, nil
}

// checkOwner answers an owner header naming another account.
func checkOwner(o *ownerClaim, h Home) *awserr.Error {
	if o == nil {
		return nil
	}
	if _, err := aws.ParseAccountID(o.account); err != nil {
		return errInvalidOwner(*o)
	}
	if o.account != h.OwnerAccount.String() {
		return errAccessDenied()
	}
	return nil
}

// admitted is a request the cross-region and owner checks let through,
// with the region a lenient serve crossed from (zero when none).
type admitted struct {
	crossed aws.Region
}

// admit runs the checks every request about an existing bucket passes:
// the cross-region rules, then the owner header. Its errors carry
// x-amz-bucket-region.
func (s *Service) admit(c *kernel.Call, src aws.RegionSource, a access, b *Bucket) (admitted, *awserr.Error) {
	crossed, err := crossRegion(s.opts.CrossRegion, src, c.Endpoint().Host, a.name, b.Home)
	if err != nil {
		return admitted{}, inRegion(err, b.Home)
	}
	if err := checkOwner(a.owner, b.Home); err != nil {
		return admitted{}, inRegion(err, b.Home)
	}
	return admitted{crossed: crossed}, nil
}

// warnCrossed journals an Advisory on the call when s3.crossRegion: lenient
// served a request from the bucket's home region although it asserted
// another.
func (s *Service) warnCrossed(ctx context.Context, c *kernel.Call, name string, home aws.Region, ad admitted) {
	if ad.crossed.IsZero() {
		return
	}
	c.Record(ctx, c.Scope(), journal.Advisory{
		Kind:     journal.AdvisoryCrossRegionServed,
		Resource: bucketARN(name),
		Text: "the request asserted region " + ad.crossed.String() + " and was served from the bucket's home region " + home.String() +
			"; AWS would refuse or redirect it (s3.crossRegion: lenient)",
	})
}

// reader is the handler of an operation that reads the bucket its input
// names.
func reader[In, Out any](s *Service, of func(*In) access, fn func(in *In, b *Bucket) (*Out, *awserr.Error)) kernel.Handler[In, Out] {
	return func(ctx context.Context, c *kernel.Call, in *In) (*Out, *awserr.Error) {
		return read(ctx, s, c, of(in), func(b *Bucket) (*Out, *awserr.Error) { return fn(in, b) })
	}
}

// writer is the handler of an operation that changes the bucket its input
// names and answers its empty output.
func writer[In, Out any](s *Service, of func(*In) access, fn func(in *In, b *Bucket) *awserr.Error) kernel.Handler[In, Out] {
	return func(ctx context.Context, c *kernel.Call, in *In) (*Out, *awserr.Error) {
		err := s.change(ctx, c, of(in), func(b *Bucket, _ clock.Instant) *awserr.Error { return fn(in, b) })
		if err != nil {
			return nil, err
		}
		return new(Out), nil
	}
}

// read runs fn on the bucket a names once the request is admitted. Writers
// replace the bucket's documents rather than change them, so fn may return
// documents it holds, with one exception: object and multipart writes
// change Objects.Keys, Uploads.ByID and each Upload.Parts in place under
// the cell lock (Objects, Uploads), so fn must never return those maps, a
// key's version chain or an upload.
func read[R any](ctx context.Context, s *Service, c *kernel.Call, a access, fn func(b *Bucket) (R, *awserr.Error)) (R, *awserr.Error) {
	type result struct {
		r       R
		home    aws.Region
		ad      admitted
		err     *awserr.Error
		retired bool
	}
	src := s.regionSource(c, a.name)
	for {
		cell, ok := s.buckets.Lookup(a.name)
		if !ok {
			var zero R
			return zero, errNoSuchBucket(a.name)
		}
		got := store.Read(cell, func(bc *BucketCell) result {
			if cell.Retired() {
				return result{retired: true}
			}
			b := bc.Bucket
			if b == nil {
				return result{err: errNoSuchBucket(a.name)}
			}
			ad, err := s.admit(c, src, a, b)
			if err != nil {
				return result{err: err}
			}
			r, err := fn(b)
			return result{r: r, home: b.Home.Region, ad: ad, err: inRegion(err, b.Home)}
		})
		if got.retired {
			continue
		}
		s.warnCrossed(ctx, c, a.name, got.home, got.ad)
		return got.r, got.err
	}
}

// change runs fn on a copy of the bucket a names, once the request is
// admitted, and stores the copy when fn succeeds. fn replaces the copy's
// documents, slices and maps; it never changes one in place, since readers
// share them (with and without copy a map). Only writeObjects changes
// maps in place, Objects.Keys, Uploads.ByID and each Upload.Parts, for the
// reason Objects gives.
func (s *Service) change(ctx context.Context, c *kernel.Call, a access, fn func(b *Bucket, now clock.Instant) *awserr.Error) *awserr.Error {
	if _, ok := s.buckets.Lookup(a.name); !ok {
		return errNoSuchBucket(a.name)
	}
	src := s.regionSource(c, a.name)
	type result struct {
		home aws.Region
		ad   admitted
	}
	got, err := kernel.Write(c, s.buckets, a.name, func(bc *BucketCell, now clock.Instant, tx *kernel.Tx) (result, *awserr.Error) {
		if bc.Bucket == nil {
			return result{}, s.vanished(tx, a.name)
		}
		ad, err := s.admit(c, src, a, bc.Bucket)
		if err != nil {
			return result{}, err
		}
		next := *bc.Bucket
		if err := fn(&next, now); err != nil {
			return result{}, inRegion(err, next.Home)
		}
		bc.Bucket = &next
		return result{home: next.Home.Region, ad: ad}, nil
	})
	if err != nil {
		return err
	}
	s.warnCrossed(ctx, c, a.name, got.home, got.ad)
	return nil
}

// vanished answers a write that found the cell of name empty: the bucket
// was deleted after the caller's Lookup, and the write's resolve made a new
// empty cell. Dropping it keeps the partition free of empty cells; a
// concurrent CreateBucket's write re-resolves the retired cell.
func (s *Service) vanished(tx *kernel.Tx, name string) *awserr.Error {
	tx.Unchanged()
	s.buckets.Delete(name)
	return errNoSuchBucket(name)
}
