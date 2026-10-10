package policydoc_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

const (
	queueARN = "arn:aws:sqs:us-east-1:111111111111:orders"
	topicARN = "arn:aws:sns:us-east-1:111111111111:events"
	roleARN  = "arn:aws:iam::111111111111:role/worker"
)

// allowed reports whether d lets the request through.
func allowed(d policydoc.Decision) bool {
	_, ok := d.(policydoc.Allowed)
	return ok
}

func TestAllowsIdentityPolicy(t *testing.T) {
	doc := mustParse(t, `{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":["sqs:Receive*","sqs:DeleteMessage","logs:*"],"Resource":["arn:aws:sqs:us-east-1:111111111111:*","*"]},
 {"Effect":"Deny","Action":"logs:DeleteLogGroup","Resource":"*"},
 {"Effect":"Allow","NotAction":"s3:*","NotResource":"arn:aws:sns:*:*:secret-?"}]}`)
	tests := []struct {
		name     string
		action   string
		resource string
		want     policydoc.Decision
	}{
		{"wildcard action", "sqs:ReceiveMessage", queueARN, policydoc.Allowed{}},
		{"action names ignore case", "SQS:receivemessage", queueARN, policydoc.Allowed{}},
		{"exact action", "sqs:DeleteMessage", queueARN, policydoc.Allowed{}},
		{"explicit deny wins", "logs:DeleteLogGroup", "arn:aws:logs:us-east-1:111111111111:log-group:g", policydoc.ExplicitDeny{Statement: policydoc.StatementRef{Index: 1}}},
		{"other allowed log action", "logs:PutLogEvents", "arn:aws:logs:us-east-1:111111111111:log-group:g", policydoc.Allowed{}},
		{"not action covers other services", "sns:Publish", topicARN, policydoc.Allowed{}},
		{"not resource excludes a match", "sns:Publish", "arn:aws:sns:us-east-1:111111111111:secret-1", policydoc.ImplicitDeny{}},
		{"question mark matches one character", "sns:Publish", "arn:aws:sns:us-east-1:111111111111:secret-10", policydoc.Allowed{}},
		{"not action excludes s3", "s3:GetObject", "arn:aws:s3:::b/k", policydoc.ImplicitDeny{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := policydoc.Allows(doc, policydoc.Query{
				Principal: policydoc.AWSCaller{ARN: roleARN},
				Action:    tc.action,
				Resource:  tc.resource,
			})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Allows(%s on %s) = %#v, want %#v", tc.action, tc.resource, got, tc.want)
			}
		})
	}
}

func TestAllowsResourceNamesAreCaseSensitive(t *testing.T) {
	doc := mustParse(t, `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sqs:*","Resource":"arn:aws:sqs:us-east-1:111111111111:Orders"}}`)
	if allowed(policydoc.Allows(doc, policydoc.Query{Action: "sqs:SendMessage", Resource: queueARN})) {
		t.Error("Allows matched a resource that differs in case")
	}
}

func TestAllowsMatchesPrincipals(t *testing.T) {
	tests := []struct {
		name      string
		principal string
		caller    policydoc.Caller
		want      bool
	}{
		{"star string allows a service", `"*"`, policydoc.ServiceCaller{Name: "sns.amazonaws.com"}, true},
		{"star string allows anonymous", `"*"`, policydoc.Anonymous{}, true},
		{"AWS star allows a role", `{"AWS":"*"}`, policydoc.AWSCaller{ARN: roleARN}, true},
		{"AWS star allows a service", `{"AWS":"*"}`, policydoc.ServiceCaller{Name: "sns.amazonaws.com"}, true},
		{"service principal", `{"Service":["events.amazonaws.com","sns.amazonaws.com"]}`, policydoc.ServiceCaller{Name: "sns.amazonaws.com"}, true},
		{"other service principal", `{"Service":"events.amazonaws.com"}`, policydoc.ServiceCaller{Name: "sns.amazonaws.com"}, false},
		{"account id", `{"AWS":"111111111111"}`, policydoc.AWSCaller{ARN: roleARN}, true},
		{"account root", `{"AWS":"arn:aws:iam::111111111111:root"}`, policydoc.AWSCaller{ARN: roleARN}, true},
		{"other account", `{"AWS":"arn:aws:iam::222222222222:root"}`, policydoc.AWSCaller{ARN: roleARN}, false},
		{"exact role", `{"AWS":"arn:aws:iam::111111111111:role/worker"}`, policydoc.AWSCaller{ARN: roleARN}, true},
		{"other role", `{"AWS":"arn:aws:iam::111111111111:role/other"}`, policydoc.AWSCaller{ARN: roleARN}, false},
		{"account principal is not anonymous", `{"AWS":"111111111111"}`, policydoc.Anonymous{}, false},
		{"service is not an account", `{"AWS":"111111111111"}`, policydoc.ServiceCaller{Name: "sns.amazonaws.com"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustParse(t, `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":`+tc.principal+`,"Action":"sqs:SendMessage","Resource":"`+queueARN+`"}}`)
			q := policydoc.Query{Principal: tc.caller, Action: "sqs:SendMessage", Resource: queueARN}
			if got := allowed(policydoc.Allows(doc, q)); got != tc.want {
				t.Errorf("Principal %s for %#v: allowed = %v, want %v", tc.principal, tc.caller, got, tc.want)
			}
			notDoc := mustParse(t, `{"Version":"2012-10-17","Statement":{"Effect":"Allow","NotPrincipal":`+tc.principal+`,"Action":"sqs:SendMessage","Resource":"`+queueARN+`"}}`)
			if got := allowed(policydoc.Allows(notDoc, q)); got == tc.want {
				t.Errorf("NotPrincipal %s for %#v: allowed = %v, want %v", tc.principal, tc.caller, got, !tc.want)
			}
		})
	}
}

