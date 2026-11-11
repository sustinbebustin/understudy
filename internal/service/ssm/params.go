package ssm

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/serviceevent"
	"github.com/sustinbebustin/understudy/internal/store"
)

// defaultDataType is the DataType of a parameter written without one
// (ssm-parameters.md 4.3).
const defaultDataType = "text"

// errAlreadyExists is PutParameter's answer for an existing name without
// Overwrite (ssm-parameters.md 12.4).
func errAlreadyExists() *awserr.Error {
	return ssmapi.ErrParameterAlreadyExists("The parameter already exists. To overwrite this value, set the overwrite option in the request to true.")
}

// errNotFound is ParameterNotFound with no message, as AWS sends it to
// GetParameter, DeleteParameter and GetParameterHistory (ssm-parameters.md
// 2.2; SSM-01 step 01, SSM-02 steps 01-02).
func errNotFound() *awserr.Error { return ssmapi.ErrParameterNotFound("") }

// lookup copies out the parameter canonical of scope.
func (s *Service) lookup(scope aws.Scope, canonical string) (Parameter, bool) {
	return store.View2(s.cells, scope, func(st *State) (Parameter, bool) { p, ok := st.Parameters[canonical]; return p, ok })
}

// latest is the parameter's newest version. A stored parameter always has
// one.
func (p *Parameter) latest() Version { return p.Versions[len(p.Versions)-1] }

// PutParameter limits (ssm-parameters.md 6.1, 7.9).
const (
	// maxVersions is how many versions a parameter retains.
	maxVersions = 100
	// maxTags is how many tags a parameter can carry.
	maxTags = 50
)

// errTagsWithOverwrite is PutParameter's answer for Tags with Overwrite
// true (ssm-parameters.md 7.1, 12.4), for a name that does not exist yet
// too (SSM-03 steps 02-03).
func errTagsWithOverwrite() *awserr.Error {
	return ssmapi.ErrValidationException("Invalid request: tags and overwrite can't be used together. " +
		"To create a parameter with tags, please remove overwrite flag. " +
		"To update tags for an existing parameter, please use AddTagsToResource or RemoveTagsFromResource.")
}

// putParameter creates a parameter, or with Overwrite writes its next
// version. A SecureString value is encrypted through KMS after every other
// check passes and before the cell is taken: the
// request is checked against the stored parameter once to learn the key,
// then again in the cell, which refuses with TooManyUpdates if a
// concurrent write changed the key or type in between.
func (s *Service) putParameter(ctx context.Context, c *kernel.Call, in *ssmapi.PutParameterInput) (*ssmapi.PutParameterOutput, *awserr.Error) {
	const op = "PutParameter"
	if err := checkReserved(op, in.Name); err != nil {
		return nil, err
	}
	scope := c.Scope()
	n, err := parseName(in.Name, askerOf(c))
	if err != nil {
		return nil, err
	}
	overwrite := in.Overwrite != nil && *in.Overwrite
	switch {
	case in.Policies != nil:
		return nil, errUnsupported(op, "parameter Policies")
	case in.DataType != nil && *in.DataType == dataTypeEC2Image:
		return nil, errUnsupported(op, "DataType aws:ec2:image, since understudy has no EC2 to validate an AMI,")
	case len(in.Tags) > 0 && overwrite:
		return nil, errTagsWithOverwrite()
	}
	tags := addTags(nil, in.Tags)
	if len(tags) > maxTags {
		return nil, ssmapi.ErrTooManyTagsError()
	}
	// LastModifiedUser is the caller's principal ARN as SigV4 resolved it,
	// the assumed-role session ARN for a role session (ssm-parameters.md
	// 9.1; SSM-24).
	w := write{op: op, name: n, in: in, tags: tags, at: c.Admitted(), user: identity.Render(c.Principal())}
	planned, err := store.View2(s.cells, scope, func(st *State) (Parameter, *awserr.Error) { return w.apply(st, overwrite) })
	if err != nil {
		return nil, err
	}
	var sealed *sealedValue
	if sv, ok := planned.latest().Value.(sealedValue); ok {
		v, err := s.seal(ctx, c, scope, n.canonical, sv.KeyRef, in.Value)
		if err != nil {
			return nil, err
		}
		sealed = &v
	}
	var queued *serviceevent.Queued
	v, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (Version, *awserr.Error) {
		p, err := w.apply(st, overwrite)
		if err == nil {
			err = w.fillSealed(&p.Versions[len(p.Versions)-1], sealed)
		}
		if err != nil {
			tx.Unchanged()
			return Version{}, err
		}
		op := opCreate
		if _, exists := st.Parameters[n.canonical]; exists {
			op = opUpdate
		}
		st.Parameters[n.canonical] = p
		queued = s.events.Push(c, clock.Instant{}, changeEvent(scope, &p, op, nil))
		return p.latest(), nil
	})
	s.events.Flush(ctx, queued)
	if err != nil {
		return nil, err
	}
	c.Touch(parameterARN(scope, n.canonical))
	return &ssmapi.PutParameterOutput{Version: v.Number, Tier: new(ssmapi.ParameterTier(v.Tier))}, nil
}

