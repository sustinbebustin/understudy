package lambda

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The account limits GetAccountSettings reports and reserved concurrency
// is checked against (lambda-api.md 7.4). The code storage quota is the
// documented 75 GB, which get-account-settings reports as 80530636800.
const (
	accountConcurrency       = 1000
	minUnreservedConcurrency = 100
	accountCodeStorage       = 75 << 30
)

// listPageCap is the most functions or versions one listing page holds,
// whatever MaxItems says.
const listPageCap = 50

func (s *Service) bindFunctions(b lambdaapi.Binder) {
	// The layer clause is lambda-destinations-layers.md 11.1's.
	const layerFound = "a layer version is found only if it was published to understudy, which never fetches layers from AWS; "
	// The trust check is recorded for CreateFunction only
	// (iam-enforcement-surfaces.md 13.28).
	// The dead-letter checks are lambda-destinations-layers.md 5.1's.
	const (
		trust       = " under iam.enforce=enforce the caller needs iam:PassRole on the execution role, asked with iam:PassedToService lambda.amazonaws.com, then the role's trust policy must let lambda.amazonaws.com assume it"
		trustModes  = ", else InvalidParameterValueException; under advise both are asked and a denial journaled, under off neither"
		trustUpdate = " (inferred: only CreateFunction's trust check is recorded)"
		deadLetter  = "; a dead-letter target must be an existing standard SQS queue or an SNS topic, and under iam.enforce=enforce the execution role must be allowed sqs:SendMessage or sns:Publish on it (advise journals a denial, off asks nothing); the answers for a missing topic and for the role's sns:Publish are inferred"
	)
	b.CreateFunction(s.createFunction, partial(layerFound+"PublishTo, TenancyConfig and DurableConfig are refused; code signing and VPC networking are stored only;"+trust+trustModes+deadLetter+" (inferred: only UpdateFunctionConfiguration's dead-letter checks are recorded)"),
		kernel.WithValidator(validateCreateFunction), kernel.WithCheck(checkCreateFunction))
	b.GetFunction(s.getFunction, partial("Code.Location is a link understudy serves the package at, not a presigned S3 URL"))
	b.GetFunctionConfiguration(s.getFunctionConfiguration, full())
	b.ListFunctions(s.listFunctions, full())
	b.UpdateFunctionConfiguration(s.updateFunctionConfiguration, partial(layerFound+"DurableConfig is refused; code signing and VPC networking are stored only; when the update passes a Role,"+trust+trustUpdate+trustModes+deadLetter),
		kernel.WithValidator(validateUpdateConfiguration), kernel.WithCheck(checkUpdateConfiguration))
	b.UpdateFunctionCode(s.updateFunctionCode, partial(publishToRefused), kernel.WithValidator(validateUpdateCode),
		kernel.WithCheck(func(in *lambdaapi.UpdateFunctionCodeInput) *awserr.Error {
			return checkPublishTo("UpdateFunctionCode", in.PublishTo)
		}))
	b.DeleteFunction(s.deleteFunction, full())
	b.GetFunctionCodeSigningConfig(s.getFunctionCodeSigningConfig, stored("code signing is not enforced; no function has a configuration"))
	b.GetAccountSettings(s.getAccountSettings, full())
}