// The SNS-to-SQS queue policy pattern: the topic is named in an
// aws:SourceArn condition, evaluated when the query supplies the key.
func TestAllowsEvaluatesSuppliedConditionKeys(t *testing.T) {
	doc := mustParse(t, `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"`+queueARN+`",
 "Condition":{"ArnEquals":{"aws:SourceArn":"`+topicARN+`"},"StringEquals":{"aws:SourceAccount":"111111111111"}}}}`)
	q := policydoc.Query{Principal: policydoc.ServiceCaller{Name: "sns.amazonaws.com"}, Action: "sqs:SendMessage", Resource: queueARN}

	t.Run("no context: assumed and reported", func(t *testing.T) {
		want := policydoc.Allowed{Assumed: doc.Statements[0].Condition}
		if got := policydoc.Allows(doc, q); !reflect.DeepEqual(got, want) {
			t.Errorf("Allows = %#v, want %#v", got, want)
		}
	})
	t.Run("keys match case-insensitively", func(t *testing.T) {
		q := q
		q.Context = map[string]string{"AWS:SourceArn": topicARN, "aws:sourceaccount": "111111111111"}
		if got := policydoc.Allows(doc, q); !reflect.DeepEqual(got, policydoc.Allowed{}) {
			t.Errorf("Allows = %#v, want allowed with nothing assumed", got)
		}
	})
	t.Run("other topic", func(t *testing.T) {
		q := q
		q.Context = map[string]string{"aws:SourceArn": "arn:aws:sns:us-east-1:111111111111:other"}
		if got := policydoc.Allows(doc, q); got != (policydoc.ImplicitDeny{}) {
			t.Errorf("Allows = %#v for another topic, want ImplicitDeny", got)
		}
	})
}

func TestAllowsConditionOperators(t *testing.T) {
	tests := []struct {
		operator, values, context string
		want                      bool
	}{
		{"StringEquals", `"a"`, "a", true},
		{"StringEquals", `["x","a"]`, "a", true},
		{"StringEquals", `"A"`, "a", false},
		{"StringNotEquals", `["x","y"]`, "a", true},
		{"StringNotEquals", `["x","a"]`, "a", false},
		{"StringEqualsIgnoreCase", `"A"`, "a", true},
		{"StringNotEqualsIgnoreCase", `"A"`, "a", false},
		{"StringLike", `"arn:*:t?"`, "arn:aws:t1", true},
		{"StringLike", `"arn:*:t?"`, "arn:aws:t10", false},
		{"StringNotLike", `"arn:*"`, "arn:aws:t1", false},
		{"ArnLike", `"arn:aws:sns:*:111111111111:*"`, topicARN, true},
		{"ArnEquals", `"arn:aws:sns:*:222222222222:*"`, topicARN, false},
		{"ArnNotEquals", `"arn:aws:sns:*:222222222222:*"`, topicARN, true},
		{"ArnNotLike", `"` + topicARN + `"`, topicARN, false},
		// AWS matches the six ARN elements one by one, so a pattern or a
		// value with fewer elements matches nothing, even where a * could
		// span the missing colons. A * in the resource element still spans
		// the colons inside it.
		{"ArnLike", `"arn:aws:sns:us-east-1:*"`, topicARN, false},
		{"ArnEquals", `"arn:aws:*:111111111111:events"`, topicARN, false},
		{"ArnNotLike", `"arn:aws:sns:us-east-1:*"`, topicARN, true},
		{"ArnLike", `"arn:aws:sns:us-*:*:*"`, topicARN, true},
		{"ArnLike", `"*"`, "not-an-arn", false},
		{"ArnLike", `"arn:aws:lambda:*:*:function:*"`, "arn:aws:lambda:us-east-1:111111111111:function:worker:live", true},
		{"StringEqualsIfExists", `"a"`, "b", false},
		{"StringEquals", `true`, "true", true},
		{"StringEquals", `7`, "7", true},
	}
	for _, tc := range tests {
		t.Run(tc.operator+" "+tc.values+" "+tc.context, func(t *testing.T) {
			doc := mustParse(t, `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sns:Publish","Resource":"*","Condition":{"`+tc.operator+`":{"k":`+tc.values+`}}}}`)
			var want policydoc.Decision = policydoc.ImplicitDeny{}
			if tc.want {
				want = policydoc.Allowed{}
			}
			got := policydoc.Allows(doc, policydoc.Query{Action: "sns:Publish", Resource: topicARN, Context: map[string]string{"k": tc.context}})
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Allows = %#v, want %#v", got, want)
			}
		})
	}
}

