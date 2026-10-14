package awsquery_test

import (
	"errors"
	"maps"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/registry"
)

type golden struct {
	status int
	header http.Header
	body   string
}

func checkResponse(t *testing.T, got awsquery.Response, want golden) {
	t.Helper()
	if got.Status != want.status {
		t.Errorf("status = %d, want %d", got.Status, want.status)
	}
	wantHeader := http.Header{
		"Content-Type":     {"text/xml"},
		"X-Amzn-Requestid": {requestID},
		"Content-Length":   {strconv.Itoa(len(want.body))},
	}
	maps.Copy(wantHeader, want.header)
	if !reflect.DeepEqual(got.Header, wantHeader) {
		t.Errorf("headers =\n%v\nwant\n%v", got.Header, wantHeader)
	}
	if string(got.Body) != want.body {
		t.Errorf("body =\n%s\nwant\n%s", got.Body, want.body)
	}
}

func encode(t *testing.T, op *registry.Operation, out *dynStruct) awsquery.Response {
	t.Helper()
	resp, aerr := awsquery.Codec{}.Encode(op, requestID, out, http.StatusOK)
	if aerr != nil {
		t.Fatalf("Encode: %v", aerr.AsError())
	}
	return resp
}

const (
	stsNamespace = "https://sts.amazonaws.com/doc/2011-06-15/"
	iamNamespace = "https://iam.amazonaws.com/doc/2010-05-08/"
	snsNamespace = "http://sns.amazonaws.com/doc/2010-03-31/"
	cwNamespace  = "http://monitoring.amazonaws.com/doc/2010-08-01/"
)

// wire-json-query.md 8.1: the GetCallerIdentity body of the STS API
// reference, without its indentation. The serializer writes members in the
// order Serialize visits them, so the schema lists them in the documented
// order, Arn first.
func TestGoldenGetCallerIdentity(t *testing.T) {
	t.Parallel()
	output := structure("com.amazonaws.sts#GetCallerIdentityResponse", member("Arn", str), member("UserId", str), member("Account", str))
	svc := newService("sts", "2011-06-15", stsNamespace, opSpec{name: "GetCallerIdentity", out: output})
	out := &dynStruct{schema: output, fields: map[string]any{
		"Arn": "arn:aws:iam::123456789012:user/Alice", "UserId": "AIDACKCEVSQ6C2EXAMPLE", "Account": "123456789012",
	}}
	checkResponse(t, encode(t, operation(t, svc, "GetCallerIdentity"), out), golden{
		status: 200,
		body: `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">` +
			`<GetCallerIdentityResult>` +
			`<Arn>arn:aws:iam::123456789012:user/Alice</Arn>` +
			`<UserId>AIDACKCEVSQ6C2EXAMPLE</UserId>` +
			`<Account>123456789012</Account>` +
			`</GetCallerIdentityResult>` +
			`<ResponseMetadata><RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ResponseMetadata>` +
			`</GetCallerIdentityResponse>`,
	})
}

// iamRole is IAM's Role in model order, its trust policy a
// policyDocumentType.
func iamRole() *smithy.Schema {
	policy := scalar("com.amazonaws.iam#policyDocumentType", smithy.TypeString)
	return structure("com.amazonaws.iam#Role",
		member("Path", str), member("RoleName", str), member("RoleId", str), member("Arn", str),
		member("CreateDate", tstamp), member("AssumeRolePolicyDocument", policy), member("MaxSessionDuration", integer))
}