func (s *Service) createFunction(ctx context.Context, c *kernel.Call, in *lambdaapi.CreateFunctionInput) (*lambdaapi.FunctionConfiguration, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	if err := ref.unqualified(errQualifierNotAccepted("CreateFunction")); err != nil {
		return nil, err
	}
	if err := checkRoleTrust(ctx, c, ref, &in.Role); err != nil {
		return nil, err
	}
	if target := deadLetterTarget(in.DeadLetterConfig); target != "" {
		if err := s.checkDeadLetter(ctx, c, ref, in.Role, target); err != nil {
			return nil, err
		}
	}
	packageType := smithy.Deref(in.PackageType)
	if packageType == "" {
		packageType = lambdaapi.PackageTypeZip
	}
	src, err := sourceOf(packageType, in.Code.ZipFile, in.Code.S3Bucket, in.Code.S3Key, in.Code.S3ObjectVersion, in.Code.ImageUri)
	if err != nil {
		return nil, err
	}
	att, err := s.resolveLayers(ctx, c.Principal(), ref.scope, in.Layers)
	if err != nil {
		return nil, err
	}
	code, err := s.newCode(ctx, c, src)
	if err != nil {
		s.release(ctx, att.blobs()...)
		return nil, err
	}
	if err := checkCodeWithLayers(code, att.layers); err != nil {
		s.release(ctx, append(codeRefs(code), att.blobs()...)...)
		return nil, err
	}
	held, err := s.holdForPublish(ctx, in.Publish, code, att.layers)
	if err != nil {
		s.release(ctx, append(codeRefs(code), att.blobs()...)...)
		return nil, err
	}
	cfg := newConfig(in)
	cfg.Layers = att.layers
	out, err := write(s, c, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (lambdaapi.FunctionConfiguration, *awserr.Error) {
		if _, ok := st.Functions[ref.name]; ok {
			return lambdaapi.FunctionConfiguration{}, errFunctionExists(ref.name)
		}
		v := Version{Number: latest, Config: cfg, Code: code, Status: s.initialStatus(code, now), Modified: now, RevisionID: st.revision(ref.scope)}
		f := Function{
			Name: ref.name, Created: now, Latest: v, LastVersion: st.reclaimVersions(ref.name),
			Tags: maps.Clone(in.Tags), Generation: 1, Incarnation: st.revision(ref.scope),
		}
		s.armProvisioning(tx, ref.scope, ref.name, &v, now)
		out := renderConfig(ref.scope, &f, &v, ref.arn(), false)
		if in.Publish {
			// A new function has no version yet, so publishing always adds
			// version 1, which takes the held reference. AWS reports it
			// under the unqualified ARN with Version "1"
			// (test_publish_version_on_create), unlike UpdateFunctionCode.
			pv, _ := s.publish(st, tx, ref.scope, &f, nil, now)
			out = renderConfig(ref.scope, &f, &pv, ref.arn(), false)
		}
		st.Functions[ref.name] = f
		return out, nil
	})
	if err != nil {
		s.release(ctx, slices.Concat(codeRefs(code), att.blobs(), held)...)
		return nil, err
	}
	touch(c, ref.arn())
	return &out, nil
}

// holdForPublish retains, before a write that changes $LATEST and then
// publishes it, the references a new version of code with layers would
// hold, as publish needs them held. The caller releases the returned
// references when the write fails or publishes no version.
func (s *Service) holdForPublish(ctx context.Context, publish bool, code Code, layers []LayerRef) ([]blob.Ref, *awserr.Error) {
	if !publish {
		return nil, nil
	}
	refs := append(codeRefs(code), layerBlobs(layers)...)
	if err := s.retain(ctx, refs...); err != nil {
		return nil, err
	}
	return refs, nil
}

// newCode turns a code source into the code a version stores; a zip
// package, sent inline or read from S3 for c's caller, is checked and kept
// in the blob store first.
func (s *Service) newCode(ctx context.Context, c *kernel.Call, src codeSource) (Code, *awserr.Error) {
	switch src := src.(type) {
	case zipSource:
		unzipped, err := checkZip(src.bytes)
		if err != nil {
			return nil, err
		}
		return s.storeZip(ctx, src.bytes, unzipped)
	case s3Source:
		b, err := s.fetchPackage(ctx, c, src)
		if err != nil {
			return nil, err
		}
		unzipped, err := checkArchive(b)
		if err != nil {
			return nil, err
		}
		return s.storeZip(ctx, b, unzipped)
	case imageSource:
		return ImageCode{URI: src.uri}, nil
	}
	return nil, errCodeSource(string(lambdaapi.PackageTypeZip))
}

func (s *Service) getFunction(_ context.Context, c *kernel.Call, in *lambdaapi.GetFunctionInput) (*lambdaapi.GetFunctionOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	return store.View2(s.cells, ref.scope, func(st *State) (*lambdaapi.GetFunctionOutput, *awserr.Error) {
		f, v, err := st.lookup(ref)
		if err != nil {
			return nil, err
		}
		out := &lambdaapi.GetFunctionOutput{
			Configuration: new(renderConfig(ref.scope, &f, &v, ref.qualifiedARN(), false)),
			Code:          renderCodeLocation(c.Endpoint(), ref.scope, f.Name, v),
		}
		if len(f.Tags) > 0 {
			out.Tags = maps.Clone(f.Tags)
		}
		if f.Reserved != nil && ref.qualifier == "" {
			out.Concurrency = &lambdaapi.Concurrency{ReservedConcurrentExecutions: new(*f.Reserved)}
		}
		return out, nil
	})
}

