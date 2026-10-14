package policydoc_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// A stored policy reads as a document when Parse accepts it, and as none
// when it is empty or one Parse refuses.
func TestParseStored(t *testing.T) {
	const valid = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"*"}]}`
	want, err := policydoc.Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if got := policydoc.ParseStored(valid); got == nil || !reflect.DeepEqual(*got, want) {
		t.Errorf("ParseStored(valid) = %+v, want %+v", got, want)
	}
	for _, text := range []string{"", `{"Statement": 7}`, "not json"} {
		if got := policydoc.ParseStored(text); got != nil {
			t.Errorf("ParseStored(%q) = %+v, want nil", text, got)
		}
	}
}

func TestParseRecordedDocuments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want policydoc.Document
	}{
		{
			// The trust policy of a Lambda execution role, as IAM returns it
			// (iam-sts-kms.md 3.3): a single statement in a list, string
			// Action, no Resource.
			name: "lambda trust policy",
			in:   `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
			want: policydoc.Document{
				Version: policydoc.Version20121017,
				Statements: []policydoc.Statement{{
					Effect:    policydoc.Allow,
					Principal: policydoc.Principal{Who: policydoc.Named{Service: []string{"lambda.amazonaws.com"}}},
					Action:    policydoc.Action{"sts:AssumeRole"},
				}},
			},
		},
		{
			// The default KMS key policy (iam-sts-kms.md 5.3).
			name: "kms default key policy",
			in: `{
  "Version" : "2012-10-17",
  "Id" : "key-default-1",
  "Statement" : [ {
    "Sid" : "Enable IAM User Permissions",
    "Effect" : "Allow",
    "Principal" : {
      "AWS" : ["arn:aws:iam::111111111111:root"]
    },
    "Action" : "kms:*",
    "Resource" : "*"
  } ]
}`,
			want: policydoc.Document{
				Version: policydoc.Version20121017,
				ID:      "key-default-1",
				Statements: []policydoc.Statement{{
					Sid:       "Enable IAM User Permissions",
					Effect:    policydoc.Allow,
					Principal: policydoc.Principal{Who: policydoc.Named{AWS: []string{"arn:aws:iam::111111111111:root"}}},
					Action:    policydoc.Action{"kms:*"},
					Resource:  policydoc.Resource{"*"},
				}},
			},
		},
		{
			// A Lambda AddPermission statement (lambda-api.md 7.3), as a
			// single statement object rather than a list.
			name: "lambda permission statement",
			in: `{"Version":"2012-10-17","Id":"default","Statement":{"Sid":"sns","Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},
 "Action":"lambda:InvokeFunction","Resource":"arn:aws:lambda:us-east-1:111111111111:function:fn",
 "Condition":{"StringEquals":{"AWS:SourceAccount":"111111111111"},"ArnLike":{"AWS:SourceArn":"arn:aws:sns:us-east-1:111111111111:t"}}}}`,
			want: policydoc.Document{
				Version: policydoc.Version20121017,
				ID:      "default",
				Statements: []policydoc.Statement{{
					Sid:       "sns",
					Effect:    policydoc.Allow,
					Principal: policydoc.Principal{Who: policydoc.Named{Service: []string{"sns.amazonaws.com"}}},
					Action:    policydoc.Action{"lambda:InvokeFunction"},
					Resource:  policydoc.Resource{"arn:aws:lambda:us-east-1:111111111111:function:fn"},
					Condition: policydoc.Condition{
						{
							Operator: policydoc.Operator{Test: policydoc.ArnLike},
							Key:      "AWS:SourceArn",
							Values:   []policydoc.Value{policydoc.StringValue("arn:aws:sns:us-east-1:111111111111:t")},
						},
						{
							Operator: policydoc.Operator{Test: policydoc.StringEquals},
							Key:      "AWS:SourceAccount",
							Values:   []policydoc.Value{policydoc.StringValue("111111111111")},
						},
					},
				}},
			},
		},
		{
			name: "public principal, not-elements and qualified operators",
			in: `{"Statement":[{"Effect":"Deny","NotPrincipal":"*","NotAction":["s3:Get*","s3:List*"],"NotResource":["arn:aws:s3:::b/*"],
 "Condition":{"ForAnyValue:StringLikeIfExists":{"aws:TagKeys":["a*",true,3]},"Bool":{"aws:SecureTransport":false}}}]}`,
			want: policydoc.Document{
				Statements: []policydoc.Statement{{
					Effect:    policydoc.Deny,
					Principal: policydoc.NotPrincipal{Who: policydoc.Anyone{}},
					Action:    policydoc.NotAction{"s3:Get*", "s3:List*"},
					Resource:  policydoc.NotResource{"arn:aws:s3:::b/*"},
					Condition: policydoc.Condition{
						{
							Operator: policydoc.Operator{Test: policydoc.Bool},
							Key:      "aws:SecureTransport",
							Values:   []policydoc.Value{policydoc.BoolValue(false)},
						},
						{
							Operator: policydoc.Operator{Set: policydoc.ForAnyValue, Test: policydoc.StringLike, IfExists: true},
							Key:      "aws:TagKeys",
							Values:   []policydoc.Value{policydoc.StringValue("a*"), policydoc.BoolValue(true), policydoc.NumberValue("3")},
						},
					},
				}},
			},
		},
		{
			// An operator IAM does not know is accepted while its block is
			// empty, and contributes nothing.
			name: "unknown operator with an empty block",
			in:   `{"Version":"2008-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"],"Federated":"cognito-identity.amazonaws.com","CanonicalUser":"abc"},"Action":[],"Condition":{"Mystery":{}}}]}`,
			want: policydoc.Document{
				Version: policydoc.Version20081017,
				Statements: []policydoc.Statement{{
					Effect: policydoc.Allow,
					Principal: policydoc.Principal{Who: policydoc.Named{
						AWS:           []string{"*"},
						CanonicalUser: []string{"abc"},
						Federated:     []string{"cognito-identity.amazonaws.com"},
					}},
					Action: policydoc.Action{},
				}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := policydoc.Parse([]byte(tc.in))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse =\n%#v\nwant\n%#v", got, tc.want)
			}
		})
	}
}

