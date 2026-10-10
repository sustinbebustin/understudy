package policydoc

import (
	"fmt"
	"strings"
)

// Rule is the IAM rule a document breaks.
type Rule uint8

// Rule values, in the order ValidateIdentity and ValidateTrust check them.
const (
	// RuleVersion is an identity policy whose Version is not 2012-10-17.
	// ValidateTrust does not apply it.
	RuleVersion Rule = iota + 1
	// RuleLegacy is a Date condition value or a resource that IAM's legacy
	// parser rejects.
	RuleLegacy
	RuleUniqueSid
	RuleActionsRequired
	RuleResourceARN
	RuleResourcePartition
	RuleResourceVendor
	RuleIAMResourceRegion
	RuleS3ResourceRegion
	RuleIAMResourcePath
	RuleActionVendor
	RuleActionColons
	RuleVendorName
	RuleResourcesRequired
	RuleTrustAction
	RuleResourceProhibited
	// RulePrincipalProhibited is a Principal or NotPrincipal in an identity
	// policy. moto lacks it; the text is versitygw's
	// (iamapi/policy/validate.go@2147a0c30473dc9314e9106b527d50a36c484594,
	// UNVERIFIED against AWS), and its place after the resource rules is
	// understudy's choice.
	RulePrincipalProhibited
)

// ValidationError reports a parsed document IAM rejects with
// MalformedPolicyDocument. Subject is the offending value (a Sid, an action,
// a vendor, a resource, a condition value) when the rule has one, or the
// offending element's name (NotAction in a trust policy) when the element
// itself is what IAM rejects.
type ValidationError struct {
	Rule    Rule
	Subject string
}

// Error describes the rejection, naming the offending value when there is
// one.
func (e *ValidationError) Error() string {
	if e.Subject == "" {
		return "policy document rejected by IAM: " + e.Message()
	}
	return fmt.Sprintf("policy document rejected by IAM at %q: %s", e.Subject, e.Message())
}

// Message returns the MalformedPolicyDocument text (iam-sts-kms.md 3.3 and
// moto's validator).
func (e *ValidationError) Message() string {
	switch e.Rule {
	case RuleVersion:
		return "Policy document must be version 2012-10-17 or greater."
	case RuleLegacy:
		return "The policy failed legacy parsing"
	case RuleUniqueSid:
		return "Statement IDs (SID) in a single policy must be unique."
	case RuleActionsRequired:
		return "Policy statement must contain actions."
	case RuleResourceARN:
		return "Resource " + e.Subject + ` must be in ARN format or "*".`
	case RuleResourcePartition:
		return partitionMessage(e.Subject)
	case RuleResourceVendor:
		return "Resource vendor must be fully qualified and cannot contain regexes."
	case RuleIAMResourceRegion:
		return "IAM resource " + e.Subject + " cannot contain region information."
	case RuleS3ResourceRegion:
		return "Resource " + e.Subject + " can not contain region information."
	case RuleIAMResourcePath:
		return `IAM resource path must either be "*" or start with ` + strings.Join(iamResourcePaths(), ", ") + "."
	case RuleActionVendor:
		return "Actions/Conditions must be prefaced by a vendor, e.g., iam, sdb, ec2, etc."
	case RuleActionColons:
		return "Actions/Condition can contain only one colon."
	case RuleVendorName:
		return "Vendor " + e.Subject + " is not valid"
	case RuleResourcesRequired:
		return "Policy statement must contain resources."
	case RuleTrustAction:
		// The double space is moto's.
		return "Trust Policy statement actions can only be sts:AssumeRole, sts:AssumeRoleWithSAML,  and sts:AssumeRoleWithWebIdentity"
	case RuleResourceProhibited:
		return "Has prohibited field Resource."
	case RulePrincipalProhibited:
		return "Policy document should not specify a principal."
	default:
		return fmt.Sprintf("Rule(%d)", uint8(e.Rule))
	}
}

// ValidateIdentity applies, in IAM's order, the rules IAM checks after
// parsing a managed or inline policy attached to a user, group or role, and
// returns the first one doc breaks: its Version must be 2012-10-17, and on
// top of the rules every policy follows, every statement needs resources
// and none may name a principal. Resource policies of other services are
// not validated by IAM and need no call.
func ValidateIdentity(doc Document) *ValidationError {
	if doc.Version != Version20121017 {
		return &ValidationError{Rule: RuleVersion}
	}
	if err := validate(doc); err != nil {
		return err
	}
	for _, st := range doc.Statements {
		if len(resourcePatterns(st.Resource)) == 0 {
			return &ValidationError{Rule: RuleResourcesRequired}
		}
	}
	for _, st := range doc.Statements {
		if st.Principal != nil {
			return &ValidationError{Rule: RulePrincipalProhibited}
		}
	}
	return nil
}