// renderCodeLocation is GetFunction's Code for version v of function: a
// zip package's Location is the link understudy serves it at
// (codeLocation), where AWS sends a presigned S3 URL.
func renderCodeLocation(e kernel.Endpoint, scope aws.Scope, function string, v Version) *lambdaapi.FunctionCodeLocation {
	switch code := v.Code.(type) {
	case ZipCode:
		return &lambdaapi.FunctionCodeLocation{RepositoryType: new("S3"), Location: new(codeLocation(e, scope, function, v.Number, code))}
	case ImageCode:
		out := &lambdaapi.FunctionCodeLocation{RepositoryType: new("ECR"), ImageUri: new(code.URI)}
		if digest, ok := cutDigest(code.Digest); ok {
			out.ResolvedImageUri = new(launcher.Repository(code.URI) + "@sha256:" + digest)
		}
		return out
	}
	return &lambdaapi.FunctionCodeLocation{}
}

func (s *Service) getFunctionConfiguration(_ context.Context, c *kernel.Call, in *lambdaapi.GetFunctionConfigurationInput) (*lambdaapi.FunctionConfiguration, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	return store.View2(s.cells, ref.scope, func(st *State) (*lambdaapi.FunctionConfiguration, *awserr.Error) {
		f, v, err := st.lookup(ref)
		if err != nil {
			return nil, err
		}
		return new(renderConfig(ref.scope, &f, &v, ref.qualifiedARN(), false)), nil
	})
}

// versionRow is one row of a function or version listing, with the key it
// sorts and pages by.
type versionRow struct {
	key    string
	config *lambdaapi.FunctionConfiguration
}

// versionKey sorts $LATEST first and published versions by number.
func versionKey(number string) string {
	if number == latest {
		return ""
	}
	return strings.Repeat("0", max(0, 10-len(number))) + number
}

// versionRows renders the versions of f as listing rows: $LATEST under
// its unqualified ARN when unqualified is set, every version under its
// qualified ARN otherwise.
func versionRows(scope aws.Scope, f *Function, prefix string, unqualified bool) []versionRow {
	arn := functionARN(scope, f.Name)
	if unqualified {
		return []versionRow{{key: prefix, config: new(renderConfig(scope, f, &f.Latest, arn, true))}}
	}
	versions := f.all()
	rows := make([]versionRow, len(versions))
	for i := range versions {
		v := &versions[i]
		rows[i] = versionRow{key: prefix + versionKey(v.Number), config: new(renderConfig(scope, f, v, arn+":"+v.Number, true))}
	}
	return rows
}

// configs are the rows' configurations.
func configs(rows []versionRow) []lambdaapi.FunctionConfiguration {
	out := make([]lambdaapi.FunctionConfiguration, len(rows))
	for i, r := range rows {
		out[i] = *r.config
	}
	return out
}

func (s *Service) listFunctions(_ context.Context, c *kernel.Call, in *lambdaapi.ListFunctionsInput) (*lambdaapi.ListFunctionsOutput, *awserr.Error) {
	scope := c.Scope()
	all := in.FunctionVersion != nil && *in.FunctionVersion == lambdaapi.FunctionVersionAll
	rows, _ := store.View2(s.cells, scope, func(st *State) ([]versionRow, *awserr.Error) {
		var rows []versionRow
		for _, name := range slices.Sorted(maps.Keys(st.Functions)) {
			f := st.Functions[name]
			rows = append(rows, versionRows(scope, &f, name+"\x00", !all)...)
		}
		return rows, nil
	})
	p, err := page(listingFunctions, rows, func(r versionRow) string { return r.key }, in.MaxItems, listPageCap, in.Marker)
	if err != nil {
		return nil, err
	}
	return &lambdaapi.ListFunctionsOutput{Functions: configs(p.Items), NextMarker: p.Next}, nil
}