func TestParseRejectsSyntaxErrors(t *testing.T) {
	tests := map[string]string{
		"not json":                          `{"Version":`,
		"trailing content":                  `{"Statement":{"Effect":"Allow"}} {}`,
		"not an object":                     `["Statement"]`,
		"unknown top-level element":         `{"Statement":{"Effect":"Allow"},"Extra":1}`,
		"element names are case-sensitive":  `{"statement":{"Effect":"Allow"}}`,
		"unknown version":                   `{"Version":"2013-01-01","Statement":{"Effect":"Allow"}}`,
		"id not a string":                   `{"Id":1,"Statement":{"Effect":"Allow"}}`,
		"missing statement":                 `{"Version":"2012-10-17"}`,
		"empty statement list":              `{"Statement":[]}`,
		"statement not an object":           `{"Statement":["x"]}`,
		"unknown statement element":         `{"Statement":{"Effect":"Allow","Actions":"s3:*"}}`,
		"missing effect":                    `{"Statement":{"Action":"s3:*"}}`,
		"unknown effect":                    `{"Statement":{"Effect":"Permit"}}`,
		"action and not action":             `{"Statement":{"Effect":"Allow","Action":"a:b","NotAction":"a:c"}}`,
		"resource and not resource":         `{"Statement":{"Effect":"Allow","Resource":"*","NotResource":"*"}}`,
		"principal and not principal":       `{"Statement":{"Effect":"Allow","Principal":"*","NotPrincipal":"*"}}`,
		"action not a string":               `{"Statement":{"Effect":"Allow","Action":1}}`,
		"action list with a number":         `{"Statement":{"Effect":"Allow","Action":["a:b",1]}}`,
		"action list with null":             `{"Statement":{"Effect":"Allow","Action":["a:b",null]}}`,
		"sid not a string":                  `{"Statement":{"Effect":"Allow","Sid":1}}`,
		"principal string other than star":  `{"Statement":{"Effect":"Allow","Principal":"sns.amazonaws.com"}}`,
		"principal unknown key":             `{"Statement":{"Effect":"Allow","Principal":{"Users":"x"}}}`,
		"principal empty object":            `{"Statement":{"Effect":"Allow","Principal":{}}}`,
		"principal empty list":              `{"Statement":{"Effect":"Allow","Principal":{"AWS":[]}}}`,
		"condition not an object":           `{"Statement":{"Effect":"Allow","Condition":"x"}}`,
		"condition block not an object":     `{"Statement":{"Effect":"Allow","Condition":{"StringEquals":"x"}}}`,
		"condition unknown operator in use": `{"Statement":{"Effect":"Allow","Condition":{"Mystery":{"k":"v"}}}}`,
		"condition value null":              `{"Statement":{"Effect":"Allow","Condition":{"StringEquals":{"k":null}}}}`,
		"condition value object":            `{"Statement":{"Effect":"Allow","Condition":{"StringEquals":{"k":{}}}}}`,
		"condition empty key":               `{"Statement":{"Effect":"Allow","Condition":{"StringEquals":{"":"v"}}}}`,
		"condition qualifier twice":         `{"Statement":{"Effect":"Allow","Condition":{"ForAnyValue:ForAllValues:StringLike":{"k":"v"}}}}`,
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := policydoc.Parse([]byte(in))
			if err == nil {
				t.Fatal("Parse accepted the document")
			}
			if got, want := err.Message(), "Syntax errors in policy."; got != want {
				t.Errorf("Message() = %q, want %q (detail: %v)", got, want, err)
			}
		})
	}
}

// IAM's syntax stage matches the effect case-insensitively and its legacy
// parser exactly, as moto's validator (iam-sts-kms.md 3.3) models it.
func TestParseRejectsEffectCaseAsLegacy(t *testing.T) {
	_, err := policydoc.Parse([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"allow","Action":"s3:*","Resource":"*"}}`))
	if err == nil {
		t.Fatal("Parse accepted a lowercase effect")
	}
	if got, want := err.Message(), "The policy failed legacy parsing"; got != want {
		t.Errorf("Message() = %q, want %q", got, want)
	}
}

// A syntax error anywhere outranks a legacy error earlier in the document.
func TestParseReportsSyntaxBeforeLegacy(t *testing.T) {
	_, err := policydoc.Parse([]byte(`{"Statement":[{"Effect":"allow"},{"Effect":"Allow","Bogus":1}]}`))
	if err == nil || err.Message() != "Syntax errors in policy." {
		t.Fatalf("Parse error = %v, want the syntax error", err)
	}
}