// ValidateTrust applies, in IAM's order, the rules IAM checks after parsing
// a role's assume-role policy, and returns the first one doc breaks: on top
// of the rules every policy follows, its actions are STS actions and no
// statement may have resources. Any Version is accepted, 2008-10-17 and an
// absent one included (see the package doc).
func ValidateTrust(doc Document) *ValidationError {
	if err := validate(doc); err != nil {
		return err
	}
	if err := checkTrustActions(doc.Statements); err != nil {
		return err
	}
	for _, st := range doc.Statements {
		if st.Resource != nil {
			return &ValidationError{Rule: RuleResourceProhibited}
		}
	}
	return nil
}

// validate applies the rules every policy IAM validates follows, in IAM's
// order.
func validate(doc Document) *ValidationError {
	if err := checkLegacyDates(doc.Statements); err != nil {
		return err
	}
	legacy, resourceErr := checkResourceFormats(doc.Statements)
	if legacy != nil {
		return legacy
	}
	if err := checkSids(doc.Statements); err != nil {
		return err
	}
	for _, st := range doc.Statements {
		if len(actionPatterns(st.Action)) == 0 {
			return &ValidationError{Rule: RuleActionsRequired}
		}
	}
	if resourceErr != nil {
		return resourceErr
	}
	return checkVendors(doc.Statements)
}

// checkSids rejects a non-empty Sid used twice.
func checkSids(statements []Statement) *ValidationError {
	seen := map[string]bool{}
	for _, st := range statements {
		if st.Sid == "" {
			continue
		}
		if seen[st.Sid] {
			return &ValidationError{Rule: RuleUniqueSid, Subject: st.Sid}
		}
		seen[st.Sid] = true
	}
	return nil
}

// checkVendors checks every statement's Action before any NotAction, as IAM
// does.
func checkVendors(statements []Statement) *ValidationError {
	var actions, notActions []string
	for _, st := range statements {
		switch a := st.Action.(type) {
		case nil:
		case Action:
			actions = append(actions, a...)
		case NotAction:
			notActions = append(notActions, a...)
		}
	}
	for _, a := range append(actions, notActions...) {
		if err := checkVendor(a); err != nil {
			return err
		}
	}
	return nil
}

// checkVendor checks that an action is "*" or vendor:name with a vendor of
// letters, digits, "-" and ".".
func checkVendor(action string) *ValidationError {
	parts := strings.Split(action, ":")
	switch {
	case len(parts) == 1 && action != "*":
		return &ValidationError{Rule: RuleActionVendor, Subject: action}
	case len(parts) > 2:
		return &ValidationError{Rule: RuleActionColons, Subject: action}
	}
	vendor := parts[0]
	if vendor == "*" {
		return nil
	}
	for _, r := range vendor {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '.' {
			return &ValidationError{Rule: RuleVendorName, Subject: vendor}
		}
	}
	return nil
}

// checkTrustActions rejects a trust-policy statement whose Action names
// anything but an STS action, or that uses NotAction.
func checkTrustActions(statements []Statement) *ValidationError {
	for _, st := range statements {
		actions, ok := st.Action.(Action)
		if !ok {
			return &ValidationError{Rule: RuleTrustAction, Subject: "NotAction"}
		}
		for _, a := range actions {
			if !isTrustAction(a) {
				return &ValidationError{Rule: RuleTrustAction, Subject: a}
			}
		}
	}
	return nil
}

// isTrustAction reports whether a trust policy may name action: moto's
// list, matched exactly.
func isTrustAction(action string) bool {
	switch action {
	case "sts:AssumeRole", "sts:AssumeRoleWithSAML", "sts:AssumeRoleWithWebIdentity", "sts:DecodeAuthorizationMessage",
		"sts:GetAccessKeyInfo", "sts:GetCallerIdentity", "sts:GetFederationToken", "sts:GetServiceBearerToken",
		"sts:GetSessionToken", "sts:SetSourceIdentity", "sts:TagSession":
		return true
	default:
		return false
	}
}

// actionPatterns returns the patterns of either form, nil when absent.
func actionPatterns(a Actions) []string {
	switch x := a.(type) {
	case Action:
		return x
	case NotAction:
		return x
	default:
		return nil
	}
}

// resourcePatterns returns the patterns of either form, nil when absent.
func resourcePatterns(r Resources) []string {
	switch x := r.(type) {
	case Resource:
		return x
	case NotResource:
		return x
	default:
		return nil
	}
}