// updateLatest applies change to $LATEST of the function ref names, once
// its state allows a change and the RevisionId precondition holds, and
// stores the result as a new revision of a new generation. change gets
// what $LATEST was, for the status of an update in progress; when change
// starts one, the configuration it serves takes its own reference on each
// of its layers' archives (Serving.blobRefs), which settleVersion releases.
func (s *Service) updateLatest(ctx context.Context, st *State, tx *kernel.Tx, ref functionRef, revision *string, now clock.Instant, change func(v *Version, was Serving) *awserr.Error) (Function, *awserr.Error) {
	f, err := st.function(ref)
	if err != nil {
		return Function{}, err
	}
	if err := mutable(ref.arn(), &f.Latest); err != nil {
		return Function{}, err
	}
	if revision != nil && *revision != f.Latest.RevisionID {
		return Function{}, errRevisionMismatch()
	}
	v := f.Latest
	v.Config = v.Config.clone()
	if err := change(&v, Serving{Config: f.Latest.Config.clone(), Generation: f.Generation}); err != nil {
		return Function{}, err
	}
	if u, ok := v.updating(); ok {
		if err := s.retain(ctx, layerBlobs(u.Serving.Config.Layers)...); err != nil {
			return Function{}, err
		}
	}
	v.Modified, v.RevisionID = now, st.revision(ref.scope)
	f = f.withVersion(v)
	f.Generation++
	s.armProvisioning(tx, ref.scope, ref.name, &v, now)
	st.Functions[ref.name] = f
	return f, nil
}

// checkImageUpdate refuses layers, then a runtime or handler, on an image
// function's configuration (lambda-destinations-layers.md 7.1).
func checkImageUpdate(code Code, in *lambdaapi.UpdateFunctionConfigurationInput) *awserr.Error {
	if _, image := code.(ImageCode); !image {
		return nil
	}
	switch {
	case len(in.Layers) > 0:
		return errImageTakesNoLayers()
	case in.Runtime != nil || in.Handler != nil:
		return errImageTakesNoRuntime()
	}
	return nil
}

func (s *Service) updateFunctionConfiguration(ctx context.Context, c *kernel.Call, in *lambdaapi.UpdateFunctionConfigurationInput) (*lambdaapi.FunctionConfiguration, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	if err := ref.unqualified(errQualifierNotAccepted("UpdateFunctionConfiguration")); err != nil {
		return nil, err
	}
	if err := checkRoleTrust(ctx, c, ref, in.Role); err != nil {
		return nil, err
	}
	if err := s.checkUpdatedDeadLetter(ctx, c, ref, in); err != nil {
		return nil, err
	}
	att, err := s.layersForUpdate(ctx, c.Principal(), ref, in.Layers)
	if err != nil {
		return nil, err
	}
	type result struct {
		out      lambdaapi.FunctionConfiguration
		released []blob.Ref
	}
	res, err := write(s, c, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (result, *awserr.Error) {
		var released []blob.Ref
		f, err := s.updateLatest(ctx, st, tx, ref, in.RevisionId, now, func(v *Version, was Serving) *awserr.Error {
			if err := checkImageUpdate(v.Code, in); err != nil {
				return err
			}
			// Layers replaces the list whole, and [] clears it
			// (lambda-destinations-layers.md 9.2).
			if in.Layers != nil {
				if err := checkCodeWithLayers(v.Code, att.layers); err != nil {
					return err
				}
				released, v.Config.Layers = layerBlobs(v.Config.Layers), att.layers
			}
			v.Config.update(in)
			v.Status = s.updatedStatus(now, was)
			return nil
		})
		if err != nil {
			return result{}, err
		}
		return result{out: renderConfig(ref.scope, &f, &f.Latest, ref.arn(), false), released: released}, nil
	})
	if err != nil {
		s.release(ctx, att.blobs()...)
		return nil, err
	}
	s.release(ctx, res.released...)
	touch(c, ref.arn())
	return &res.out, nil
}

// deadLetterTarget is the target a DeadLetterConfig sets; "" for none,
// which clears the target ({"TargetArn": ""}) or leaves it ({}), and needs
// no check.
func deadLetterTarget(d *lambdaapi.DeadLetterConfig) string {
	if d == nil {
		return ""
	}
	return smithy.Deref(d.TargetArn)
}