// write is one PutParameter request, validated up to what needs the stored
// parameter.
type write struct {
	op   string
	name name
	in   *ssmapi.PutParameterInput
	tags []Tag
	at   clock.Instant
	user string
}

// apply is the parameter st holds after the write, without storing it.
// st is not modified. A SecureString version's value is a sealedValue
// naming its key with no ciphertext yet; fillSealed supplies it.
func (w write) apply(st *State, overwrite bool) (Parameter, *awserr.Error) {
	prev, exists := st.Parameters[w.name.canonical]
	switch {
	case !exists:
		return w.create(st)
	case !overwrite:
		return Parameter{}, errAlreadyExists()
	}
	return w.next(st, prev)
}

// fillSealed puts the ciphertext KMS returned into v, the version apply
// built in the cell. sealed is nil when the check before encrypting found
// no SecureString. A key or type that differs from that check's means a
// concurrent write changed the parameter in between; nothing is written.
func (w write) fillSealed(v *Version, sealed *sealedValue) *awserr.Error {
	want, secure := v.Value.(sealedValue)
	switch {
	case !secure && sealed == nil:
		return nil
	case secure && sealed != nil && sealed.KeyRef == want.KeyRef:
		v.Value = *sealed
		return nil
	}
	return ssmapi.ErrTooManyUpdates(fmt.Sprintf(
		"Parameter %q changed type or KMS key while this request encrypted its value, so nothing was written. Try the request again.",
		w.name.spelling))
}

// create is the parameter a create writes: version 1, with DataType text
// unless given (ssm-parameters.md 4.3).
func (w write) create(st *State) (Parameter, *awserr.Error) {
	if w.in.Type == nil {
		// ssm-parameters.md 7.1; SSM-11 step 01.
		return Parameter{}, ssmapi.ErrValidationException("A parameter type is required when you create a parameter.")
	}
	t, err := w.requestType()
	if err != nil {
		return Parameter{}, err
	}
	v, err := w.version(st, Version{Number: 1, Type: t, DataType: defaultDataType}, nil)
	if err != nil {
		return Parameter{}, err
	}
	return Parameter{Name: w.name.canonical, Versions: []Version{v}, LastVersion: 1, Labels: map[string]int64{}, Tags: w.tags}, nil
}

// requestType is the request's Type, which is present, in the closed set.
// The generated enum check refuses any other value before the handler
// runs, so one reaching here is a bug in that check.
func (w write) requestType() (paramType, *awserr.Error) {
	t, ok := parseType(*w.in.Type)
	if !ok {
		return "", awserr.Internalf(fmt.Errorf("ssm: PutParameter Type %q passed the generated enum check but is not String, StringList or SecureString", *w.in.Type))
	}
	return t, nil
}

