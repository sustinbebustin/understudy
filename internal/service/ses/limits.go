package ses

import "time"

// The values chosen without a switch (ses.md 11.4, second table). Each names
// the recording case that would settle it; recording one means searching
// this package for its id, since coverage-check cannot flag a constant.

// TagMaxLength is the most characters a message tag name or value may have:
// the recorded text says "cannot exceed 255 characters", but the recordings
// used 2,100 and 2,200, so 255 against 256 is UNVERIFIED (ses.md 4.5,
// ses.tags.max-length, SES-16).
const TagMaxLength = 255

// FeedbackIDTagNames are the only "ses:"-prefixed tag names a send accepts
// (ses.md 4.5, recorded for these two); any other "ses:" name fails the
// charset rule, which is UNVERIFIED (ses.tags.ses-prefix, SES-16).
func FeedbackIDTagNames() []string {
	return []string{"ses:feedback-id-a", "ses:feedback-id-b"}
}

// EmailVerificationExpiry is how long after its latest verification email a
// Pending email identity turns Failed, and DomainVerificationExpiry how long
// after creation a Pending domain does: 24 and 72 hours are documented
// (ses.md 5.2); that the change is exact rather than at a later periodic
// check is UNVERIFIED (ses.verification.expiry, SES-27).
const (
	EmailVerificationExpiry  = 24 * time.Hour
	DomainVerificationExpiry = 72 * time.Hour
)

// SimulatorDomain is the mailbox simulator's domain, matched
// case-insensitively; a recipient there is a simulator address only when
// its local part, with any "+label" removed, is exactly one of the
// Scenarios (ses.md 6.2). Other local parts are ordinary recipients, and
// matching is case-sensitive in the local part: both UNVERIFIED
// (ses.simulator.addresses, SES-19).
const SimulatorDomain = "simulator.amazonses.com"

// Scenario is a mailbox simulator outcome, named by its local part
// (ses.md 6.2). A message record maps each simulator recipient to one.
type Scenario string

// The five documented simulator addresses (ses.md 6.2).
const (
	ScenarioSuccess         Scenario = "success"
	ScenarioBounce          Scenario = "bounce"
	ScenarioOOTO            Scenario = "ooto"
	ScenarioComplaint       Scenario = "complaint"
	ScenarioSuppressionList Scenario = "suppressionlist"
)

// Scenarios lists the simulator local parts in the documented order.
func Scenarios() []Scenario {
	return []Scenario{ScenarioSuccess, ScenarioBounce, ScenarioOOTO, ScenarioComplaint, ScenarioSuppressionList}
}

// SandboxRecipientLabels is whether a sandbox recipient "local+label@domain"
// passes when "local@domain" is verified. Labels are documented for senders
// (ses.md 5.1); for recipients it is UNVERIFIED (ses.sandbox.recipient-labels,
// SES-19).
const SandboxRecipientLabels = true

// SendRateEnforced is whether MaxSendRate throttles sends; the limiter's
// burst behavior and status are UNVERIFIED (ses.md 6.3,
// ses.quota.send-rate, SES-21).
const SendRateEnforced = false

// The sending quota of an account and Region. The sandbox values are
// recorded (ses.md 6.3); the production values are UNVERIFIED
// (ses.quota.production, SES-26).
const (
	SandboxMax24HourSend    = 200.0
	SandboxMaxSendRate      = 1.0
	ProductionMax24HourSend = -1.0
	ProductionMaxSendRate   = 14.0
)

// SendCheck is one step of a send's checks.
type SendCheck uint8

// SendCheck values, in the order SendChecks lists them.
const (
	// CheckModelValidation is the generated validation of the input.
	CheckModelValidation SendCheck = iota
	// CheckNotImplemented refuses a member or raw header understudy does
	// not implement.
	CheckNotImplemented
	// CheckTags validates message tags.
	CheckTags
	// CheckAddresses validates every address, then the message: its size,
	// and a raw message's MIME limits.
	CheckAddresses
	// CheckRecipientCount requires 1 to MaxRecipients envelope recipients.
	CheckRecipientCount
	// CheckSender requires each sender to be verified.
	CheckSender
	// CheckSandboxRecipients requires each envelope recipient to be
	// verified or a simulator address, in the sandbox only.
	CheckSandboxRecipients
	// CheckDailyQuota refuses a send that would pass Max24HourSend.
	CheckDailyQuota
)

// SendChecks is the order a send's checks run in, the first failing one
// answering. Tags before the sender is recorded (ses.md 4.5); the rest of
// the order is UNVERIFIED (ses.md 6.4, ses.send.check-order, SES-17).
func SendChecks() []SendCheck {
	return []SendCheck{
		CheckModelValidation, CheckNotImplemented, CheckTags, CheckAddresses,
		CheckRecipientCount, CheckSender, CheckSandboxRecipients, CheckDailyQuota,
	}
}