// checkUpdatedDeadLetter checks the dead-letter target an
// UpdateFunctionConfiguration sets (checkDeadLetter) for the execution role
// the function will have: the Role the update passes, else the stored one.
// A missing function is refused first, as the write would refuse it.
func (s *Service) checkUpdatedDeadLetter(ctx context.Context, c *kernel.Call, ref functionRef, in *lambdaapi.UpdateFunctionConfigurationInput) *awserr.Error {
	target := deadLetterTarget(in.DeadLetterConfig)
	if target == "" {
		return nil
	}
	role, err := store.View2(s.cells, ref.scope, func(st *State) (string, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return "", err
		}
		return f.Latest.Config.Role, nil
	})
	if err != nil {
		return err
	}
	if in.Role != nil {
		role = *in.Role
	}
	return s.checkDeadLetter(ctx, c, ref, role, target)
}

// layersForUpdate resolves the Layers caller's UpdateFunctionConfiguration
// gives the function ref names; nothing for none. An image function takes
// no layer, which is refused before the layers are looked up; the write
// checks again, as the function may be created anew in between.
func (s *Service) layersForUpdate(ctx context.Context, caller identity.Principal, ref functionRef, layers []string) (attachment, *awserr.Error) {
	if len(layers) == 0 {
		return attachment{}, nil
	}
	packageType, err := s.packageType(ref)
	if err != nil {
		return attachment{}, err
	}
	if packageType == lambdaapi.PackageTypeImage {
		return attachment{}, errImageTakesNoLayers()
	}
	return s.resolveLayers(ctx, caller, ref.scope, layers)
}

// packageType is the package type of the function ref names.
func (s *Service) packageType(ref functionRef) (lambdaapi.PackageType, *awserr.Error) {
	return store.View2(s.cells, ref.scope, func(st *State) (lambdaapi.PackageType, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return "", err
		}
		if _, image := f.Latest.Code.(ImageCode); image {
			return lambdaapi.PackageTypeImage, nil
		}
		return lambdaapi.PackageTypeZip, nil
	})
}

// codeUpdateSource reads the code an UpdateFunctionCode names, which must
// be of the function's package type (lambda-api.md 4.4).
func (s *Service) codeUpdateSource(ref functionRef, in *lambdaapi.UpdateFunctionCodeInput) (codeSource, *awserr.Error) {
	packageType, err := s.packageType(ref)
	if err != nil {
		return nil, err
	}
	switch {
	case packageType == lambdaapi.PackageTypeZip && in.ImageUri != nil:
		return nil, errImageURIOnZip()
	case packageType == lambdaapi.PackageTypeImage && in.ImageUri == nil:
		return nil, errImageURIMissing()
	}
	return sourceOf(packageType, in.ZipFile, in.S3Bucket, in.S3Key, in.S3ObjectVersion, in.ImageUri)
}

