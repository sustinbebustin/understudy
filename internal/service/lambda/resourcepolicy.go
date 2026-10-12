package lambda

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the permissions of the function target names,
// arn:...:function:<name>[:<qualifier>], as the policy GetPolicy returns,
// for the delivery access advisory (outbox.PolicySource, ARCHITECTURE.md
// 16); nil when the function or qualifier has no statements. ok is false
// when no such function exists, which skips the advisory.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	rest, isFunction := strings.CutPrefix(target.Resource, "function:")
	scope, inScope := scopeOf(target)
	if target.Service != "lambda" || !isFunction || !inScope {
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
	if !f.ok || f.text == "" {
		return nil, f.ok
	}
	parsed, err := policydoc.Parse([]byte(f.text))
	if err != nil {
		return nil, true
	}
	return &parsed, true
}