// Documented limits (ses.md 4.1, 4.2, 5.1, 7, 11.5).
const (
	// MaxRecipients is the most envelope recipients one send may have.
	MaxRecipients = 50
	// MaxIdentities is the most identities, addresses and domains together,
	// per account and Region.
	MaxIdentities = 10_000
	// MaxTemplates is the most templates per account and Region.
	MaxTemplates = 20_000
	// MaxRawLineLength is the most characters a raw message line may have,
	// without its line ending.
	MaxRawLineLength = 1000
	// MaxMIMEParts is the most MIME parts a raw message may have.
	MaxMIMEParts = 500
	// MaxVerificationAttributesIdentities is the most identities one
	// GetIdentityVerificationAttributes call may name.
	MaxVerificationAttributesIdentities = 100
	// ListIdentitiesMaxItems is ListIdentities' largest MaxItems and its
	// default; MaxItems below 1 or above it is refused.
	ListIdentitiesMaxItems = 1000
	// ListTemplatesMaxItems is the page size ListTemplates clamps a larger
	// MaxItems to, and ListTemplatesDefaultItems its default (model,
	// com.amazonaws.ses#ListTemplatesRequest$MaxItems).
	ListTemplatesMaxItems     = 100
	ListTemplatesDefaultItems = 10
	// ListEmailIdentitiesMaxPageSize is the largest PageSize (model,
	// com.amazonaws.sesv2#ListEmailIdentitiesRequest$PageSize).
	ListEmailIdentitiesMaxPageSize = 1000
	// ListEmailTemplatesMaxPageSize is the largest PageSize, whose least
	// is 1 (model, com.amazonaws.sesv2#ListEmailTemplatesRequest$PageSize).
	ListEmailTemplatesMaxPageSize = 100
)

// Limits whose exact value is UNVERIFIED (SES-29).
const (
	// MaxV1MessageBytes and MaxV2MessageBytes are "10 MB" and "40 MB"
	// after base64 (ses.md 2.2), read as binary megabytes.
	MaxV1MessageBytes = 10 * 1024 * 1024
	MaxV2MessageBytes = 40 * 1024 * 1024
	// MaxTemplateBytes is "500 KB" for a template's text and HTML parts
	// together (ses.md 7), read as Terraform's 512,000-character bound.
	MaxTemplateBytes = 512_000
	// ListEmailIdentitiesDefaultPageSize is the page size of a
	// ListEmailIdentities without PageSize, or with PageSize 0.
	ListEmailIdentitiesDefaultPageSize = 100
	// ListEmailTemplatesDefaultPageSize is the page size of a
	// ListEmailTemplates without PageSize, read from v1 ListTemplates'
	// documented default (model, com.amazonaws.ses#ListTemplatesRequest$MaxItems).
	ListEmailTemplatesDefaultPageSize = ListTemplatesDefaultItems
)

// MaxStoredMessages is how many sent messages the store keeps per account
// and Region; a new one evicts the oldest and counts it (ses.md 11.2).
const MaxStoredMessages = 10_000