func (s *Service) updateFunctionCode(ctx context.Context, c *kernel.Call, in *lambdaapi.UpdateFunctionCodeInput) (*lambdaapi.FunctionConfiguration, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	if err := ref.unqualified(errQualifierNotAccepted("UpdateFunctionCode")); err != nil {
		return nil, err
	}
	src, err := s.codeUpdateSource(ref, in)
	if err != nil {
		return nil, err
	}
	if in.DryRun {
		return s.dryRunCode(ctx, c, ref, src)
	}
	code, err := s.newCode(ctx, c, src)
	if err != nil {
		return nil, err
	}
	held, err := s.holdForPublish(ctx, in.Publish, code, nil)
	if err != nil {
		s.release(ctx, codeRefs(code)...)
		return nil, err
	}
	type result struct {
		out      lambdaapi.FunctionConfiguration
		released []blob.Ref
	}
	// heldLayers are the references a published version's layers take:
	// $LATEST's, which a code update keeps, held inside the write before it
	// changes the state, as publishing cannot fail.
	var heldLayers []blob.Ref
	res, err := write(s, c, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (result, *awserr.Error) {
		if f, ok := st.Functions[ref.name]; ok && in.Publish {
			if err := s.retain(ctx, layerBlobs(f.Latest.Config.Layers)...); err != nil {
				return result{}, err
			}
			heldLayers = layerBlobs(f.Latest.Config.Layers)
		}
		var released []blob.Ref
		f, err := s.updateLatest(ctx, st, tx, ref, in.RevisionId, now, func(v *Version, was Serving) *awserr.Error {
			var err *awserr.Error
			released, err = s.applyCode(v, was, code, in.Architectures, now)
			return err
		})
		if err != nil {
			return result{}, err
		}
		out := renderConfig(ref.scope, &f, &f.Latest, ref.arn(), false)
		if in.Publish {
			pv, added := s.publish(st, tx, ref.scope, &f, nil, now)
			if !added {
				released = slices.Concat(released, held, heldLayers)
			}
			st.Functions[ref.name] = f
			out = renderConfig(ref.scope, &f, &pv, ref.arn()+":"+pv.Number, false)
		}
		return result{out: out, released: released}, nil
	})
	if err != nil {
		s.release(ctx, slices.Concat(codeRefs(code), held, heldLayers)...)
		return nil, err
	}
	s.release(ctx, res.released...)
	touch(c, ref.arn())
	s.hotReloadAdvisory(ctx, c, ref)
	return &res.out, nil
}

// applyCode is UpdateFunctionCode's change to $LATEST v, whose state
// before the update is was: v takes code, which with v's layers must fit
// the unzipped limit, or for an image waits for it, on the architecture
// archs names. It returns the package reference v no longer holds; an
// update in progress keeps the replaced package for the invocations it
// serves until it completes (settleVersion).
func (s *Service) applyCode(v *Version, was Serving, code Code, archs []lambdaapi.Architecture, now clock.Instant) ([]blob.Ref, *awserr.Error) {
	if err := samePackageType(v.Code, code); err != nil {
		return nil, err
	}
	if err := checkCodeWithLayers(code, v.Config.Layers); err != nil {
		return nil, err
	}
	if len(archs) == 1 {
		v.Config.Architecture = string(archs[0])
	}
	if image, ok := code.(ImageCode); ok {
		v.Status = Active{Update: UpdatingImage{Pending: image}}
		return nil, nil
	}
	was.Code = v.Code
	v.Code, v.Status = code, s.updatedStatus(now, was)
	if s.clockTransitions() {
		return nil, nil
	}
	return codeRefs(was.Code), nil
}

// samePackageType refuses code of the other package type than current.
// codeUpdateSource checks this before the package is stored; checking
// again inside the write covers a function deleted and created again with
// the other type in between.
func samePackageType(current, code Code) *awserr.Error {
	_, wasImage := current.(ImageCode)
	_, isImage := code.(ImageCode)
	switch {
	case wasImage && !isImage:
		return errImageURIMissing()
	case !wasImage && isImage:
		return errImageURIOnZip()
	}
	return nil
}

// dryRunCode validates an UpdateFunctionCode without changing the
// function: the package is checked, with $LATEST's layers against the
// unzipped limit, and the current $LATEST returned.
func (s *Service) dryRunCode(ctx context.Context, c *kernel.Call, ref functionRef, src codeSource) (*lambdaapi.FunctionConfiguration, *awserr.Error) {
	var code Code = ImageCode{}
	switch src := src.(type) {
	case zipSource:
		unzipped, err := checkZip(src.bytes)
		if err != nil {
			return nil, err
		}
		code = ZipCode{Unzipped: unzipped}
	case s3Source:
		b, err := s.fetchPackage(ctx, c, src)
		if err != nil {
			return nil, err
		}
		unzipped, err := checkArchive(b)
		if err != nil {
			return nil, err
		}
		code = ZipCode{Unzipped: unzipped}
	case imageSource:
	}
	return store.View2(s.cells, ref.scope, func(st *State) (*lambdaapi.FunctionConfiguration, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return nil, err
		}
		if err := checkCodeWithLayers(code, f.Latest.Config.Layers); err != nil {
			return nil, err
		}
		return new(renderConfig(ref.scope, &f, &f.Latest, ref.arn(), false)), nil
	})
}

