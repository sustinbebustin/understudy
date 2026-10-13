package s3

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// euAlias is the LocationConstraint that creates a bucket in eu-west-1 and
// is reported back as EU (s3.md 3.2).
const euAlias = "EU"

// maxBucketsPage is ListBuckets' page size without max-buckets: the
// default general purpose bucket quota (s3.md 3.3).
const maxBucketsPage = 10000

// bindBuckets binds the bucket lifecycle: create, head, location, list and
// delete.
func (s *Service) bindBuckets(b s3api.Binder) {
	b.CreateBucket(s.createBucket, registry.Full{},
		touches(func(in *s3api.CreateBucketInput) access { return at(in.Bucket, nil) }))
	head := func(in *s3api.HeadBucketInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.HeadBucket(s.headBucket, registry.Full{}, touches(head))
	location := func(in *s3api.GetBucketLocationInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetBucketLocation(reader(s, location, getLocation), registry.Full{}, touches(location))
	b.ListBuckets(s.listBuckets, registry.Partial{Note: "general purpose buckets only; no directory buckets"})
	b.DeleteBucket(s.deleteBucket, registry.Full{},
		touches(func(in *s3api.DeleteBucketInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }))
}

// canonicalID is the canonical user id S3 reports for an account: 64 hex
// characters, as AWS's are. understudy derives it from the account id.
func canonicalID(account aws.AccountID) string {
	sum := sha256.Sum256([]byte("understudy canonical user " + account.String()))
	return hex.EncodeToString(sum[:])
}

// creation is what a CreateBucket asks for once its input is read.
type creation struct {
	name     string
	region   aws.Region
	location string
	tags     map[string]string
	grants   []s3api.Grant
	owner    s3api.ObjectOwnership
	locked   bool
	// endpoint is the region the request was sent to.
	endpoint aws.Region
}

// createBucket creates a bucket (s3.md 3.2): it reserves the name in the
// index, then writes the bucket's cell. Re-creating a us-east-1 bucket the
// caller owns succeeds, resetting the ACL, when the request also resolves
// to us-east-1 and carries no tags; it is BucketAlreadyOwnedByYou
// otherwise.
func (s *Service) createBucket(ctx context.Context, c *kernel.Call, in *s3api.CreateBucketInput) (*s3api.CreateBucketOutput, *awserr.Error) {
	cr, err := s.readCreation(c, in)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	res, err := kernel.Write(c, s.index, indexKey{}, func(x *Index, now clock.Instant, tx *kernel.Tx) (reservation, *awserr.Error) {
		if h, ok := x.Buckets[cr.name]; ok {
			tx.Unchanged()
			switch {
			case h.OwnerAccount != account:
				return nil, errBucketAlreadyExists(cr.name)
			case h.Region == aws.DefaultRegion() && cr.region == aws.DefaultRegion() && len(cr.tags) == 0:
				return recreated{}, nil
			default:
				return nil, errBucketAlreadyOwnedByYou(cr.name)
			}
		}
		h := Home{OwnerAccount: account, Region: cr.region, Created: clock.FromWire(seconds(now.Wire()))}
		x.Buckets = with(x.Buckets, cr.name, h)
		return reserved{home: h}, nil
	})
	if err != nil {
		return nil, err
	}
	out := &s3api.CreateBucketOutput{Location: new(s.createdLocation(c, cr)), BucketArn: new(bucketARN(cr.name).String())}
	switch r := res.(type) {
	case recreated:
		err = s.change(ctx, c, at(cr.name, nil), func(b *Bucket, _ clock.Instant) *awserr.Error {
			b.Grants = cr.grants
			return nil
		})
	case reserved:
		_, err = kernel.Write(c, s.buckets, cr.name, func(bc *BucketCell, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
			bc.Bucket = newBucket(cr, r.home)
			return struct{}{}, nil
		})
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// reservation is the index's answer to a CreateBucket it accepts.
//
//sumtype:decl
type reservation interface{ isReservation() }

// reserved is a new name, now reserved for a bucket at home.
type reserved struct{ home Home }

// recreated is a us-east-1 CreateBucket without tags of a us-east-1 bucket
// the caller owns, which succeeds and resets the ACL. Both regions are
// us-east-1, so the cross-region check of the ACL write always passes.
type recreated struct{}

func (reserved) isReservation()  {}
func (recreated) isReservation() {}

// readCreation reads and checks a CreateBucket input against the region
// the request was sent to.
func (s *Service) readCreation(c *kernel.Call, in *s3api.CreateBucketInput) (creation, *awserr.Error) {
	if !validBucketName(in.Bucket, in.BucketNamespace) {
		return creation{}, errInvalidBucketName(in.Bucket)
	}
	endpoint := endpointRegion(s.regionSource(c, in.Bucket))
	region, location, err := s.locate(endpoint, in.CreateBucketConfiguration)
	if err != nil {
		return creation{}, err
	}
	var tags []s3api.Tag
	if in.CreateBucketConfiguration != nil {
		tags = in.CreateBucketConfiguration.Tags
	}
	tagSet, err := createTags(tags)
	if err != nil {
		return creation{}, err
	}
	owner, err := parseOwnership(in.ObjectOwnership)
	if err != nil {
		return creation{}, err
	}
	acl := aclRequest{canned: in.ACL, headers: grantHeaders(in.GrantFullControl, in.GrantRead, in.GrantReadACP, in.GrantWrite, in.GrantWriteACP)}
	if owner == s3api.ObjectOwnershipBucketOwnerEnforced && !acl.private() {
		return creation{}, errPublicACLAtCreate()
	}
	grants, err := acl.grants(c.Scope().Account)
	if err != nil {
		return creation{}, err
	}
	return creation{
		name: in.Bucket, region: region, location: location, tags: tagSet, grants: grants, owner: owner,
		locked: in.ObjectLockEnabledForBucket != nil && *in.ObjectLockEnabledForBucket, endpoint: endpoint,
	}, nil
}

// locate applies the LocationConstraint rules of s3.md 3.2 to a request
// sent to endpoint: the bucket's home region and the constraint
// GetBucketLocation will report.
func (s *Service) locate(endpoint aws.Region, cfg *s3api.CreateBucketConfiguration) (aws.Region, string, *awserr.Error) {
	constraint := ""
	if cfg != nil && cfg.LocationConstraint != nil {
		constraint = string(*cfg.LocationConstraint)
	}
	euWest1, euErr := s.opts.Regions.Parse("eu-west-1")
	if endpoint != aws.DefaultRegion() {
		switch {
		case constraint == endpoint.String():
			return endpoint, constraint, nil
		case constraint == euAlias && euErr == nil && endpoint == euWest1:
			return endpoint, euAlias, nil
		}
		return aws.Region{}, "", errIllegalLocationConstraint(constraint)
	}
	switch constraint {
	case "":
		return endpoint, "", nil
	case euAlias:
		return euWest1, euAlias, nil
	case aws.DefaultRegion().String():
		return aws.Region{}, "", errInvalidLocationConstraint(constraint)
	}
	r, err := s.opts.Regions.Parse(constraint)
	if err != nil {
		return aws.Region{}, "", errInvalidLocationConstraint(constraint)
	}
	return r, constraint, nil
}

// createdLocation is CreateBucket's Location header: /<bucket> without a
// location constraint, the bucket's virtual-hosted URL with one. A
// virtual-hosted request's Host already starts with the bucket's labels,
// which are not repeated.
func (s *Service) createdLocation(c *kernel.Call, cr creation) string {
	if cr.location == "" {
		return "/" + cr.name
	}
	ep := c.Endpoint()
	base := strings.TrimPrefix(ep.Host, cr.name+".")
	return ep.Scheme + "://" + cr.name + "." + base + "/"
}

// newBucket is a new bucket with AWS's defaults (s3.md 4.1): ownership
// controls with the requested rule, every public access block flag on,
// the SSE-S3 default encryption, and, with Object Lock, versioning on.
func newBucket(cr creation, h Home) *Bucket {
	b := &Bucket{
		Name:     cr.name,
		Home:     h,
		Location: cr.location,
		Grants:   cr.grants,
		Tags:     cr.tags,
		Ownership: &s3api.OwnershipControls{
			Rules: []s3api.OwnershipControlsRule{{ObjectOwnership: cr.owner}},
		},
		PublicAccessBlock: &s3api.PublicAccessBlockConfiguration{
			BlockPublicAcls: new(true), IgnorePublicAcls: new(true), BlockPublicPolicy: new(true), RestrictPublicBuckets: new(true),
		},
	}
	if cr.locked {
		b.ObjectLock = &s3api.ObjectLockConfiguration{ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled)}
		b.Versioning = &s3api.VersioningConfiguration{Status: new(s3api.BucketVersioningStatusEnabled)}
	}
	return b
}

// headBucket answers 200 with the bucket's region, or 404 without a body.
func (s *Service) headBucket(ctx context.Context, c *kernel.Call, in *s3api.HeadBucketInput) (*s3api.HeadBucketOutput, *awserr.Error) {
	out, err := read(ctx, s, c, at(in.Bucket, in.ExpectedBucketOwner), func(b *Bucket) (*s3api.HeadBucketOutput, *awserr.Error) {
		return &s3api.HeadBucketOutput{
			BucketRegion:     new(b.Home.Region.String()),
			BucketArn:        new(bucketARN(b.Name).String()),
			AccessPointAlias: new(false),
		}, nil
	})
	if err != nil && err.ID.Shape == s3api.IdentityNoSuchBucket().Shape {
		return nil, errNotFound()
	}
	return out, err
}

// getLocation reports the bucket's LocationConstraint: empty for
// us-east-1.
func getLocation(_ *s3api.GetBucketLocationInput, b *Bucket) (*s3api.GetBucketLocationOutput, *awserr.Error) {
	out := &s3api.GetBucketLocationOutput{}
	if b.Location != "" {
		out.LocationConstraint = new(s3api.BucketLocationConstraint(b.Location))
	}
	return out, nil
}

// listBuckets lists the caller's buckets in name order (s3.md 3.3).
func (s *Service) listBuckets(_ context.Context, c *kernel.Call, in *s3api.ListBucketsInput) (*s3api.ListBucketsOutput, *awserr.Error) {
	account := c.Scope().Account
	var region aws.Region
	if in.BucketRegion != nil {
		r, err := s.opts.Regions.Parse(*in.BucketRegion)
		if err != nil {
			return nil, errInvalidBucketRegion(*in.BucketRegion)
		}
		region = r
	}
	after := ""
	if in.ContinuationToken != nil && *in.ContinuationToken != "" {
		raw, err := base64.RawURLEncoding.DecodeString(*in.ContinuationToken)
		if err != nil || len(raw) == 0 {
			return nil, errInvalidContinuationToken(*in.ContinuationToken)
		}
		after = string(raw)
	}
	prefix := ""
	if in.Prefix != nil {
		prefix = *in.Prefix
	}
	limit := maxBucketsPage
	if in.MaxBuckets != nil {
		limit = int(cmp.Or(*in.MaxBuckets, int32(maxBucketsPage)))
	}
	homes := s.homes()
	// Buckets is never nil: AWS sends an empty <Buckets/> when nothing
	// matches, which SDKs read as an empty list (REC LocalStack
	// TestS3ListBuckets list-objects-by-prefix-empty: "Buckets": []).
	out := &s3api.ListBucketsOutput{Buckets: []s3api.Bucket{}, Owner: &s3api.Owner{ID: new(canonicalID(account))}, Prefix: in.Prefix}
	for _, name := range slices.Sorted(maps.Keys(homes)) {
		h := homes[name]
		if h.OwnerAccount != account || name <= after || !strings.HasPrefix(name, prefix) || !region.IsZero() && h.Region != region {
			continue
		}
		if len(out.Buckets) == limit {
			out.ContinuationToken = new(base64.RawURLEncoding.EncodeToString([]byte(*out.Buckets[limit-1].Name)))
			break
		}
		out.Buckets = append(out.Buckets, s3api.Bucket{
			Name:         new(name),
			CreationDate: new(h.Created.Wire()),
			BucketRegion: new(h.Region.String()),
			BucketArn:    new(bucketARN(name).String()),
		})
	}
	return out, nil
}

// deleteBucket deletes an empty bucket with its subresources, then
// releases its name.
func (s *Service) deleteBucket(ctx context.Context, c *kernel.Call, in *s3api.DeleteBucketInput) (*s3api.DeleteBucketOutput, *awserr.Error) {
	a := at(in.Bucket, in.ExpectedBucketOwner)
	if _, ok := s.buckets.Lookup(a.name); !ok {
		return nil, errNoSuchBucket(a.name)
	}
	src := s.regionSource(c, a.name)
	type deleted struct {
		home Home
		ad   admitted
		// parts are the references the dropped uploads' parts held.
		parts []blob.Ref
	}
	got, err := kernel.Write(c, s.buckets, a.name, func(bc *BucketCell, _ clock.Instant, tx *kernel.Tx) (deleted, *awserr.Error) {
		b := bc.Bucket
		if b == nil {
			return deleted{}, s.vanished(tx, a.name)
		}
		ad, err := s.admit(c, src, a, b)
		if err != nil {
			return deleted{}, err
		}
		if !b.Objects.empty() {
			return deleted{}, inRegion(errBucketNotEmpty(b), b.Home)
		}
		s.buckets.Delete(a.name)
		bc.Bucket = nil
		return deleted{home: b.Home, ad: ad, parts: b.Uploads.blobs()}, nil
	})
	if err != nil {
		return nil, err
	}
	s.release(ctx, got.parts...)
	s.warnCrossed(ctx, c, a.name, got.home.Region, got.ad)
	_, err = kernel.Write(c, s.index, indexKey{}, func(x *Index, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if h, ok := x.Buckets[a.name]; !ok || h != got.home {
			tx.Unchanged()
			return struct{}{}, nil
		}
		x.Buckets = without(x.Buckets, a.name)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &s3api.DeleteBucketOutput{}, nil
}
