package ses

import (
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the version of the "ses" snapshot section. Version 2
// added configuration sets and the set a message was sent with; version 3
// added the paused account sending setting.
const stateVersion = 3

// state is the SES state of one account and Region, the "ses" snapshot
// section's data.
type state struct {
	// Identities are in creation order, the order ListIdentities and
	// Resources list them in (SES-14).
	Identities []identity `json:"identities"`
	// Templates are the v1 templates in creation order, the order
	// ListTemplates and Resources list them in.
	Templates []template `json:"templates,omitempty"`
	// ConfigurationSets are the configuration sets of both APIs in
	// creation order, the order the list operations and Resources list
	// them in.
	ConfigurationSets []configurationSet `json:"configurationSets,omitempty"`
	// Messages are the recorded messages, oldest first, at most
	// MaxStoredMessages (ses.md 11.2).
	Messages []record `json:"messages,omitempty"`
	// Evicted counts the records evicted to keep MaxStoredMessages.
	Evicted int `json:"evicted,omitempty"`
	// Recipients counts accepted recipients per minute over the last 24
	// hours, oldest first: SentLast24Hours and the daily quota (ses.md
	// 6.3). Clearing messages leaves it; a reset drops it with the scope.
	Recipients []minuteCount `json:"recipients,omitempty"`
	// SendingPaused is set by UpdateAccountSendingEnabled false; account
	// sending starts enabled, so the zero value is the default.
	SendingPaused bool `json:"sendingPaused,omitempty"`
}

func newState(aws.Scope) state { return state{} }

// IsZero reports a scope without identities, templates, configuration
// sets, messages, evictions, counted recipients or a paused account, which
// snapshots skip.
func (s *state) IsZero() bool {
	return len(s.Identities) == 0 && len(s.Templates) == 0 && len(s.ConfigurationSets) == 0 &&
		len(s.Messages) == 0 && s.Evicted == 0 && len(s.Recipients) == 0 && !s.SendingPaused
}

// minuteCount is the recipients counted in one minute of emulator time,
// Minute being Unix seconds divided by 60.
type minuteCount struct {
	Minute int64 `json:"minute"`
	Count  int   `json:"count"`
}

// quotaWindowMinutes is the 24-hour window quotas count over (ses.md 6.3),
// in minutes.
const quotaWindowMinutes = 24 * 60

// minuteOf is the minute bucket of now.
func minuteOf(now clock.Instant) int64 { return now.Wire().Unix() / 60 }

// sentLast24Hours is the recipients counted in the 24 hours before now, to
// the minute.
func (s *state) sentLast24Hours(now clock.Instant) int {
	oldest := minuteOf(now) - quotaWindowMinutes
	n := 0
	for _, b := range s.Recipients {
		if b.Minute > oldest {
			n += b.Count
		}
	}
	return n
}

// count adds n recipients at now and drops the buckets the window has
// passed.
func (s *state) count(now clock.Instant, n int) {
	m := minuteOf(now)
	s.Recipients = slices.DeleteFunc(s.Recipients, func(b minuteCount) bool { return b.Minute <= m-quotaWindowMinutes })
	if n == 0 {
		return
	}
	if last := len(s.Recipients) - 1; last >= 0 && s.Recipients[last].Minute == m {
		s.Recipients[last].Count += n
		return
	}
	s.Recipients = append(s.Recipients, minuteCount{Minute: m, Count: n})
}

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*state)(nil)

// view is the identities one API's calls see, per switch
// ses.store.shared-v1-v2 (ses.md 2.3, SES-12): every identity of the scope
// when shared, only those the API created when separate.
type view struct {
	api      API
	separate bool
}

// sees reports whether id is in v.
func (v view) sees(id *identity) bool { return v.owns(id.API) }

// owns reports whether what api made is in v.
func (v view) owns(api API) bool { return !v.separate || api == v.api }

// find is the index of the identity in v named name, byte for byte
// (ses.md 9.1), or -1.
func (s *state) find(v view, name string) int {
	for i := range s.Identities {
		if s.Identities[i].Name == name && v.sees(&s.Identities[i]) {
			return i
		}
	}
	return -1
}

// identities is the number of identities in v.
func (s *state) identities(v view) int {
	n := 0
	for i := range s.Identities {
		if v.sees(&s.Identities[i]) {
			n++
		}
	}
	return n
}

// templates is the number of templates in v, which MaxTemplates bounds.
func (s *state) templates(v view) int {
	n := 0
	for i := range s.Templates {
		if v.owns(s.Templates[i].API) {
			n++
		}
	}
	return n
}