func (s *Service) deleteFunction(ctx context.Context, c *kernel.Call, in *lambdaapi.DeleteFunctionInput) (*lambdaapi.DeleteFunctionOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	// $LATEST and aliases are refused before the function is looked up:
	// AWS answers them for a function that does not exist too
	// (test_ops_with_arn_qualifier_mismatch[delete_function],
	// test_function_name_and_qualifier_validation).
	switch {
	case ref.qualifier == latest:
		return nil, errLatestNotDeletable()
	case isAliasName(ref.qualifier):
		return nil, errAliasNotDeletable()
	}
	released, err := write(s, c, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) ([]blob.Ref, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return nil, err
		}
		switch q := ref.qualifier; {
		case q == "":
			versions := f.all()
			for i := range versions {
				s.disarmProvisioning(tx, ref.scope, ref.name, versions[i].Number)
			}
			st.retireURLs(slices.Collect(maps.Values(f.URLs))...)
			st.retireFunction(&f)
			return f.blobRefs(), nil
		case !isVersionNumber(q):
			return nil, ref.notFound()
		}
		return s.deleteVersion(st, tx, ref, f)
	})
	if err != nil {
		return nil, err
	}
	s.release(ctx, released...)
	touch(c, ref.arn())
	return &lambdaapi.DeleteFunctionOutput{}, nil
}

// deleteVersion deletes the published version ref names, unless an alias
// routes to it (lambda-api.md 6.4), and returns the blob references it held.
// A version that does not exist is deleted already: AWS answers 204
// (test_delete_on_nonexisting_version).
func (s *Service) deleteVersion(st *State, tx *kernel.Tx, ref functionRef, f Function) ([]blob.Ref, *awserr.Error) {
	v, ok := f.version(ref.qualifier)
	if !ok {
		tx.Unchanged()
		return nil, nil
	}
	for _, name := range slices.Sorted(maps.Keys(f.Aliases)) {
		a := f.Aliases[name]
		if _, weighted := a.Weights[v.Number]; a.Version == v.Number || weighted {
			return nil, errVersionAliased(v.Number, name)
		}
	}
	f = f.clone()
	f.Versions = slices.DeleteFunc(f.Versions, func(x Version) bool { return x.Number == v.Number })
	delete(f.Policies, v.Number)
	delete(f.EventInvoke, v.Number)
	s.disarmProvisioning(tx, ref.scope, ref.name, v.Number)
	st.Functions[ref.name] = f
	return v.blobRefs(), nil
}

func (s *Service) getFunctionCodeSigningConfig(_ context.Context, c *kernel.Call, in *lambdaapi.GetFunctionCodeSigningConfigInput) (*lambdaapi.GetFunctionCodeSigningConfigOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	return store.View2(s.cells, ref.scope, func(st *State) (*lambdaapi.GetFunctionCodeSigningConfigOutput, *awserr.Error) {
		if _, err := st.function(ref); err != nil {
			return nil, err
		}
		// Without a config AWS answers neither member (lambda/R07/01-get-code-signing-config).
		return &lambdaapi.GetFunctionCodeSigningConfigOutput{}, nil
	})
}

func (s *Service) getAccountSettings(_ context.Context, c *kernel.Call, _ *lambdaapi.GetAccountSettingsInput) (*lambdaapi.GetAccountSettingsOutput, *awserr.Error) {
	return store.View2(s.cells, c.Scope(), func(st *State) (*lambdaapi.GetAccountSettingsOutput, *awserr.Error) {
		usage := &lambdaapi.AccountUsage{FunctionCount: int64(len(st.Functions))}
		for name := range st.Functions {
			versions := st.Functions[name].all()
			for i := range versions {
				if z, ok := versions[i].Code.(ZipCode); ok {
					usage.TotalCodeSize += z.Size
				}
			}
		}
		return &lambdaapi.GetAccountSettingsOutput{
			AccountLimit: &lambdaapi.AccountLimit{
				TotalCodeSize: accountCodeStorage, CodeSizeUnzipped: maxUnzipped, CodeSizeZipped: maxZipped,
				ConcurrentExecutions: accountConcurrency, UnreservedConcurrentExecutions: new(st.unreserved("")),
			},
			AccountUsage: usage,
		}, nil
	})
}