// next is prev with the overwrite's version appended. Every overwrite
// creates a version, one identical to the latest included
// (ssm-parameters.md 6.1; SSM-08 step 01). Past 100 versions the oldest is
// dropped, unless a label names it, which AWS refuses naming the parameter
// without its leading slash and with a curly apostrophe; numbers keep
// counting from LastVersion (ssm-parameters.md 6.1, 12.4; SSM-30).
func (w write) next(st *State, prev Parameter) (Parameter, *awserr.Error) {
	base := prev.latest()
	base.Number = prev.LastVersion + 1
	v, err := w.version(st, base, &base.Tier)
	if err != nil {
		return Parameter{}, err
	}
	kept := prev.Versions
	if len(kept) >= maxVersions {
		oldest := kept[0].Number
		for _, labeled := range prev.Labels {
			if labeled == oldest {
				return Parameter{}, ssmapi.ErrParameterMaxVersionLimitExceeded(fmt.Sprintf(
					"You attempted to create a new version of %s by calling the PutParameter API with the overwrite flag. "+
						"Version %d, the oldest version, can’t be deleted because it has a label associated with it. "+
						"Move the label to another version of the parameter, and try again.", strings.TrimPrefix(prev.Name, "/"), oldest))
			}
		}
		kept = kept[len(kept)-maxVersions+1:]
	}
	p := prev
	p.Versions = append(slices.Clone(kept), v)
	p.LastVersion = v.Number
	return p, nil
}

// version is base with the request's fields applied, checked in order: a
// KeyId on a type other than SecureString, the DataType, a nested reference
// in the value, then valueRules. Under switch ssm.put.omitted-fields
// (preserved) a field
// the request leaves out keeps base's value, and an empty AllowedPattern
// clears it (ssm-parameters.md 7.1; SSM-10). UNVERIFIED (SSM-10),
// ssm-parameters.md 7.1: an empty Description clears it too. current is the
// parameter's tier, nil on a create. A type change is accepted in a
// hierarchy as at the root (ssm-parameters.md 4.1; SSM-09), and the
// generated length check has refused an empty Value (SSM-11 step 03).
func (w write) version(st *State, base Version, current *tier) (Version, *awserr.Error) {
	in := w.in
	v := base
	v.LastModified, v.LastModifiedUser = w.at, w.user
	if in.Type != nil {
		t, err := w.requestType()
		if err != nil {
			return Version{}, err
		}
		v.Type = t
	}
	v.Value = plainValue{Text: in.Value}
	if v.Type == typeSecureString {
		v.Value = sealedValue{KeyRef: keyRefFor(in.KeyId, base.Value)}
	}
	if in.Description != nil {
		v.Description = nil
		if *in.Description != "" {
			v.Description = new(*in.Description)
		}
	}
	if in.AllowedPattern != nil {
		v.AllowedPattern = nil
		if *in.AllowedPattern != "" {
			v.AllowedPattern = new(*in.AllowedPattern)
		}
	}
	if in.DataType != nil {
		v.DataType = *in.DataType
	}
	if in.KeyId != nil && v.Type != typeSecureString {
		// ssm-parameters.md 7.1; SSM-11 step 02.
		return Version{}, ssmapi.ErrValidationException("KeyId is required for SecureString type parameter only.")
	}
	if in.DataType != nil {
		if err := w.checkDataType(*in.DataType); err != nil {
			return Version{}, err
		}
	}
	if hasReference(in.Value) {
		// AWS's text quotes the value after "Parameter value:" (SSM-11
		// step 05); understudy leaves the value out, as it does from every
		// message (ssm-parameters.md 4.1, 12.4).
		return Version{}, ssmapi.ErrValidationException(
			`Parameter value failed to satisfy constraint: Parameter value can't nest another parameter. Do not use "{{}}" in the value.`)
	}
	return w.valueRules(st, v, current)
}

// valueRules resolves v's tier and checks the value against it, the
// pattern and the scope's parameter count.
func (w write) valueRules(st *State, v Version, current *tier) (Version, *awserr.Error) {
	t, err := w.resolveTier(st, current)
	if err != nil {
		return Version{}, err
	}
	v.Tier = t
	if v.Type == typeSecureString && t == tierAdvanced {
		return Version{}, errUnsupported(w.op, "an Advanced-tier SecureString (understudy stores only Standard SecureString values)")
	}
	if err := w.checkSize(t); err != nil {
		return Version{}, err
	}
	if v.AllowedPattern != nil {
		p, err := w.compilePattern(w.op, *v.AllowedPattern)
		if err != nil {
			return Version{}, err
		}
		if err := w.checkPattern(p); err != nil {
			return Version{}, err
		}
	}
	if current == nil || *current != t {
		if err := w.checkCount(st, t); err != nil {
			return Version{}, err
		}
	}
	return v, nil
}