// identity is one verified or verifying email address or domain (ses.md
// 5.1).
type identity struct {
	Name   string              `json:"name"`
	Kind   sesapi.IdentityType `json:"kind"`
	Status status              `json:"status"`
	// API is the API whose call created the identity, which switch
	// ses.store.shared-v1-v2 reads when it keeps the APIs apart.
	API API `json:"api"`
	// LinkToken is the token of an email identity's verification link,
	// the same in every verification email it is sent (inferred, SES-09).
	LinkToken string `json:"linkToken,omitempty"`
	// DomainToken is a domain identity's verification token, minted once
	// and answered unchanged on every read and repeated verify (ses.md
	// 9.2; inferred, SES-10).
	DomainToken string `json:"domainToken,omitempty"`
	// DkimTokens are the three Easy DKIM tokens v2 CreateEmailIdentity
	// mints for a domain, stable across reads (ses.md 5.4, 9.3; format
	// inferred, SES-13). Empty for an address and for a domain v1 created.
	DkimTokens []string `json:"dkimTokens,omitempty"`
	// DkimSigningAttributes is what CreateEmailIdentity stored of its
	// DkimSigningAttributes; it signs nothing (StoredOnly, ses.md 11.1).
	DkimSigningAttributes *dkimSigning `json:"dkimSigningAttributes,omitempty"`
	// Tags are the identity's tags in the order they were first set
	// (ses.md 5.4).
	Tags    []tag         `json:"tags,omitempty"`
	Created clock.Instant `json:"created"`
}

// dkimSigning is the part of a stored DkimSigningAttributes GetEmailIdentity
// reads back: whether a Bring Your Own DKIM selector was given, which makes
// SigningAttributesOrigin EXTERNAL. The private key is never
// kept.
type dkimSigning struct {
	Selector string `json:"selector,omitempty"`
}

// tag is one identity tag.
type tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// deadline is when the identity, Pending since since, turns Failed:
// EmailVerificationExpiry after its latest verification email for an
// address, DomainVerificationExpiry after creation for a domain (ses.md
// 5.2, SES-27).
func (id *identity) deadline(since clock.Instant) clock.Instant {
	if id.Kind == sesapi.IdentityTypeDomain {
		return since.Add(DomainVerificationExpiry)
	}
	return since.Add(EmailVerificationExpiry)
}

// view is the identities api's calls see under the configured
// ses.store.shared-v1-v2.
func (s *Service) view(api API) view { return view{api: api, separate: s.opts.Store == StoreSeparate} }

// own is the identities api created, whatever the switch: the view an
// identity's own timer finds it in.
func own(api API) view { return view{api: api, separate: true} }

// findToken is the index of the email identity whose link token is token,
// or -1.
func (s *state) findToken(token string) int {
	for i := range s.Identities {
		if s.Identities[i].LinkToken == token {
			return i
		}
	}
	return -1
}

// identityARN is the ARN of identity name in scope,
// arn:aws:ses:<region>:<account>:identity/<name> (ses.md 5.1).
func identityARN(scope aws.Scope, name string) aws.ARN {
	return aws.ARN{
		Partition: scope.Partition.String(),
		Service:   "ses",
		Region:    scope.Region.String(),
		Account:   scope.Account.String(),
		Resource:  "identity/" + name,
	}
}

// status is an identity's verification status (ses.md 5.2). NotStarted is
// not one: it only answers for an identity the store does not hold.
type status interface {
	// v1 is the status as v1 VerificationStatus spells it.
	v1() sesapi.VerificationStatus
	// v2 is the status as v2 VerificationStatus spells it: v1's in upper
	// snake case.
	v2() sesv2api.VerificationStatus
}

// statusPending is an identity whose verification started at Since and has
// not settled.
type statusPending struct {
	Since clock.Instant `json:"since"`
}

// statusSuccess is a verified identity.
type statusSuccess struct{}

// statusFailed is an identity whose verification expired.
type statusFailed struct{}

// statusTemporaryFailure is an identity whose verification failed for a
// reason that may pass.
type statusTemporaryFailure struct{}

func (statusPending) v1() sesapi.VerificationStatus { return sesapi.VerificationStatusPending }
func (statusSuccess) v1() sesapi.VerificationStatus { return sesapi.VerificationStatusSuccess }
func (statusFailed) v1() sesapi.VerificationStatus  { return sesapi.VerificationStatusFailed }
func (statusTemporaryFailure) v1() sesapi.VerificationStatus {
	return sesapi.VerificationStatusTemporaryFailure
}

func (statusPending) v2() sesv2api.VerificationStatus { return sesv2api.VerificationStatusPending }
func (statusSuccess) v2() sesv2api.VerificationStatus { return sesv2api.VerificationStatusSuccess }
func (statusFailed) v2() sesv2api.VerificationStatus  { return sesv2api.VerificationStatusFailed }
func (statusTemporaryFailure) v2() sesv2api.VerificationStatus {
	return sesv2api.VerificationStatusTemporaryFailure
}

// codec is the state codec: jsonx with the status union. The table is
// static, so an error building it is a bug in it; the codec then fails
// every section with it.
func codec() store.Codec {
	statuses, err := jsonx.NewUnion("ses.status",
		jsonx.Variant[status, statusPending]("Pending"),
		jsonx.Variant[status, statusSuccess]("Success"),
		jsonx.Variant[status, statusFailed]("Failed"),
		jsonx.Variant[status, statusTemporaryFailure]("TemporaryFailure"),
	)
	if err != nil {
		return store.Codec{
			Marshal:   func(any) ([]byte, error) { return nil, err },
			Unmarshal: func([]byte, any) error { return err },
		}
	}
	c := jsonx.NewCodec(statuses)
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}