// unreserved is the account's concurrency left after the reservation of
// every function other than the one named except.
func (st *State) unreserved(except string) int32 {
	n := int32(accountConcurrency)
	for name := range st.Functions {
		if r := st.Functions[name].Reserved; r != nil && name != except {
			n -= *r
		}
	}
	return n
}

// initialStatus is a new version's status: Pending while its image is
// made present, Pending until the settle timer under TransitionsClock,
// else Active at once.
func (s *Service) initialStatus(code Code, now clock.Instant) Status {
	switch {
	case isImageAwaited(code):
		return PullingImage{}
	case s.clockTransitions():
		return Creating{Settles: now.Add(settleDelay)}
	}
	return Active{Update: Successful{}}
}

// isImageAwaited reports whether code is image code whose image is not
// present yet.
func isImageAwaited(code Code) bool {
	img, ok := code.(ImageCode)
	return ok && img.Digest == ""
}

// updatedStatus is $LATEST's status after an update applied in place:
// InProgress, still serving what $LATEST was, until the settle timer
// under TransitionsClock, else Successful at once.
func (s *Service) updatedStatus(now clock.Instant, was Serving) Status {
	if s.clockTransitions() {
		return Active{Update: Updating{Settles: now.Add(settleDelay), Serving: was}}
	}
	return Active{Update: Successful{}}
}

// mutable refuses a change to $LATEST while it is provisioned or updated:
// ResourceConflictException, as the SDKs and Terraform retry it
// (lambda-api.md 5.2).
func mutable(arn string, v *Version) *awserr.Error {
	switch st := v.Status.(type) {
	case Creating, PullingImage:
		return errFunctionState(lambdaapi.StatePending)
	case Failed:
		return errFunctionState(lambdaapi.StateFailed)
	case Active:
		switch st.Update.(type) {
		case Updating, UpdatingImage:
			return errUpdateInProgress(arn)
		case Successful, UpdateFailed:
		}
	}
	return nil
}

// onSettle is TimerSettle's handler: a version or mapping whose
// provisioning state was due to end at due reaches its final state.
func (s *Service) onSettle(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	ctx := context.Background()
	a, err := aws.ParseARN(key.ID)
	scope, serr := aws.ScopeOf(a)
	if err != nil || serr != nil {
		s.k.Log().LogAttrs(ctx, slog.LevelError, "Lambda settle timer names no resource", slog.String("timer", key.ID))
		return
	}
	released, werr := write(s, c, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) ([]blob.Ref, *awserr.Error) {
		changed := false
		var released []blob.Ref
		if uuid, ok := strings.CutPrefix(a.Resource, "event-source-mapping:"); ok {
			changed = st.settleMapping(uuid, due)
		} else if v, ok := parseVersionID(key.ID); ok {
			released, changed = st.settleVersion(scope, v.name, v.number, due)
		}
		if !changed {
			tx.Unchanged()
		}
		return released, nil
	})
	if werr != nil {
		s.k.Log().LogAttrs(ctx, slog.LevelError, "settling a Lambda resource failed", slog.String("resource", key.ID), slog.String("error", werr.Message))
		return
	}
	s.release(ctx, released...)
}

// settleVersion ends a version's Creating or Updating that was due by due,
// and returns the blob references of what a completed update no longer
// serves (Serving.blobRefs). The version gets a new RevisionId, as AWS's changes when
// a function becomes Active and when an update completes
// (test_function_revisions_basic).
func (st *State) settleVersion(scope aws.Scope, name, number string, due clock.Instant) (released []blob.Ref, changed bool) {
	f, ok := st.Functions[name]
	if !ok {
		return nil, false
	}
	v, ok := f.version(number)
	if !ok {
		return nil, false
	}
	switch status := v.Status.(type) {
	case Creating:
		if due.Before(status.Settles) {
			return nil, false
		}
	case Active:
		u, ok := status.Update.(Updating)
		if !ok || due.Before(u.Settles) {
			return nil, false
		}
		released = u.Serving.blobRefs()
	case PullingImage, Failed:
		return nil, false
	}
	v.Status, v.RevisionID = Active{Update: Successful{}}, st.revision(scope)
	st.Functions[name] = f.withVersion(v)
	return released, true
}
