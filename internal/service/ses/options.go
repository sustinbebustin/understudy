package ses

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
)

// Options are the SES settings wire.go fills from the configuration and the
// switches. The zero value is the sandbox, no configured identity and every
// switch at its default.
type Options struct {
	// VerifiedIdentities is ses.verifiedIdentities: identities that start
	// verified when they are created, in any account or Region (ses.md
	// 11.3 item 3).
	VerifiedIdentities VerifiedIdentities
	// ProductionAccess is ses.productionAccess: true takes every account
	// and Region out of the sandbox (ses.md 11.3).
	ProductionAccess bool

	// Store is the ses.store.shared-v1-v2 switch.
	Store StoreSharing
	// MessageID is the ses.message-id.format switch.
	MessageID MessageIDFormat
	// UnverifiedText is the ses.error.unverified.text switch.
	UnverifiedText UnverifiedText
	// UnknownIdentity is the ses.verification.unknown-identity switch.
	UnknownIdentity UnknownIdentity
	// RawEnvelope is the ses.raw.envelope switch.
	RawEnvelope RawEnvelope
}

// VerifiedIdentities is ses.verifiedIdentities parsed and classified by
// internal/config: an identity created with a name that matches an entry
// starts Success instead of Pending.
type VerifiedIdentities struct {
	// Addresses are exact email addresses, matched case-sensitively
	// against an email identity's name (ses.md 5.1).
	Addresses []string
	// Domains are domain names, matched case-insensitively against a
	// domain identity's name; they verify no email identity.
	Domains []string
	// AddressDomains are the domains of the "*@domain" entries: an email
	// identity whose domain part equals one, case-insensitively, starts
	// verified. Subdomains do not match.
	AddressDomains []string
}

// verifies reports whether an identity of kind named name starts
// verified: an address listed exactly or whose domain part is an address
// domain, or a domain listed.
func (v VerifiedIdentities) verifies(kind sesapi.IdentityType, name string) bool {
	if kind == sesapi.IdentityTypeDomain {
		return slices.ContainsFunc(v.Domains, func(d string) bool { return strings.EqualFold(d, name) })
	}
	if slices.Contains(v.Addresses, name) {
		return true
	}
	at := strings.LastIndex(name, "@")
	return at >= 0 && slices.ContainsFunc(v.AddressDomains, func(d string) bool { return strings.EqualFold(d, name[at+1:]) })
}

// StoreSharing is whether the v1 and v2 APIs share one identity and
// template store (ses.md 2.3, SES-12).
type StoreSharing uint8

// StoreSharing values; the zero value is StoreShared.
const (
	// StoreShared is one store behind both APIs, as the shared ARNs
	// suggest (inferred).
	StoreShared StoreSharing = iota
	// StoreSeparate gives each API its own identities and templates; the
	// sent-message store, the recipient counter and production access stay
	// shared.
	StoreSeparate
)

// String returns "shared" or "separate".
func (s StoreSharing) String() string {
	switch s {
	case StoreShared:
		return "shared"
	case StoreSeparate:
		return "separate"
	}
	return "StoreSharing(" + strconv.Itoa(int(s)) + ")"
}

// MessageIDFormat is how a MessageId is minted (ses.md 4.7, SES-07).
type MessageIDFormat uint8

// MessageIDFormat values; the zero value is MessageIDTimestamped.
const (
	// MessageIDTimestamped is "01", MessageIDRegionCode, 12 lower-case hex
	// digits of the emulator-clock Unix milliseconds at acceptance, "-", a
	// version-4 UUID drawn from ids.KindSESMessageID, "-000000": 60
	// characters.
	MessageIDTimestamped MessageIDFormat = iota
	// MessageIDRandom replaces the 16-digit prefix with 16 lower-case hex
	// digits drawn from ids.KindSESMessageID, so the id carries no time.
	MessageIDRandom
)

// String returns "timestamped" or "random".
func (f MessageIDFormat) String() string {
	switch f {
	case MessageIDTimestamped:
		return "timestamped"
	case MessageIDRandom:
		return "random"
	}
	return "MessageIDFormat(" + strconv.Itoa(int(f)) + ")"
}

// UnverifiedText is the order of the identities the MessageRejected text of
// a failed sender or sandbox check lists (ses.md 6.1, SES-01, SES-03).
type UnverifiedText uint8

// UnverifiedText values; the zero value is UnverifiedHashOrder.
const (
	// UnverifiedHashOrder lists each failing identity once, as written in
	// the request, in the iteration order of a Java HashSet they were added
	// to senders first (source or From, then ReturnPath), then the
	// envelope recipients in request order (hashSetOrder), joined by ", ".
	// It reproduces both lists SES-03 recorded (ses.md 6.1).
	UnverifiedHashOrder UnverifiedText = iota
	// UnverifiedSendersFirst lists the failing senders, then the failing
	// envelope recipients, each in request order.
	UnverifiedSendersFirst
	// UnverifiedRecipientsFirst lists the failing recipients first, then
	// the senders.
	UnverifiedRecipientsFirst
)

// String returns "hash-order", "senders-first" or "recipients-first".
func (u UnverifiedText) String() string {
	switch u {
	case UnverifiedHashOrder:
		return "hash-order"
	case UnverifiedSendersFirst:
		return "senders-first"
	case UnverifiedRecipientsFirst:
		return "recipients-first"
	}
	return "UnverifiedText(" + strconv.Itoa(int(u)) + ")"
}

// UnknownIdentity is how GetIdentityVerificationAttributes and
// GetIdentityNotificationAttributes answer an identity the store does not
// hold (ses.md 5.3, 9.1, SES-08).
type UnknownIdentity uint8

// UnknownIdentity values; the zero value is UnknownIdentityOmitted.
const (
	// UnknownIdentityOmitted leaves the identity out of the map, which
	// Terraform reads as gone (ses.md 9.1).
	UnknownIdentityOmitted UnknownIdentity = iota
	// UnknownIdentityNotStarted returns the identity with
	// VerificationStatus NotStarted and no token (verification attributes
	// only; notification attributes still omit it).
	UnknownIdentityNotStarted
)

// String returns "omitted" or "not-started".
func (u UnknownIdentity) String() string {
	switch u {
	case UnknownIdentityOmitted:
		return "omitted"
	case UnknownIdentityNotStarted:
		return "not-started"
	}
	return "UnknownIdentity(" + strconv.Itoa(int(u)) + ")"
}

// RawEnvelope is which recipients a raw send's sandbox check and counter
// use (ses.md 4.2, SES-20).
type RawEnvelope uint8

// RawEnvelope values; the zero value is RawEnvelopeDestinations.
const (
	// RawEnvelopeDestinations uses v1 Destinations when non-empty, else
	// the addresses of the To, Cc and Bcc headers; v2 Raw content, which has
	// no Destinations, uses Destination when given, else the headers.
	RawEnvelopeDestinations RawEnvelope = iota
	// RawEnvelopeUnion uses Destinations (or v2 Destination) together with
	// the To, Cc and Bcc headers, each address once.
	RawEnvelopeUnion
)

// String returns "destinations" or "union".
func (r RawEnvelope) String() string {
	switch r {
	case RawEnvelopeDestinations:
		return "destinations"
	case RawEnvelopeUnion:
		return "union"
	}
	return "RawEnvelope(" + strconv.Itoa(int(r)) + ")"
}
