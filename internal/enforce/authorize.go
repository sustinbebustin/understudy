package enforce

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Authorize implements kernel.Authorizer: each pair of q is decided by
// authz.Decide in order, with the condition keys q supplies for the pair's
// action (kernel.AuthzQuery.KeysFor), every condition key understudy cannot supply is
// journaled, and the first denying pair ends the question: answered with
// the service's denial under Enforce, journaled and served under Advise
// (iam-enforcement-surfaces.md 12.1, 12.2). Under Enforce a pair whose
// statements test one of q.Refused ends the call Unsupported first, whatever
// the decision, since that decision took the key as absent. A pair it cannot decide, such
// as on a stored policy that does not parse, ends the call Internal under
// Enforce and is journaled and passed over under Advise; it is never read as
// no policy (12.10). Unbound, it answers a nil Verdict, which the kernel
// reports as VerdictError.
func (a *Authorizer) Authorize(ctx context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	s := a.sources.Load()
	if s == nil {
		return nil
	}
	principal := c.Principal()
	ids, idErr := s.identity(principal)
	base := authz.ContextInput{Now: c.Admitted(), Region: callRegion(c), Via: via(c)}
	base.UserAgent, _ = c.Header("User-Agent")
	base.Referer, _ = c.Header("Referer")
	for _, pair := range q.Pairs {
		// An identity-based policy that cannot be read makes every pair
		// unevaluable.
		err := idErr
		var decided evaluation
		if err == nil {
			in := base
			in.Keys = q.KeysFor(pair.Action)
			decided, err = s.evaluate(request{principal: principal, ids: ids, pair: pair, account: c.Scope().Account, context: in})
		}
		if err != nil {
			cause := &UnevaluableError{Pair: pair, Cause: err}
			if a.mode == Enforce {
				return kernel.Unevaluable{Cause: cause}
			}
			c.Record(ctx, c.Scope(), journal.Advisory{Kind: journal.AdvisoryAccessUnevaluated, Resource: pairARN(pair), Text: unevaluatedText(principal, pair, err)})
			continue
		}
		for _, u := range decided.outcome.Unsupplied {
			c.Record(ctx, c.Scope(), journal.Advisory{Kind: journal.AdvisoryConditionKeyUnsupplied, Resource: pairARN(pair), Text: unsuppliedText(u, pair)})
		}
		if u, ok := refused(decided.outcome.Unsupplied, q.Refused); ok && a.mode == Enforce {
			return kernel.Unsupported{Message: refusedText(u, pair)}
		}
		if _, allowed := decided.outcome.Decision.(authz.Allow); allowed {
			continue
		}
		message := decided.denial.Message(grammar(serviceOf(c), pair.Action))
		if a.mode != Enforce {
			c.Record(ctx, c.Scope(), journal.Advisory{Kind: journal.AdvisoryAccessWouldDeny, Resource: pairARN(pair), Text: deniedText(message, decided, pair) + servedUnderAdvise})
			return kernel.Allowed{}
		}
		c.Record(ctx, c.Scope(), journal.Advisory{Kind: journal.AdvisoryAccessDenied, Resource: pairARN(pair), Text: deniedText(message, decided, pair)})
		return kernel.Denied{Message: message}
	}
	return kernel.Allowed{}
}

// Evaluate decides r's pairs as Authorize would, without a call: nothing is
// journaled, the context has no request keys and no user agent, the region
// is the resource ARN's (us-east-1 without one) and the clock is
// Sources.Clock. Each message is in the grammar of the action's service. It
// returns an *UnknownPrincipalError for a principal that does not exist and
// an *UnevaluableError for a pair it cannot decide.
func (a *Authorizer) Evaluate(_ context.Context, r EvaluateRequest) ([]Evaluation, error) {
	s := a.sources.Load()
	if s == nil {
		return nil, &UnboundError{}
	}
	principal, ok := s.Identities.PrincipalOf(r.Principal)
	if !ok {
		return nil, &UnknownPrincipalError{ARN: r.Principal.String()}
	}
	ids, idErr := s.identity(principal)
	if replaced, ok := r.Identity.(ReplacedIdentity); ok {
		ids.Policies, ids.Session = replaced.Policies, nil
	}
	account, _ := identity.AccountOf(principal)
	var now clock.Instant
	if s.Clock != nil {
		now = s.Clock.Now()
	}
	out := make([]Evaluation, 0, len(r.Pairs))
	for _, pair := range r.Pairs {
		if idErr != nil {
			return nil, &UnevaluableError{Pair: pair, Cause: idErr}
		}
		decided, err := s.evaluate(request{
			principal: principal, ids: ids, pair: pair, account: account,
			context: authz.ContextInput{Now: now, Region: arnRegion(pair), Via: authz.Direct{}},
		})
		if err != nil {
			return nil, &UnevaluableError{Pair: pair, Cause: err}
		}
		out = append(out, Evaluation{Pair: pair, Outcome: decided.outcome, Message: decided.denial.Message(grammar(ref.ServiceID(pair.Action.Prefix), pair.Action))})
	}
	return out, nil
}
