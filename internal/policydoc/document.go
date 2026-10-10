package policydoc

import "fmt"

// Document is an IAM policy document: an identity policy, a trust policy or a
// resource policy (SQS queue, SNS topic, S3 bucket, Lambda function, KMS key).
type Document struct {
	Version Version
	// ID is the optional Id element; "" when absent.
	ID         string
	Statements []Statement
}

// Version is the policy language version.
type Version uint8

// Version values.
const (
	// VersionAbsent is a document without a Version element; IAM reads it as
	// 2008-10-17.
	VersionAbsent Version = iota
	Version20081017
	Version20121017
)

// String returns the element's text, "" for VersionAbsent.
func (v Version) String() string {
	switch v {
	case VersionAbsent:
		return ""
	case Version20081017:
		return "2008-10-17"
	case Version20121017:
		return "2012-10-17"
	default:
		return fmt.Sprintf("Version(%d)", uint8(v))
	}
}

// Effect is a statement's effect.
type Effect uint8

// Effect values.
const (
	Allow Effect = iota + 1
	Deny
)

// String returns "Allow" or "Deny".
func (e Effect) String() string {
	switch e {
	case Allow:
		return "Allow"
	case Deny:
		return "Deny"
	default:
		return fmt.Sprintf("Effect(%d)", uint8(e))
	}
}

// Statement is one statement. A nil Principal, Action or Resource is an
// absent element: identity policies have no principal, trust policies no
// resource.
type Statement struct {
	// Sid is the optional statement id; "" when absent.
	Sid string
	// Effect is Allow or Deny, as Parse and Render set it. The zero value
	// is not an effect: Encode writes it as Effect(0), which Parse and IAM
	// reject, and Allows never applies such a statement.
	Effect    Effect
	Principal PrincipalElement
	Action    Actions
	Resource  Resources
	// Condition is empty when the statement has none.
	Condition Condition
}

// PrincipalElement is the Principal or NotPrincipal element.
//
//sumtype:decl
type PrincipalElement interface{ isPrincipalElement() }

// Principal is the Principal element: the statement applies to Who.
type Principal struct{ Who Who }

// NotPrincipal is the NotPrincipal element: the statement applies to
// everyone except Who.
type NotPrincipal struct{ Who Who }

func (Principal) isPrincipalElement()    {}
func (NotPrincipal) isPrincipalElement() {}

// Who is the value of a principal element.
//
//sumtype:decl
type Who interface{ isWho() }

// Anyone is the string form "*".
type Anyone struct{}

// Named is the object form. Each list holds the values of one key; Parse
// never returns a Named with every list empty.
type Named struct {
	AWS           []string // account ids, account root ARNs, IAM ARNs, "*"
	CanonicalUser []string
	Federated     []string // identity providers: "cognito-identity.amazonaws.com", SAML provider ARNs
	Service       []string // service principals: "sns.amazonaws.com"
}

func (Anyone) isWho() {}
func (Named) isWho()  {}

// Actions is the Action or NotAction element.
//
//sumtype:decl
type Actions interface{ isActions() }

// Action lists the actions the statement covers, with * and ? wildcards.
type Action []string

// NotAction lists the actions the statement does not cover.
type NotAction []string

func (Action) isActions()    {}
func (NotAction) isActions() {}

// Resources is the Resource or NotResource element.
//
//sumtype:decl
type Resources interface{ isResources() }

// Resource lists the resource ARNs the statement covers, with * and ?
// wildcards.
type Resource []string

// NotResource lists the resources the statement does not cover.
type NotResource []string

func (Resource) isResources()    {}
func (NotResource) isResources() {}

// Condition is the Condition element as one clause per (operator, key),
// sorted by operator text and then key.
type Condition []Clause

// Clause is one condition key under one operator.
type Clause struct {
	Operator Operator
	Key      string
	Values   []Value
}

// Operator is a condition operator: a test with an optional set qualifier
// and an optional IfExists suffix, as in ForAnyValue:StringLikeIfExists.
type Operator struct {
	Set      SetQualifier
	Test     Test
	IfExists bool
}