// iam-sts-kms.md 1.2 and 3.3, wire-json-query.md 8.4: GetRole with the
// trust policy percent-encoded as IAM sends it, in the recorded form, and
// CreateDate in whole seconds. A '+' in the document is %2B and a space
// %20, so Terraform's QueryUnescape reads the document back unchanged.
func TestGoldenGetRoleEncodesThePolicyDocument(t *testing.T) {
	t.Parallel()
	role := iamRole()
	output := structure("com.amazonaws.iam#GetRoleResponse", member("Role", role))
	svc := newService("iam", "2010-05-08", iamNamespace, opSpec{name: "GetRole", out: output})
	op := operation(t, svc, "GetRole")
	roleValue := func(policy string) *dynStruct {
		return &dynStruct{schema: output, fields: map[string]any{"Role": dynStruct{schema: role, fields: map[string]any{
			"Path": "/", "RoleName": "worker", "RoleId": "AROADBQP57FF2AEXAMPLE", "Arn": "arn:aws:iam::123456789012:role/worker",
			"CreateDate": time.Date(2020, 8, 9, 16, 55, 28, 0, time.UTC), "AssumeRolePolicyDocument": policy, "MaxSessionDuration": int32(3600),
		}}}}
	}
	recorded := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	checkResponse(t, encode(t, op, roleValue(recorded)), golden{
		status: 200,
		body: `<GetRoleResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetRoleResult><Role>` +
			`<Path>/</Path><RoleName>worker</RoleName><RoleId>AROADBQP57FF2AEXAMPLE</RoleId>` +
			`<Arn>arn:aws:iam::123456789012:role/worker</Arn><CreateDate>2020-08-09T16:55:28Z</CreateDate>` +
			`<AssumeRolePolicyDocument>%7B%22Version%22%3A%222012-10-17%22%2C%22Statement%22%3A%5B%7B%22Effect%22%3A%22Allow%22%2C%22Principal%22%3A%7B%22Service%22%3A%22lambda.amazonaws.com%22%7D%2C%22Action%22%3A%22sts%3AAssumeRole%22%7D%5D%7D</AssumeRolePolicyDocument>` +
			`<MaxSessionDuration>3600</MaxSessionDuration>` +
			`</Role></GetRoleResult><ResponseMetadata><RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ResponseMetadata></GetRoleResponse>`,
	})
	body := string(encode(t, op, roleValue(`{"Condition":{"StringLike":{"k":"a+b c"}}}`)).Body)
	if want := `<AssumeRolePolicyDocument>%7B%22Condition%22%3A%7B%22StringLike%22%3A%7B%22k%22%3A%22a%2Bb%20c%22%7D%7D%7D</AssumeRolePolicyDocument>`; !strings.Contains(body, want) {
		t.Errorf("body %s\nlacks %s", body, want)
	}
}

// wire-json-query.md 8.5 and 8.3: an empty list is <Roles/>, a boolean is
// lowercase, and a timestamp keeps its milliseconds.
func TestGoldenListRolesEmptyCollection(t *testing.T) {
	t.Parallel()
	output := structure("com.amazonaws.iam#ListRolesResponse",
		member("Roles", listOf("com.amazonaws.iam#roleListType", member("member", iamRole()))),
		member("IsTruncated", boolean), member("Marker", str))
	svc := newService("iam", "2010-05-08", iamNamespace, opSpec{name: "ListRoles", out: output})
	out := &dynStruct{schema: output, fields: map[string]any{"Roles": []any{}, "IsTruncated": false}}
	checkResponse(t, encode(t, operation(t, svc, "ListRoles"), out), golden{
		status: 200,
		body: `<ListRolesResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><ListRolesResult>` +
			`<Roles/><IsTruncated>false</IsTruncated>` +
			`</ListRolesResult><ResponseMetadata><RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ResponseMetadata></ListRolesResponse>`,
	})
}

// iam-sts-kms.md 1.2: an operation without output (DeleteRole) answers the
// envelope with ResponseMetadata alone, never an empty body.
func TestGoldenUnitOutputKeepsTheEnvelope(t *testing.T) {
	t.Parallel()
	svc := newService("iam", "2010-05-08", iamNamespace, opSpec{name: "DeleteRole", out: unit})
	resp, aerr := awsquery.Codec{}.Encode(operation(t, svc, "DeleteRole"), requestID, nil, http.StatusOK)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	checkResponse(t, resp, golden{
		status: 200,
		body: `<DeleteRoleResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/">` +
			`<ResponseMetadata><RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ResponseMetadata></DeleteRoleResponse>`,
	})
}

