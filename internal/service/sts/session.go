package sts

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// Limits of AssumeRole's session policies that the Smithy model does not
// carry (AssumeRole API reference, PolicyArns and Policy).
const (
	// maxPolicyARNs is the number of managed session policies a request
	// may name.
	maxPolicyARNs = 10
	// maxSessionPolicyText caps the plaintext of the inline session policy
	// and the managed session policy ARNs together, in characters.
	maxSessionPolicyText = 2048
)

// reservedPrefix begins the source identities and session tag keys and
// values AWS keeps for itself, in any case (AssumeRole API reference,
// SourceIdentity; IAM User Guide, rules for tagging).
const reservedPrefix = "aws:"

// checkSession applies the rules AWS documents for what an AssumeRole
// request asks of its session and the model cannot express: the managed
// policy count, the reserved prefix, the session tags, the inline policy's
// grammar and the plaintext size of the session policies, in that order.
// Session policies and tags are validated but never evaluated.
func checkSession(in *stsapi.AssumeRoleInput) *awserr.Error {
	if len(in.PolicyArns) > maxPolicyARNs {
		return errTooManyPolicyARNs()
	}
	if in.SourceIdentity != nil && reserved(*in.SourceIdentity) {
		return errReservedSourceIdentity(*in.SourceIdentity)
	}
	for _, t := range in.Tags {
		if reserved(t.Key) || reserved(t.Value) {
			return errReservedTag(t.Key)
		}
	}
	if err := checkTagKeys(in.Tags, in.TransitiveTagKeys); err != nil {
		return err
	}
	if in.Policy != nil {
		if err := checkSessionPolicy(*in.Policy); err != nil {
			return err
		}
	}
	if n := sessionPolicyText(in); n > maxSessionPolicyText {
		return errPackedPolicyTooLarge(n)
	}
	return nil
}

// checkTagKeys applies AWS's rules for session tag keys, which are not case
// sensitive (AssumeRole API reference, Tags): no two tags may share a key
// in any case, and every transitive tag key must name one of the tags.
func checkTagKeys(tags []stsapi.Tag, transitive []string) *awserr.Error {
	hasKey := func(key string, tags []stsapi.Tag) bool {
		return slices.ContainsFunc(tags, func(t stsapi.Tag) bool { return strings.EqualFold(t.Key, key) })
	}
	for i, t := range tags {
		if hasKey(t.Key, tags[i+1:]) {
			return errDuplicateTagKeys()
		}
	}
	for _, k := range transitive {
		if !hasKey(k, tags) {
			return errTransitiveTagKeyMissing()
		}
	}
	return nil
}

// checkSessionPolicy validates an inline session policy as IAM validates
// an identity policy: a session policy limits the role's identity
// policies, so it follows their grammar.
func checkSessionPolicy(doc string) *awserr.Error {
	parsed, perr := policydoc.Parse([]byte(doc))
	if perr != nil {
		return stsapi.ErrMalformedPolicyDocumentException(perr.Message())
	}
	if verr := policydoc.ValidateIdentity(parsed); verr != nil {
		return stsapi.ErrMalformedPolicyDocumentException(verr.Message())
	}
	return nil
}

// sessionPolicyText is the plaintext length of a request's session
// policies: the inline policy's characters plus each managed policy ARN's.
func sessionPolicyText(in *stsapi.AssumeRoleInput) int {
	n := 0
	if in.Policy != nil {
		n += utf8.RuneCountInString(*in.Policy)
	}
	for _, p := range in.PolicyArns {
		if p.Arn != nil {
			n += utf8.RuneCountInString(*p.Arn)
		}
	}
	return n
}

func reserved(s string) bool {
	return len(s) >= len(reservedPrefix) && strings.EqualFold(s[:len(reservedPrefix)], reservedPrefix)
}