// hasReference reports a value holding {{ followed later by }}, which a
// parameter value can't contain (ssm-parameters.md 4.1 [D]).
func hasReference(value string) bool {
	_, after, ok := strings.Cut(value, "{{")
	return ok && strings.Contains(after, "}}")
}

// deleteParameter drops a parameter with its versions, labels and tags.
// The name can be created again at once, as version 1 with no tags
// (ssm-parameters.md 7.7; SSM-26).
func (s *Service) deleteParameter(ctx context.Context, c *kernel.Call, in *ssmapi.DeleteParameterInput) (*ssmapi.DeleteParameterOutput, *awserr.Error) {
	if err := checkReserved("DeleteParameter", in.Name); err != nil {
		return nil, err
	}
	scope := c.Scope()
	n, err := parseName(in.Name, askerOf(c))
	if err != nil {
		return nil, err
	}
	var queued *serviceevent.Queued
	_, err = kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		p, ok := st.Parameters[n.canonical]
		if !ok {
			tx.Unchanged()
			return struct{}{}, errNotFound()
		}
		delete(st.Parameters, n.canonical)
		queued = s.events.Push(c, clock.Instant{}, changeEvent(scope, &p, opDelete, nil))
		return struct{}{}, nil
	})
	s.events.Flush(ctx, queued)
	if err != nil {
		return nil, err
	}
	c.Touch(parameterARN(scope, n.canonical))
	return &ssmapi.DeleteParameterOutput{}, nil
}

// deleteParameters deletes up to ten parameters by name
// (ssm-parameters.md 7.7): those deleted, as the request spelled them
// (trimmed), in byte order; those not found or not valid names, as given,
// in InvalidParameters, sorted. Both are [] rather than absent when empty
// (7.10; SSM-27). UNVERIFIED (SSM-27), ssm-parameters.md 7.7: a parameter
// named twice, under any of its spellings (3.2), is deleted and listed
// once, as the first spelling asked. Each deleted parameter sends its own
// Delete event, in request order (inferred: SSM-31 recorded
// DeleteParameter only).
func (s *Service) deleteParameters(ctx context.Context, c *kernel.Call, in *ssmapi.DeleteParametersInput) (*ssmapi.DeleteParametersOutput, *awserr.Error) {
	if err := checkReserved("DeleteParameters", in.Names...); err != nil {
		return nil, err
	}
	scope, a := c.Scope(), askerOf(c)
	type outcome struct {
		deleted []name
		invalid []string
	}
	var last *serviceevent.Queued
	o, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (outcome, *awserr.Error) {
		o := outcome{invalid: []string{}}
		seen := make(map[string]bool, len(in.Names))
		gone := make(map[string]bool, len(in.Names))
		for _, raw := range in.Names {
			asked := strings.Trim(raw, " ")
			if seen[asked] {
				continue
			}
			seen[asked] = true
			n, err := parseName(asked, a)
			if err == nil && gone[n.canonical] {
				continue
			}
			p, ok := st.Parameters[n.canonical]
			if err != nil || !ok {
				o.invalid = append(o.invalid, asked)
				continue
			}
			delete(st.Parameters, n.canonical)
			gone[n.canonical] = true
			o.deleted = append(o.deleted, n)
			last = s.events.Push(c, clock.Instant{}, changeEvent(scope, &p, opDelete, nil))
		}
		if len(o.deleted) == 0 {
			tx.Unchanged()
		}
		return o, nil
	})
	s.events.Flush(ctx, last)
	if err != nil {
		return nil, err
	}
	deleted := make([]string, 0, len(o.deleted))
	for _, n := range o.deleted {
		deleted = append(deleted, n.spelling)
		c.Touch(parameterARN(scope, n.canonical))
	}
	slices.Sort(deleted)
	slices.Sort(o.invalid)
	return &ssmapi.DeleteParametersOutput{DeletedParameters: deleted, InvalidParameters: o.invalid}, nil
}