// wire-json-query.md 9.1: the Smithy specification's error, with an extra
// member of the error structure after Message.
func TestGoldenErrorResponse(t *testing.T) {
	t.Parallel()
	svc := newService("greeter", "2020-01-08", "https://example.com/", opSpec{name: "Greet"})
	e := &awserr.Error{
		ID:      awserr.Identity{Shape: "test#InvalidGreeting", QueryCode: "InvalidGreeting", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageUpper},
		Message: "Hi",
		Fields:  []awserr.Field{{Name: "AnotherSetting", Value: "setting"}},
		Origin:  awserr.Modeled{},
	}
	resp := awsquery.Codec{}.EncodeError(svc, "c6104cbe-af31-11e0-8154-cbc7ccf896c7", e)
	want := `<ErrorResponse><Error><Type>Sender</Type><Code>InvalidGreeting</Code><Message>Hi</Message>` +
		`<AnotherSetting>setting</AnotherSetting></Error><RequestId>c6104cbe-af31-11e0-8154-cbc7ccf896c7</RequestId></ErrorResponse>`
	if resp.Status != 400 || string(resp.Body) != want {
		t.Errorf("EncodeError = %d %s\nwant 400 %s", resp.Status, resp.Body, want)
	}
}

// sns.md 1 and error-matrix.md 3 (recorded): SNS's ErrorResponse carries no
// xmlns although its success responses do, and Code is the awsQueryError
// code, not the shape name.
func TestGoldenSNSErrorHasNoNamespace(t *testing.T) {
	t.Parallel()
	svc := newService("sns", "2010-03-31", snsNamespace, opSpec{name: "GetTopicAttributes"})
	e := &awserr.Error{
		ID:      awserr.Identity{Shape: "com.amazonaws.sns#NotFoundException", QueryCode: "NotFound", Status: 404, Fault: awserr.Sender, MessageKey: awserr.MessageLower},
		Message: "Topic does not exist",
		Origin:  awserr.Modeled{},
	}
	checkResponse(t, awsquery.Codec{}.EncodeError(svc, requestID, e), golden{
		status: 404,
		body: `<ErrorResponse><Error><Type>Sender</Type><Code>NotFound</Code><Message>Topic does not exist</Message></Error>` +
			`<RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ErrorResponse>`,
	})
}

// iam-sts-kms.md 1.2: the recorded IAM error declares the IAM namespace,
// which the generated descriptor carries from the overlay.
func TestGoldenIAMNoSuchEntity(t *testing.T) {
	t.Parallel()
	svc := iamapi.Service()
	e := &awserr.Error{
		ID:      awserr.Identity{Shape: "com.amazonaws.iam#NoSuchEntityException", QueryCode: "NoSuchEntity", Status: 404, Fault: awserr.Sender, MessageKey: awserr.MessageLower},
		Message: "The user with name foobar cannot be found.",
		Origin:  awserr.Modeled{},
	}
	checkResponse(t, awsquery.Codec{}.EncodeError(svc, "358299bb-b7f8-11e2-8d39-e3a125f625ee", e), golden{
		status: 404,
		header: http.Header{"X-Amzn-Requestid": {"358299bb-b7f8-11e2-8d39-e3a125f625ee"}},
		body: `<ErrorResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><Error><Type>Sender</Type><Code>NoSuchEntity</Code>` +
			`<Message>The user with name foobar cannot be found.</Message></Error>` +
			`<RequestId>358299bb-b7f8-11e2-8d39-e3a125f625ee</RequestId></ErrorResponse>`,
	})
}

