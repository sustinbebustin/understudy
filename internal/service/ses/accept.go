package ses

import (
	"context"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// outgoing is a send that passed the checks needing no state (SendChecks up
// to CheckRecipientCount), ready for the configuration-set, sender, sandbox
// and daily-quota checks.
type outgoing struct {
	api API
	// configurationSet is the configuration set the send names, by member
	// or raw header, nil when none.
	configurationSet *string
	// senders are the addresses that must be verified, as given, in the
	// order the unverified text lists them: Source or From, then ReturnPath
	// (ses.md 6.1, 11.3).
	senders []string
	// envelope is the recipients the sandbox check and the counter use, as
	// given, in request order.
	envelope []string
	// prepare, when set, reads what the send needs from the scope's state
	// before the identity checks: a templated send's stored template.
	prepare func(st *state) *refusal
	// fill writes the message's own fields into its record.
	fill func(r *record)
	// refuse is the answer of a failed check in the send's API.
	refuse func(r *refusal) *awserr.Error
}

// accept runs the configuration-set check, o.prepare, then the sender,
// sandbox-recipient and daily-quota checks, in SendChecks order, on the
// caller's scope; a send that passes them is counted and recorded, and
// accept returns its MessageId. Nothing changes when a check fails.
func (s *Service) accept(ctx context.Context, c *kernel.Call, o outgoing) (string, *awserr.Error) {
	type accepted struct {
		id       string
		released []blob.Ref
	}
	scope := c.Scope()
	got, err := kernel.Write(c, s.cells, scope, func(st *state, now clock.Instant, _ *kernel.Tx) (accepted, *awserr.Error) {
		if r := s.checkBeforeIdentities(c, st, o); r != nil {
			return accepted{}, o.refuse(r)
		}
		production := s.opts.ProductionAccess.allows(scope)
		if r := s.checkIdentities(st, scope.Region, production, o); r != nil {
			return accepted{}, o.refuse(r)
		}
		counted, simulated := tally(o.envelope)
		if !production && float64(st.sentLast24Hours(now)+counted) > SandboxMax24HourSend {
			return accepted{}, o.refuse(&refusal{class: refusalThrottled, text: DailyQuotaText})
		}
		st.count(now, counted)
		r := s.newRecord(c, o.api, KindSend, now)
		r.Envelope = slices.Clone(o.envelope)
		r.Simulator = simulated
		if o.configurationSet != nil {
			r.ConfigurationSet = *o.configurationSet
		}
		o.fill(&r)
		return accepted{id: r.MessageID, released: st.record(r)}, nil
	})
	if err != nil {
		return "", err
	}
	s.release(ctx, got.released...)
	return got.id, nil
}

// checkBeforeIdentities runs the state checks that precede the sender's:
// CheckConfigurationSet, touching the named set's ARN when it exists, then
// o.prepare.
func (s *Service) checkBeforeIdentities(c *kernel.Call, st *state, o outgoing) *refusal {
	if o.configurationSet != nil {
		arn, r := st.checkConfigurationSet(s.view(o.api), c.Scope(), *o.configurationSet)
		if r != nil {
			return r
		}
		c.Touch(arn)
	}
	if o.prepare != nil {
		return o.prepare(st)
	}
	return nil
}

// tally is how many envelope recipients the quota counts, every one but
// the mailbox simulator's, and the scenario of each simulator recipient,
// nil when there is none (ses.md 6.2, 6.3).
func tally(envelope []string) (counted int, simulated map[string]Scenario) {
	for _, to := range envelope {
		sc, ok := simulatorScenario(addrSpec(to))
		if !ok {
			counted++
			continue
		}
		if simulated == nil {
			simulated = map[string]Scenario{}
		}
		simulated[to] = sc
	}
	return counted, simulated
}

// checkIdentities runs CheckSender and, in the sandbox,
// CheckSandboxRecipients. Every sender must be verified (ses.md 6.1, 5.1);
// in the sandbox (production unset) every envelope recipient must be
// verified or a simulator address (ses.md 6.1, 6.2). Both failures answer
// one MessageRejected listing each failing identity as given, ordered per
// switch ses.error.unverified.text.
func (s *Service) checkIdentities(st *state, region aws.Region, production bool, o outgoing) *refusal {
	var senders, recipients []string
	v := s.view(o.api)
	for _, a := range o.senders {
		if !st.verified(v, addrSpec(a), true) {
			senders = append(senders, a)
		}
	}
	if !production {
		for _, a := range o.envelope {
			spec := addrSpec(a)
			if _, sim := simulatorScenario(spec); !sim && !st.verified(v, spec, SandboxRecipientLabels) {
				recipients = append(recipients, a)
			}
		}
	}
	if len(senders) == 0 && len(recipients) == 0 {
		return nil
	}
	var failed []string
	switch s.opts.UnverifiedText {
	case UnverifiedHashOrder:
		failed = hashSetOrder(slices.Concat(senders, recipients))
	case UnverifiedSendersFirst:
		failed = slices.Concat(senders, recipients)
	case UnverifiedRecipientsFirst:
		failed = slices.Concat(recipients, senders)
	}
	return &refusal{class: refusalRejected, text: UnverifiedTextPrefix + strings.ToUpper(region.String()) + ": " + strings.Join(failed, ", ")}
}

// verified reports whether addr-spec addr passes the identity rules
// (ses.md 5.1) in v: with any +label removed from its local part when
// labels is set, it equals a Success email identity, byte for byte, or its
// domain equals or is a subdomain of a Success domain identity, regardless
// of case.
func (s *state) verified(v view, addr string, labels bool) bool {
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return false
	}
	local, domain := addr[:at], strings.ToLower(addr[at+1:])
	if labels {
		local, _, _ = strings.Cut(local, "+")
	}
	exact := local + addr[at:]
	for i := range s.Identities {
		id := &s.Identities[i]
		if _, ok := id.Status.(statusSuccess); !ok || !v.sees(id) {
			continue
		}
		switch id.Kind {
		case sesapi.IdentityTypeEmailAddress:
			if id.Name == exact {
				return true
			}
		case sesapi.IdentityTypeDomain:
			d := strings.ToLower(id.Name)
			if domain == d || strings.HasSuffix(domain, "."+d) {
				return true
			}
		}
	}
	return false
}

// simulatorScenario is the scenario of a mailbox simulator addr-spec: the
// domain SimulatorDomain regardless of case, and the local part, with any
// +label removed, exactly one of Scenarios (ses.md 6.2;
// ses.simulator.addresses, SES-19).
func simulatorScenario(addr string) (Scenario, bool) {
	at := strings.LastIndexByte(addr, '@')
	if at < 0 || !strings.EqualFold(addr[at+1:], SimulatorDomain) {
		return "", false
	}
	local, _, _ := strings.Cut(addr[:at], "+")
	if i := slices.Index(Scenarios(), Scenario(local)); i >= 0 {
		return Scenarios()[i], true
	}
	return "", false
}