// Context keys match case-insensitively, so two spellings of one key with
// different values leave the key's value unknown: the clause is assumed for
// an Allow statement and unsettled for a Deny statement, on every call
// whatever the map's iteration order. Spellings that agree are one value.
func TestAllowsTreatsConflictingContextSpellingsAsUnknown(t *testing.T) {
	doc := mustParse(t, `{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":"sns:Publish","Resource":"*","Condition":{"StringEquals":{"k":"a"}}},
 {"Effect":"Deny","Action":"sns:Publish","Resource":"*","Condition":{"StringEquals":{"k":"b"}}}]}`)
	allow, deny := doc.Statements[0], doc.Statements[1]
	q := policydoc.Query{Action: "sns:Publish", Resource: topicARN, Context: map[string]string{"k": "a", "K": "b"}}
	want := policydoc.Allowed{
		Assumed:   allow.Condition,
		Unsettled: []policydoc.UnsettledDeny{{Statement: policydoc.StatementRef{Index: 1}, Unevaluated: deny.Condition}},
	}
	for range 200 {
		if got := policydoc.Allows(doc, q); !reflect.DeepEqual(got, want) {
			t.Fatalf("Allows = %#v, want %#v", got, want)
		}
	}
	q.Context = map[string]string{"k": "a", "K": "a"}
	if got := policydoc.Allows(doc, q); !reflect.DeepEqual(got, policydoc.Allowed{}) {
		t.Errorf("Allows with agreeing spellings = %#v, want Allowed with nothing assumed", got)
	}
}

