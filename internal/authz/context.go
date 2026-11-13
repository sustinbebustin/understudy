package authz

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Via is how a call reaches its service.
//
//sumtype:decl
type Via interface{ isVia() }

// Direct is an external call.
type Direct struct{}

// ForwardAccess is an internal call made as the caller (kernel.AsCaller).
type ForwardAccess struct {
	// ViaService is the endpoint of the service that makes the call,
	// <service>.<region>.amazonaws.com, which kms:ViaService carries
	// (iam-enforcement-surfaces.md 9.2); "" when it is not known, which
	// leaves kms:ViaService Unsupplied.
	ViaService string
}

// ExecutionRole is an internal call made as a function's execution role.
type ExecutionRole struct{}

// Delivery is a service principal acting for Source. SourceAccount is zero
// when the delivery sends no aws:SourceAccount
// (iam-enforcement-surfaces.md 8.1).
type Delivery struct {
	Source        aws.ARN
	SourceAccount aws.AccountID
}

func (Direct) isVia()        {}
func (ForwardAccess) isVia() {}
func (ExecutionRole) isVia() {}
func (Delivery) isVia()      {}

// ContextInput is what BuildContext reads.
type ContextInput struct {
	Principal     identity.Principal
	PrincipalTags []Tag
	Action        registry.IAMAction
	Resource      Resource
	Now           clock.Instant
	Region        string
	// UserAgent and Referer are the request's headers, "" when absent.
	UserAgent string
	Referer   string
	Via       Via
	// Keys are request keys the binding or the handler supplies
	// (kernel.ConditionKeys); a key with no values is one the request does
	// not carry.
	Keys map[string][]string
}

// BuildContext returns the request context of iam-evaluation.md 8.1 for one
// pair: the principal keys, the request keys understudy computes, the
// delivery's source keys and in.Keys, with every key AWS would send that
// understudy cannot compute marked Unsupplied.
//
// A key never set follows policydoc.NewContext: a key of the action's own
// service is Unsupplied, since AWS sends service keys per action
// (iam-evaluation.md 8.2), and every other key is Absent, the organization,
// VPC and MFA-age keys among them (8.1, "never"). An in.Keys entry is applied
// last, so it replaces a key BuildContext computed.
func BuildContext(in ContextInput) *policydoc.Context {
	c := policydoc.NewContext(in.Action.Prefix)
	principalKeys(c, in.Principal)
	for _, t := range in.PrincipalTags {
		key := "aws:PrincipalTag/" + t.Key
		// Two tags whose keys differ only in case, with different values:
		// each matches the policy key spelled as it is, and no other
		// spelling finds a value (switch iam.principal-tag.case-duplicates
		// "exact-case", 6.13, inferred from a lower-case resource-tag
		// lookup, IAM-EVAL-31).
		if slices.ContainsFunc(in.PrincipalTags, func(o Tag) bool { return strings.EqualFold(o.Key, t.Key) && o.Value != t.Value }) {
			c.SetExact(key, t.Value)
			c.Absent(key)
		} else {
			c.Set(key, t.Value)
		}
	}
	requestKeys(c, in)
	viaKeys(c, in.Via)
	if strings.EqualFold(in.Action.Prefix, "kms") {
		kmsKeys(c, in)
	}
	if !slices.ContainsFunc(slices.Collect(maps.Keys(in.Keys)), func(k string) bool { return strings.EqualFold(k, "aws:TagKeys") }) {
		// AWS sends the tag keys with every request that tags, and a binding
		// that knows them supplies aws:TagKeys (8.1).
		c.Unsupplied("aws:TagKeys")
		c.UnsuppliedFamily("aws:RequestTag/")
	}
	for _, key := range slices.Sorted(maps.Keys(in.Keys)) {
		switch values := in.Keys[key]; {
		case len(values) == 0:
			c.Absent(key)
		case len(values) > 1 || Multivalued(key):
			c.SetMulti(key, values...)
		default:
			c.Set(key, values[0])
		}
	}
	return c
}

