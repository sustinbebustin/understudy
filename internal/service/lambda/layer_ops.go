package lambda

import (
	"context"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// The nine layer operations over the store of layers.go, with the marks and
// notes of lambda-destinations-layers.md 11.1.

func (s *Service) bindLayers(b lambdaapi.Binder) {
	const location = "Content.Location is a link understudy serves the archive at, not a presigned S3 URL"
	b.PublishLayerVersion(s.publishLayerVersion,
		partial("S3ObjectStorageMode REFERENCE is stored and the archive is copied as with COPY; the account code-storage quota is not enforced; layers are never signed"),
		kernel.WithValidator(validatePublishLayerVersion))
	b.GetLayerVersion(s.getLayerVersion, partial(location))
	b.GetLayerVersionByArn(s.getLayerVersionByArn, partial(location))
	b.ListLayers(s.listLayers, full(), kernel.WithValidator(func(in *lambdaapi.ListLayersInput) []awserr.Violation {
		return runtimeFilterViolation(in.CompatibleRuntime)
	}))
	b.ListLayerVersions(s.listLayerVersions, full(), kernel.WithValidator(func(in *lambdaapi.ListLayerVersionsInput) []awserr.Violation {
		return runtimeFilterViolation(in.CompatibleRuntime)
	}))
	b.DeleteLayerVersion(s.deleteLayerVersion, full())
	b.AddLayerVersionPermission(s.addLayerVersionPermission,
		stored("statements are stored and returned by GetLayerVersionPolicy; another account's use of the layer is advised, not refused"))
	b.RemoveLayerVersionPermission(s.removeLayerVersionPermission, full())
	b.GetLayerVersionPolicy(s.getLayerVersionPolicy, full())
}

func (s *Service) publishLayerVersion(ctx context.Context, c *kernel.Call, in *lambdaapi.PublishLayerVersionInput) (*lambdaapi.PublishLayerVersionOutput, *awserr.Error) {
	ref, err := parseLayerName(c, in.LayerName)
	if err != nil {
		return nil, err
	}
	if !isLayerName(ref.name) {
		return nil, errLayerName(in.LayerName)
	}
	archive, unzipped, err := s.layerArchive(ctx, c, in.Content)
	if err != nil {
		return nil, err
	}
	code, err := s.storeZip(ctx, archive, unzipped)
	if err != nil {
		return nil, err
	}
	v, err := write(s, c, ref.scope, func(st *State, now clock.Instant, _ *kernel.Tx) (LayerVersion, *awserr.Error) {
		v := LayerVersion{
			Number:                  st.Layers[ref.name].LastVersion + 1,
			Description:             deref(in.Description),
			CompatibleRuntimes:      nameList(in.CompatibleRuntimes),
			CompatibleArchitectures: nameList(in.CompatibleArchitectures),
			LicenseInfo:             clonePtr(in.LicenseInfo),
			StorageMode:             string(deref(in.Content.S3ObjectStorageMode)),
			Created:                 now,
			Sha256:                  code.Sha256,
			Size:                    code.Size,
			Unzipped:                code.Unzipped,
			Blob:                    code.Blob,
		}
		st.withLayerVersion(ref, v)
		return v, nil
	})
	if err != nil {
		s.release(ctx, code.Blob)
		return nil, err
	}
	touch(c, ref.arn())
	return new(lambdaapi.PublishLayerVersionOutput(v.render(c.Endpoint(), ref))), nil
}

// layerArchive reads the archive content names, inline or from S3 for c's
// caller as function code is read, checked under function code's archive
// rules (lambda-destinations-layers.md 11.5.2; the layer-side texts are
// unrecorded, LDL-14), and the sum of its entries' uncompressed sizes.
func (s *Service) layerArchive(ctx context.Context, c *kernel.Call, content lambdaapi.LayerVersionContentInput) (archive []byte, unzipped int64, err *awserr.Error) {
	hasZip, hasS3 := content.ZipFile != nil, content.S3Bucket != nil || content.S3Key != nil || content.S3ObjectVersion != nil
	switch {
	case hasZip && !hasS3:
		unzipped, err := checkZip(content.ZipFile)
		return content.ZipFile, unzipped, err
	case hasS3 && !hasZip && content.S3Bucket != nil && content.S3Key != nil:
		b, err := s.fetchPackage(ctx, c, s3Source{bucket: *content.S3Bucket, key: *content.S3Key, version: content.S3ObjectVersion})
		if err != nil {
			return nil, 0, err
		}
		unzipped, err := checkArchive(b)
		return b, unzipped, err
	}
	return nil, 0, errLayerContent()
}

func (s *Service) getLayerVersion(_ context.Context, c *kernel.Call, in *lambdaapi.GetLayerVersionInput) (*lambdaapi.GetLayerVersionResponse, *awserr.Error) {
	ref, err := parseLayerName(c, in.LayerName)
	if err != nil {
		return nil, err
	}
	if err := checkLayerVersion(in.VersionNumber); err != nil {
		return nil, err
	}
	return s.viewLayerVersion(c, ref, in.VersionNumber)
}

func (s *Service) getLayerVersionByArn(_ context.Context, c *kernel.Call, in *lambdaapi.GetLayerVersionByArnInput) (*lambdaapi.GetLayerVersionResponse, *awserr.Error) {
	ref, n, ok := parseLayerVersionARN(in.Arn)
	if !ok {
		return nil, errResourceNotFound()
	}
	return s.viewLayerVersion(c, ref, n)
}

// viewLayerVersion answers version n of the layer ref names, or the
// recorded not-found for a layer or version that does not exist
// (lambda-destinations-layers.md 6.4).
func (s *Service) viewLayerVersion(c *kernel.Call, ref layerRef, n int64) (*lambdaapi.GetLayerVersionResponse, *awserr.Error) {
	out, err := view(s, ref.scope, func(st *State) (*lambdaapi.GetLayerVersionResponse, *awserr.Error) {
		v, ok := st.layerVersion(ref, n)
		if !ok {
			return nil, errResourceNotFound()
		}
		return new(v.render(c.Endpoint(), ref)), nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.versionARN(n))
	return out, nil
}

func (s *Service) listLayers(_ context.Context, c *kernel.Call, in *lambdaapi.ListLayersInput) (*lambdaapi.ListLayersOutput, *awserr.Error) {
	scope := c.Scope()
	items, _ := view(s, scope, func(st *State) ([]lambdaapi.LayersListItem, *awserr.Error) {
		var out []lambdaapi.LayersListItem
		for _, name := range slices.Sorted(maps.Keys(st.Layers)) {
			matching := st.Layers[name].newest(in.CompatibleRuntime, in.CompatibleArchitecture)
			if len(matching) == 0 {
				continue
			}
			ref := layerRef{scope: scope, name: name}
			out = append(out, lambdaapi.LayersListItem{LayerName: new(name), LayerArn: new(ref.arn()), LatestMatchingVersion: new(matching[0].listItem(ref))})
		}
		return out, nil
	})
	p, err := page(listingLayers, items, func(i lambdaapi.LayersListItem) string { return deref(i.LayerName) }, in.MaxItems, listPageCap, in.Marker)
	if err != nil {
		return nil, err
	}
	return &lambdaapi.ListLayersOutput{Layers: append([]lambdaapi.LayersListItem{}, p.Items...), NextMarker: p.Next}, nil
}

// layerVersionRow is one row of ListLayerVersions with the key it pages by,
// which sorts the newest version first.
type layerVersionRow struct {
	key  string
	item lambdaapi.LayerVersionsListItem
}

func (s *Service) listLayerVersions(_ context.Context, c *kernel.Call, in *lambdaapi.ListLayerVersionsInput) (*lambdaapi.ListLayerVersionsOutput, *awserr.Error) {
	ref, err := parseLayerName(c, in.LayerName)
	if err != nil {
		return nil, err
	}
	// A layer that never existed, or whose versions are all deleted, lists
	// none (lambda-destinations-layers.md 6.5).
	rows, _ := view(s, ref.scope, func(st *State) ([]layerVersionRow, *awserr.Error) {
		versions := st.Layers[ref.name].newest(in.CompatibleRuntime, in.CompatibleArchitecture)
		rows := make([]layerVersionRow, len(versions))
		for i := range versions {
			// MaxInt64 less the number, zero-padded to its 19 digits, sorts
			// the newest version first.
			key := strconv.FormatInt(math.MaxInt64-versions[i].Number, 10)
			rows[i] = layerVersionRow{key: strings.Repeat("0", 19-len(key)) + key, item: versions[i].listItem(ref)}
		}
		return rows, nil
	})
	p, err := page(listingLayerVersions, rows, func(r layerVersionRow) string { return r.key }, in.MaxItems, listPageCap, in.Marker)
	if err != nil {
		return nil, err
	}
	items := make([]lambdaapi.LayerVersionsListItem, 0, len(p.Items))
	for _, r := range p.Items {
		items = append(items, r.item)
	}
	return &lambdaapi.ListLayerVersionsOutput{LayerVersions: items, NextMarker: p.Next}, nil
}

// deleteLayerVersion deletes a version and releases the store's reference on
// its archive; functions that use it hold their own. A version that does
// not exist is deleted already: 204 (lambda-destinations-layers.md 6.6).
func (s *Service) deleteLayerVersion(ctx context.Context, c *kernel.Call, in *lambdaapi.DeleteLayerVersionInput) (*lambdaapi.DeleteLayerVersionOutput, *awserr.Error) {
	ref, err := parseLayerName(c, in.LayerName)
	if err != nil {
		return nil, err
	}
	if err := checkLayerVersion(in.VersionNumber); err != nil {
		return nil, err
	}
	released, err := write(s, c, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) ([]blob.Ref, *awserr.Error) {
		v, ok := st.layerVersion(ref, in.VersionNumber)
		if !ok {
			tx.Unchanged()
			return nil, nil
		}
		// The layer stays when its last version goes: its LastVersion
		// keeps the numbering (finding 9).
		l := st.Layers[ref.name].clone()
		delete(l.Versions, v.Number)
		st.Layers[ref.name] = l
		return []blob.Ref{v.Blob}, nil
	})
	if err != nil {
		return nil, err
	}
	s.release(ctx, released...)
	touch(c, ref.versionARN(in.VersionNumber))
	return &lambdaapi.DeleteLayerVersionOutput{}, nil
}

func (s *Service) addLayerVersionPermission(_ context.Context, c *kernel.Call, in *lambdaapi.AddLayerVersionPermissionInput) (*lambdaapi.AddLayerVersionPermissionOutput, *awserr.Error) {
	ref, err := parseLayerName(c, in.LayerName)
	if err != nil {
		return nil, err
	}
	if err := checkLayerVersion(in.VersionNumber); err != nil {
		return nil, err
	}
	principal, err := layerPrincipal(ref.scope, in.Principal)
	if err != nil {
		return nil, err
	}
	versionARN := ref.versionARN(in.VersionNumber)
	text := statementJSON(layerStatement(in, principal, versionARN))
	revision, err := write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (string, *awserr.Error) {
		v, ok := st.layerVersion(ref, in.VersionNumber)
		if !ok {
			return "", errLayerVersionMissing(versionARN)
		}
		if err := checkLayerPolicyRevision(v, in.RevisionId); err != nil {
			return "", err
		}
		if slices.ContainsFunc(v.Policy, func(s Statement) bool { return s.Sid == in.StatementId }) {
			return "", errStatementExists(in.StatementId)
		}
		v.Policy = append(slices.Clone(v.Policy), Statement{Sid: in.StatementId, JSON: text})
		v.PolicyRevision = st.revision(ref.scope)
		st.withLayerVersion(ref, v)
		return v.PolicyRevision, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, versionARN)
	return &lambdaapi.AddLayerVersionPermissionOutput{Statement: new(text), RevisionId: new(revision)}, nil
}

func (s *Service) removeLayerVersionPermission(_ context.Context, c *kernel.Call, in *lambdaapi.RemoveLayerVersionPermissionInput) (*lambdaapi.RemoveLayerVersionPermissionOutput, *awserr.Error) {
	ref, err := parseLayerName(c, in.LayerName)
	if err != nil {
		return nil, err
	}
	if err := checkLayerVersion(in.VersionNumber); err != nil {
		return nil, err
	}
	versionARN := ref.versionARN(in.VersionNumber)
	_, err = write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		v, ok := st.layerVersion(ref, in.VersionNumber)
		if !ok {
			return struct{}{}, errLayerVersionMissing(versionARN)
		}
		i := slices.IndexFunc(v.Policy, func(s Statement) bool { return s.Sid == in.StatementId })
		if i < 0 {
			return struct{}{}, errStatementNotFound(in.StatementId)
		}
		if err := checkLayerPolicyRevision(v, in.RevisionId); err != nil {
			return struct{}{}, err
		}
		v.Policy = slices.Delete(slices.Clone(v.Policy), i, i+1)
		if len(v.Policy) == 0 {
			v.Policy, v.PolicyRevision = nil, ""
		} else {
			v.PolicyRevision = st.revision(ref.scope)
		}
		st.withLayerVersion(ref, v)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, versionARN)
	return &lambdaapi.RemoveLayerVersionPermissionOutput{}, nil
}

func (s *Service) getLayerVersionPolicy(_ context.Context, c *kernel.Call, in *lambdaapi.GetLayerVersionPolicyInput) (*lambdaapi.GetLayerVersionPolicyOutput, *awserr.Error) {
	ref, err := parseLayerName(c, in.LayerName)
	if err != nil {
		return nil, err
	}
	if err := checkLayerVersion(in.VersionNumber); err != nil {
		return nil, err
	}
	versionARN := ref.versionARN(in.VersionNumber)
	out, err := view(s, ref.scope, func(st *State) (*lambdaapi.GetLayerVersionPolicyOutput, *awserr.Error) {
		v, ok := st.layerVersion(ref, in.VersionNumber)
		if !ok {
			return nil, errLayerVersionMissing(versionARN)
		}
		// No statement yet is recorded; after the last is removed the same
		// answer is inferred (LDL-15).
		doc := v.policyDocument()
		if doc == "" {
			return nil, errNoPolicy()
		}
		return &lambdaapi.GetLayerVersionPolicyOutput{Policy: new(doc), RevisionId: new(v.PolicyRevision)}, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, versionARN)
	return out, nil
}