// Texts used until a recording settles them (ses.md 11.5). Each is the
// whole message, or a fmt format whose verbs the comment names.
const (
	// UnverifiedTextPrefix starts the MessageRejected text of a failed
	// sender or sandbox check; the Region in upper case, ": " and the
	// identities follow (ses.md 6.1, switch ses.error.unverified.text).
	UnverifiedTextPrefix = "Email address is not verified. The following identities failed the check in region "
	// AddressMissingDomainText, AddressWhitespaceText,
	// AddressTrailingDotText and AddressIllegalText answer an invalid
	// address, in that order of precedence: no "@", whitespace or a control
	// character in the local part, a domain ending in ".", anything else
	// net/mail refuses (ses.md 4.6, SES-04).
	AddressMissingDomainText = "Missing final '@domain'"
	AddressWhitespaceText    = "Local address contains control or whitespace"
	AddressTrailingDotText   = "Domain ends with dot."
	AddressIllegalText       = "Illegal address"
	// NoRecipientsText answers a send with no envelope recipient (ses.md
	// 4.6, SES-05).
	NoRecipientsText = "There are no recipients specified."
	// TooManyRecipientsText answers more than MaxRecipients; %d is
	// MaxRecipients (SES-06).
	TooManyRecipientsText = "Recipient count exceeds %d."
	// DailyQuotaText answers a send past Max24HourSend, Code Throttling
	// (ses.md 4.6, 11.5, SES-22).
	DailyQuotaText = "Daily message quota exceeded."
	// MessageTooLongText answers a message above its API's size; %d is the
	// limit in bytes (SES-29).
	MessageTooLongText = "Message length exceeds the maximum of %d bytes."
	// LineTooLongText answers a raw line above MaxRawLineLength; %d is
	// MaxRawLineLength (SES-29).
	LineTooLongText = "Message contains a line longer than %d characters."
	// TooManyPartsText answers a raw message above MaxMIMEParts; %d is
	// MaxMIMEParts (SES-29).
	TooManyPartsText = "Message has more than %d MIME parts."
	// RawUnparseableText answers, as InvalidParameterValue, a raw message
	// whose header block or multipart body does not parse (model,
	// SendRawEmailRequest.RawMessage: a header, a blank line, the body,
	// well-formed MIME parts); no recording case asks for the answer.
	RawUnparseableText = "Message is not a valid MIME message: it needs header fields, a blank line and the body, with well-formed MIME parts."
	// TooManyIdentitiesText answers GetIdentityVerificationAttributes
	// naming more than MaxVerificationAttributesIdentities; %d is that
	// limit (SES-08).
	TooManyIdentitiesText = "Too many identities requested: at most %d are allowed."
	// MaxItemsText answers ListIdentities with MaxItems outside 1 to
	// ListIdentitiesMaxItems, and ListTemplates with MaxItems below 1; %d
	// is the operation's largest MaxItems (SES-29).
	MaxItemsText = "MaxItems must be between 1 and %d."
	// PageSizeText answers ListEmailIdentities with PageSize above
	// ListEmailIdentitiesMaxPageSize; %d is that limit.
	PageSizeText = "PageSize must be between 0 and %d."
	// TemplatePageSizeText answers ListEmailTemplates with PageSize
	// outside 1 to ListEmailTemplatesMaxPageSize; %d is that limit
	// (SES-29).
	TemplatePageSizeText = "PageSize must be between 1 and %d."
	// IdentityLimitText answers an identity past MaxIdentities; %d is
	// MaxIdentities.
	IdentityLimitText = "Maximum number of identities (%d) reached."
	// TemplateLimitText answers a template past MaxTemplates; %d is
	// MaxTemplates.
	TemplateLimitText = "Maximum number of templates (%d) reached."
	// TemplateTooLargeText answers a template above MaxTemplateBytes; %d
	// is MaxTemplateBytes.
	TemplateTooLargeText = "Template text and HTML parts exceed %d bytes."
	// TemplateExistsText and TemplateMissingText answer a duplicate and a
	// missing template, in v1 and v2; %s is the template name (ses.md 7,
	// SES-18).
	TemplateExistsText  = "Template %s already exists."
	TemplateMissingText = "Template %s does not exist."
	// IdentityExistsText and IdentityMissingText answer v2
	// CreateEmailIdentity of an existing identity and GetEmailIdentity,
	// DeleteEmailIdentity or a tag operation on a missing one; %s is the
	// identity (ses.md 5.4, SES-11).
	IdentityExistsText  = "Email identity %s already exists."
	IdentityMissingText = "Email identity %s does not exist."
	// DkimOnAddressText answers DkimSigningAttributes on an email address
	// identity, which the model documents as domains only (ses.md 5.4).
	DkimOnAddressText = "DkimSigningAttributes can only be set on a domain identity."
	// ContentShapeText answers a v2 Content with none or more than one of
	// Simple, Raw and Template; MissingFromText a Simple or Template send
	// without FromEmailAddress (ses.md 4.4, SES-23).
	ContentShapeText = "Exactly one of Simple, Raw or Template content must be specified."
	MissingFromText  = "FromEmailAddress is required for Simple and Template content."
	// TemplateSourceText answers v2 Template content with none or more
	// than one of TemplateName, TemplateArn and TemplateContent (model
	// com.amazonaws.sesv2#Template; the answer is inferred, SES-23).
	TemplateSourceText = "Exactly one of TemplateName, TemplateArn or TemplateContent must be specified."
)

// The verification email (ses.md 5.2): its sender, and its subject, a fmt
// format whose %s is the Region code. The display name SES prints is
// UNVERIFIED.
const (
	VerificationSender      = "no-reply-aws@amazon.com"
	VerificationSubjectText = "Amazon Web Services - Email Address Verification Request in region %s"
)

// VerificationLinkPath is the path of the verification link, followed by
// "?token=" and the identity's link token (ses.md 11.3 item 1).
const VerificationLinkPath = "/_understudy/ses/VerificationLink"