// principalKeys sets the principal keys of iam-evaluation.md 7.2 and 8.1
// (S-VARS #principaltable). A role session's aws:PrincipalArn is its role's
// ARN with the role's path (S-KEYS #condition-keys-principalarn; the path is
// inferred, IAM-EVAL-20). A service principal carries no aws:PrincipalType,
// aws:userid or aws:PrincipalAccount (switch
// iam.context.service-principal-identity-keys "absent", IAM-EVAL-33); an
// anonymous caller no aws:PrincipalArn or aws:PrincipalIsAWSService (2.10).
// An identifier understudy does not know (a user or role without its unique
// id) is Unsupplied, never invented.
func principalKeys(c *policydoc.Context, p identity.Principal) {
	switch p := p.(type) {
	case identity.Root:
		c.Set("aws:PrincipalArn", identity.Render(p))
		c.Set("aws:PrincipalAccount", p.Account.String())
		c.Set("aws:userid", p.Account.String())
		c.Set("aws:PrincipalType", "Account")
		c.Set("aws:PrincipalIsAWSService", "false")
	case identity.User:
		c.Set("aws:PrincipalArn", identity.Render(p))
		c.Set("aws:PrincipalAccount", p.Account.String())
		setKnown(c, "aws:userid", p.ID, p.ID)
		c.Set("aws:PrincipalType", "User")
		c.Set("aws:username", p.Name)
		c.Set("aws:PrincipalIsAWSService", "false")
	case identity.AssumedRole:
		c.Set("aws:PrincipalArn", roleARN(p))
		c.Set("aws:PrincipalAccount", p.Account.String())
		setKnown(c, "aws:userid", p.Role.ID, p.Role.ID+":"+p.Session)
		c.Set("aws:PrincipalType", "AssumedRole")
		c.Set("aws:PrincipalIsAWSService", "false")
		// MFA is not checked, so no session has it: switch
		// iam.context.mfa-present "false-in-sessions" (8.1, IAM-EVAL-26).
		c.Set("aws:MultiFactorAuthPresent", "false")
		sessionKeys(c, p.Attributes)
	case identity.ServicePrincipal:
		c.Set("aws:PrincipalIsAWSService", "true")
		c.Set("aws:PrincipalServiceName", p.Name)
		c.SetMulti("aws:PrincipalServiceNamesList", p.Name)
	case identity.Anonymous:
		c.Set("aws:PrincipalAccount", "anonymous")
		c.Set("aws:userid", "anonymous")
		c.Set("aws:PrincipalType", "Anonymous")
	}
}

// setKnown sets key to value when id is known, else marks key Unsupplied.
func setKnown(c *policydoc.Context, key, id, value string) {
	if id == "" {
		c.Unsupplied(key)
		return
	}
	c.Set(key, value)
}

// sessionKeys sets a role session's aws:TokenIssueTime and
// aws:SourceIdentity from what AssumeRole passed (8.1). A session without
// attributes (an execution environment's) still has temporary credentials,
// so AWS sends an issue time understudy does not know.
func sessionKeys(c *policydoc.Context, a *identity.SessionAttributes) {
	if a == nil || a.Issued.IsZero() {
		c.Unsupplied("aws:TokenIssueTime")
	} else {
		c.Set("aws:TokenIssueTime", isoTime(a.Issued))
	}
	if a != nil && a.SourceIdentity != "" {
		c.Set("aws:SourceIdentity", a.SourceIdentity)
	}
}