// String formats the operator as written in a policy.
func (o Operator) String() string {
	s := o.Set.prefix() + o.Test.String()
	if o.IfExists {
		s += "IfExists"
	}
	return s
}

// SetQualifier is the multivalued-key prefix of an operator.
type SetQualifier uint8

// SetQualifier values.
const (
	SingleValued SetQualifier = iota
	ForAnyValue
	ForAllValues
)

func (q SetQualifier) prefix() string {
	switch q {
	case SingleValued:
		return ""
	case ForAnyValue:
		return "ForAnyValue:"
	case ForAllValues:
		return "ForAllValues:"
	default:
		return fmt.Sprintf("SetQualifier(%d):", uint8(q))
	}
}

// Test is the base condition operator.
type Test uint8

// Test values, in the order of the IAM condition operator reference.
const (
	StringEquals Test = iota + 1
	StringNotEquals
	StringEqualsIgnoreCase
	StringNotEqualsIgnoreCase
	StringLike
	StringNotLike
	NumericEquals
	NumericNotEquals
	NumericLessThan
	NumericLessThanEquals
	NumericGreaterThan
	NumericGreaterThanEquals
	DateEquals
	DateNotEquals
	DateLessThan
	DateLessThanEquals
	DateGreaterThan
	DateGreaterThanEquals
	Bool
	BinaryEquals
	IPAddress
	NotIPAddress
	ArnEquals
	ArnLike
	ArnNotEquals
	ArnNotLike
	Null
)

// String returns the operator name, such as "StringEquals" or "IpAddress".
func (t Test) String() string {
	switch t {
	case StringEquals:
		return "StringEquals"
	case StringNotEquals:
		return "StringNotEquals"
	case StringEqualsIgnoreCase:
		return "StringEqualsIgnoreCase"
	case StringNotEqualsIgnoreCase:
		return "StringNotEqualsIgnoreCase"
	case StringLike:
		return "StringLike"
	case StringNotLike:
		return "StringNotLike"
	case NumericEquals:
		return "NumericEquals"
	case NumericNotEquals:
		return "NumericNotEquals"
	case NumericLessThan:
		return "NumericLessThan"
	case NumericLessThanEquals:
		return "NumericLessThanEquals"
	case NumericGreaterThan:
		return "NumericGreaterThan"
	case NumericGreaterThanEquals:
		return "NumericGreaterThanEquals"
	case DateEquals:
		return "DateEquals"
	case DateNotEquals:
		return "DateNotEquals"
	case DateLessThan:
		return "DateLessThan"
	case DateLessThanEquals:
		return "DateLessThanEquals"
	case DateGreaterThan:
		return "DateGreaterThan"
	case DateGreaterThanEquals:
		return "DateGreaterThanEquals"
	case Bool:
		return "Bool"
	case BinaryEquals:
		return "BinaryEquals"
	case IPAddress:
		return "IpAddress"
	case NotIPAddress:
		return "NotIpAddress"
	case ArnEquals:
		return "ArnEquals"
	case ArnLike:
		return "ArnLike"
	case ArnNotEquals:
		return "ArnNotEquals"
	case ArnNotLike:
		return "ArnNotLike"
	case Null:
		return "Null"
	default:
		return fmt.Sprintf("Test(%d)", uint8(t))
	}
}

// parseTest returns the Test named s; names are case-sensitive.
func parseTest(s string) (Test, bool) {
	for t := StringEquals; t <= Null; t++ {
		if t.String() == s {
			return t, true
		}
	}
	return 0, false
}

// Value is one condition value.
//
//sumtype:decl
type Value interface{ isValue() }

// StringValue is a JSON string.
type StringValue string

// BoolValue is a JSON true or false.
type BoolValue bool

// NumberValue is a JSON number, kept as its literal text.
type NumberValue string

func (StringValue) isValue() {}
func (BoolValue) isValue()   {}
func (NumberValue) isValue() {}