// Operators outside the string and ARN families, set qualifiers, and keys
// the query does not supply are assumed to hold for an Allow statement. A
// Deny statement resting on such a clause does not deny: it is reported as
// unsettled.
func TestAllowsAssumesUnsupportedConditions(t *testing.T) {
	doc := mustParse(t, `{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":"s3:*","Resource":"*","Condition":{"NumericLessThan":{"k":"3"},"ForAnyValue:StringEquals":{"k":"a"}}},
 {"Sid":"DenyInsecure","Effect":"Deny","Action":"s3:DeleteObject","Resource":"*","Condition":{"Bool":{"aws:SecureTransport":false}}}]}`)
	allow, deny := doc.Statements[0], doc.Statements[1]
	q := policydoc.Query{Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k", Context: map[string]string{"k": "a"}}
	if got, want := policydoc.Allows(doc, q), (policydoc.Allowed{Assumed: allow.Condition}); !reflect.DeepEqual(got, want) {
		t.Errorf("Allows(GetObject) = %#v, want %#v", got, want)
	}
	q.Action = "s3:DeleteObject"
	want := policydoc.Allowed{
		Assumed:   allow.Condition,
		Unsettled: []policydoc.UnsettledDeny{{Statement: policydoc.StatementRef{Index: 1, Sid: "DenyInsecure"}, Unevaluated: deny.Condition}},
	}
	if got := policydoc.Allows(doc, q); !reflect.DeepEqual(got, want) {
		t.Errorf("Allows(DeleteObject) = %#v, want %#v", got, want)
	}
}

// An Allow statement that matches outright makes the decision firm, whatever
// another Allow statement assumes.
func TestAllowsReportsNoAssumptionsWhenAnAllowIsFirm(t *testing.T) {
	doc := mustParse(t, `{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"Bool":{"aws:SecureTransport":true}}},
 {"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`)
	if got := policydoc.Allows(doc, policydoc.Query{Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k"}); !reflect.DeepEqual(got, policydoc.Allowed{}) {
		t.Errorf("Allows = %#v, want Allowed with nothing assumed", got)
	}
}

// A Deny statement denies when every clause is evaluated and holds; the
// decision names the first such statement.
func TestAllowsReportsTheFirstFirmDeny(t *testing.T) {
	doc := mustParse(t, `{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Principal":"*","Action":"sqs:*","Resource":"*"},
 {"Sid":"OnlyOurAccount","Effect":"Deny","Principal":"*","Action":"sqs:SendMessage","Resource":"*","Condition":{"StringNotEquals":{"aws:SourceAccount":"111111111111"}}},
 {"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"*","Condition":{"ArnNotLike":{"aws:SourceArn":"arn:aws:sns:*:111111111111:*"}}}]}`)
	q := policydoc.Query{
		Principal: policydoc.ServiceCaller{Name: "sns.amazonaws.com"},
		Action:    "sqs:SendMessage",
		Resource:  queueARN,
		Context:   map[string]string{"aws:SourceArn": "arn:aws:sns:us-east-1:222222222222:t", "aws:SourceAccount": "222222222222"},
	}
	want := policydoc.ExplicitDeny{Statement: policydoc.StatementRef{Index: 1, Sid: "OnlyOurAccount"}}
	if got := policydoc.Allows(doc, q); got != want {
		t.Errorf("Allows = %#v, want %#v", got, want)
	}
	q.Action = "sqs:ReceiveMessage"
	want = policydoc.ExplicitDeny{Statement: policydoc.StatementRef{Index: 2}}
	if got := policydoc.Allows(doc, q); got != want {
		t.Errorf("Allows = %#v, want %#v", got, want)
	}
}

// In a 2012-10-17 document ${*}, ${?} and ${$} are a literal *, ? and $;
// a 2008-10-17 document has no escapes, so its * stays a wildcard.
func TestAllowsReadsEscapesIn2012Documents(t *testing.T) {
	const statement = `"Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::uploads/${*}${?}${$}{x}"}`
	current := mustParse(t, `{"Version":"2012-10-17",`+statement+`}`)
	old := mustParse(t, `{"Version":"2008-10-17",`+statement+`}`)
	tests := []struct {
		name     string
		doc      policydoc.Document
		resource string
		want     bool
	}{
		{"escaped name itself", current, "arn:aws:s3:::uploads/*?${x}", true},
		{"escaped star is no wildcard", current, "arn:aws:s3:::uploads/a?${x}", false},
		{"escaped question mark is no wildcard", current, "arn:aws:s3:::uploads/*a${x}", false},
		{"old version: star is a wildcard", old, "arn:aws:s3:::uploads/${secret}${?}${$}{x}", true},
		{"old version: escape text is literal", old, "arn:aws:s3:::uploads/*?${x}", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := allowed(policydoc.Allows(tc.doc, policydoc.Query{Action: "s3:GetObject", Resource: tc.resource}))
			if got != tc.want {
				t.Errorf("Allows(%s) = %t, want %t", tc.resource, got, tc.want)
			}
		})
	}
}

// The escapes stand for their character in any string that takes policy
// variables, so the equality operators decode them as the wildcard ones do.
func TestEqualityConditionsReadEscapesIn2012Documents(t *testing.T) {
	const arn = "arn:aws:sns:us-east-1:111111111111:a"
	tests := []struct {
		version, operator, context string
		want                       bool
	}{
		{"2012-10-17", "StringEquals", arn + "$b", true},
		{"2012-10-17", "StringEquals", arn + "${$}b", false},
		{"2012-10-17", "StringNotEquals", arn + "$b", false},
		{"2012-10-17", "StringEqualsIgnoreCase", strings.ToUpper(arn) + "$B", true},
		{"2012-10-17", "StringNotEqualsIgnoreCase", arn + "$b", false},
		{"2008-10-17", "StringEquals", arn + "${$}b", true},
		{"2008-10-17", "StringEquals", arn + "$b", false},
	}
	for _, tc := range tests {
		t.Run(tc.version+" "+tc.operator+" "+tc.context, func(t *testing.T) {
			doc := mustParse(t, `{"Version":"`+tc.version+`","Statement":{"Effect":"Allow","Action":"sns:Publish","Resource":"*","Condition":{"`+tc.operator+`":{"aws:SourceArn":"`+arn+`${$}b"}}}}`)
			got := allowed(policydoc.Allows(doc, policydoc.Query{Action: "sns:Publish", Resource: topicARN, Context: map[string]string{"aws:SourceArn": tc.context}}))
			if got != tc.want {
				t.Errorf("Allows with aws:SourceArn %s = %t, want %t", tc.context, got, tc.want)
			}
		})
	}
}

func TestAllowsWithoutActionsMatchesNothing(t *testing.T) {
	doc := mustParse(t, `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":"*","Resource":"*"}}`)
	if got := policydoc.Allows(doc, policydoc.Query{Action: "kms:Decrypt", Resource: "*"}); got != (policydoc.ImplicitDeny{}) {
		t.Errorf("Allows = %#v for a statement without Action, want ImplicitDeny", got)
	}
}
