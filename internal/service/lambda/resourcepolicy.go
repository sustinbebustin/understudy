package lambda

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the permissions of what target names
// (authz.PolicyProvider, ARCHITECTURE.md 16): for a function,
// arn:...:function:<name>[:<qualifier>], the policy GetPolicy returns; for
// a layer version, arn:...:layer:<name>:<version>, the policy
// GetLayerVersionPolicy returns (lambda-destinations-layers.md 7.4). doc is
// nil when there are no statements; ok is false when no such function or
// layer version exists, which skips the delivery access advisory.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	scope, serr := aws.ScopeOf(target)
	if target.Service != "lambda" || serr != nil {
		return nil, false
	}
	if strings.HasPrefix(target.Resource, "layer:") {
		return s.layerVersionPolicy(target)
	}
	rest, isFunction := strings.CutPrefix(target.Resource, "function:")
	if !isFunction {
		return nil, false
	}
	name, qualifier, _ := strings.Cut(rest, ":")
	type found struct {
		text string
		ok   bool
	}
	f, _ := view(s, scope, func(st *State) (found, *awserr.Error) {
		fn, ok := st.Functions[name]
		if !ok {
			return found{}, nil
		}
		p, ok := fn.Policies[qualifier]
		if !ok || len(p.Statements) == 0 {
			return found{ok: true}, nil
		}
		return found{text: p.document(), ok: true}, nil
	})
	if !f.ok {
		return nil, false
	}
	return policydoc.ParseStored(f.text), true
}

// layerVersionPolicy is ResourcePolicy for a layer version ARN.
func (s *Service) layerVersionPolicy(target aws.ARN) (*policydoc.Document, bool) {
	ref, n, ok := parseLayerVersionARN(target.String())
	if !ok {
		return nil, false
	}
	type found struct {
		text string
		ok   bool
	}
	f, _ := view(s, ref.scope, func(st *State) (found, *awserr.Error) {
		v, ok := st.layerVersion(ref, n)
		return found{text: v.policyDocument(), ok: ok}, nil
	})
	switch {
	case !f.ok:
		return nil, false
	case f.text == "":
		return nil, true
	}
	return policydoc.ParseStored(f.text), true
}