// error-matrix.md 3 (recorded): CloudWatch declares its namespace on
// ErrorResponse, sends no Message for ResourceNotFound, and answers it with
// awsQueryError's 404 rather than httpError's 400.
func TestGoldenCloudWatchResourceNotFound(t *testing.T) {
	t.Parallel()
	svc := cloudwatchapi.Service()
	e := &awserr.Error{
		ID: awserr.Identity{
			Shape: "com.amazonaws.cloudwatch#ResourceNotFound", QueryCode: "ResourceNotFound", Status: 400, QueryModeStatus: 404,
			Fault: awserr.Sender, MessageKey: awserr.MessageLower,
		},
		Origin: awserr.Modeled{},
	}
	checkResponse(t, awsquery.Codec{}.EncodeError(svc, requestID, e), golden{
		status: 404,
		body: `<ErrorResponse xmlns="http://monitoring.amazonaws.com/doc/2010-08-01/"><Error><Type>Sender</Type><Code>ResourceNotFound</Code></Error>` +
			`<RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ErrorResponse>`,
	})
}

// The envelope follows the service's QueryErrorEnvelope, not its id.
func TestEncodeErrorFollowsQueryErrorEnvelope(t *testing.T) {
	t.Parallel()
	svc := newService("example", "2026-10-14", "https://example.com/doc/", opSpec{name: "Ping"})
	svc.QueryErrorEnvelope = registry.QueryErrorEnvelope{Namespace: true, Detail: true}
	e := &awserr.Error{
		ID:      awserr.Identity{Shape: "com.example#Gone", QueryCode: "Gone", Status: 404, Fault: awserr.Sender, MessageKey: awserr.MessageLower},
		Message: "gone",
		Origin:  awserr.Modeled{},
	}
	checkResponse(t, awsquery.Codec{}.EncodeError(svc, requestID, e), golden{
		status: 404,
		body: `<ErrorResponse xmlns="https://example.com/doc/"><Error><Type>Sender</Type><Code>Gone</Code><Message>gone</Message><Detail/></Error>` +
			`<RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ErrorResponse>`,
	})
}

// A generic error keeps its Query spelling and extra headers, and a server
// fault is Receiver.
func TestGoldenGenericErrors(t *testing.T) {
	t.Parallel()
	svc := newService("sts", "2011-06-15", stsNamespace, opSpec{name: "AssumeRole"})
	e := awserr.Internalf(errors.New("a test bug"))
	e.Headers = []awserr.Header{{Name: "Retry-After", Value: "1"}}
	checkResponse(t, awsquery.Codec{}.EncodeError(svc, requestID, e), golden{
		status: 500,
		header: http.Header{"Retry-After": {"1"}},
		body: `<ErrorResponse><Error><Type>Receiver</Type><Code>InternalFailure</Code>` +
			`<Message>The request processing has failed because of an unknown error, exception or failure.</Message></Error>` +
			`<RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ErrorResponse>`,
	})
	unknown := &awserr.Error{ID: awserr.InvalidAction(), Message: "The action FooBar is not valid for this endpoint.", Origin: awserr.Generic{}}
	checkResponse(t, awsquery.Codec{}.EncodeError(nil, requestID, unknown), golden{
		status: 400,
		body: `<ErrorResponse><Error><Type>Sender</Type><Code>InvalidAction</Code><Message>The action FooBar is not valid for this endpoint.</Message></Error>` +
			`<RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ErrorResponse>`,
	})
}

// An extra field whose name cannot be an XML element does not lose the
// error: it is sent without its fields.
func TestErrorWithAnInvalidFieldNameIsStillSent(t *testing.T) {
	t.Parallel()
	e := &awserr.Error{ID: awserr.AccessDeniedException(), Message: "no", Fields: []awserr.Field{{Name: "not a name", Value: "x"}}, Origin: awserr.Generic{}}
	resp := awsquery.Codec{}.EncodeError(nil, requestID, e)
	want := `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDeniedException</Code><Message>no</Message></Error>` +
		`<RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId></ErrorResponse>`
	if resp.Status != 403 || string(resp.Body) != want {
		t.Errorf("EncodeError = %d %s", resp.Status, resp.Body)
	}
}