// requestKeys sets the keys understudy computes from the call (8.1): the
// emulator clock (invariant 5), the region, the headers when sent, the
// resource's account, and TLS, which AWS's endpoints always use (switch
// iam.context.secure-transport "assume-tls", 8.2). aws:SourceIp is never
// taken from the transport (iam-enforcement-surfaces.md 9.1, 12.10), and
// resource tags are not computed, so both are Unsupplied.
func requestKeys(c *policydoc.Context, in ContextInput) {
	if in.Now.IsZero() {
		c.Unsupplied("aws:CurrentTime")
		c.Unsupplied("aws:EpochTime")
	} else {
		c.Set("aws:CurrentTime", isoTime(in.Now))
		c.Set("aws:EpochTime", strconv.FormatInt(in.Now.Wire().Unix(), 10))
	}
	setKnown(c, "aws:RequestedRegion", in.Region, in.Region)
	c.Set("aws:SecureTransport", "true")
	if in.UserAgent != "" {
		c.Set("aws:UserAgent", in.UserAgent)
	}
	if in.Referer != "" {
		c.Set("aws:referer", in.Referer)
	}
	if owner := ownerOf(in.Resource); owner.IsZero() {
		c.Unsupplied("aws:ResourceAccount")
	} else {
		c.Set("aws:ResourceAccount", owner.String())
	}
	c.Unsupplied("aws:SourceIp")
	c.UnsuppliedFamily("aws:ResourceTag/")
}

// viaKeys sets the keys of how the call reached its service (8.1, S-KEYS
// #condition-keys-viaawsservice): a call a service makes as the caller has
// aws:ViaAWSService true and a CalledVia chain understudy does not track
// (IAM-EVAL-34); a delivery carries aws:SourceArn, and aws:SourceAccount when
// the delivering service sends it (iam-enforcement-surfaces.md 8.1).
func viaKeys(c *policydoc.Context, via Via) {
	switch v := via.(type) {
	case ForwardAccess:
		c.Set("aws:ViaAWSService", "true")
		c.Unsupplied("aws:CalledVia")
		c.Unsupplied("aws:CalledViaFirst")
		c.Unsupplied("aws:CalledViaLast")
		return
	case Delivery:
		c.Set("aws:SourceArn", v.Source.String())
		if !v.SourceAccount.IsZero() {
			c.Set("aws:SourceAccount", v.SourceAccount.String())
		}
	case Direct, ExecutionRole, nil:
	}
	c.Set("aws:ViaAWSService", "false")
}

// kmsKeys sets the KMS keys understudy knows for a kms action
// (iam-enforcement-surfaces.md 9.2): kms:CallerAccount for a root, user or
// session, and kms:ViaService, which AWS sends only for a call a service
// makes for the caller: the forwarding service's endpoint, Unsupplied when
// the call does not name it.
func kmsKeys(c *policydoc.Context, in ContextInput) {
	if account, ok := identity.AccountOf(in.Principal); ok {
		c.Set("kms:CallerAccount", account.String())
	}
	switch v := in.Via.(type) {
	case ForwardAccess:
		if v.ViaService == "" {
			c.Unsupplied("kms:ViaService")
			return
		}
		c.Set("kms:ViaService", v.ViaService)
	case Direct, ExecutionRole, Delivery, nil:
		c.Absent("kms:ViaService")
	}
}

// isoTime is an instant as the date keys carry it: ISO 8601 in UTC to the
// second (8.1).
func isoTime(i clock.Instant) string {
	return i.Wire().UTC().Format("2006-01-02T15:04:05Z")
}

// Multivalued reports whether AWS defines key as multivalued: aws:TagKeys,
// aws:CalledVia, aws:PrincipalServiceNamesList, aws:PrincipalOrgPaths,
// aws:ResourceOrgPaths, aws:SourceOrgPaths, aws:VpceOrgPaths,
// sts:TransitiveTagKeys, kms:EncryptionContextKeys (iam-evaluation.md
// 6.12) and ses:Recipients, ArrayOfString in the SAR
// (iam-enforcement-surfaces.md 9.2). Key names compare case-insensitively
// (6.2).
func Multivalued(key string) bool {
	return slices.ContainsFunc([]string{
		"aws:TagKeys", "aws:CalledVia", "aws:PrincipalServiceNamesList", "aws:PrincipalOrgPaths",
		"aws:ResourceOrgPaths", "aws:SourceOrgPaths", "aws:VpceOrgPaths",
		"sts:TransitiveTagKeys", "kms:EncryptionContextKeys", "ses:Recipients",
	}, func(k string) bool { return strings.EqualFold(k, key) })
}
