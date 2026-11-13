package awserr

import (
	"fmt"
	"strings"
)

// SDKClient is the AWS SDK for Java client through which one AWS service
// calls another. A service that passes on the callee's error message passes
// it with the suffix that client appends, which names the client's service,
// the callee's status and the request id of the call: Parameter Store for
// KMS (ssm-parameters.md 5.5), Step Functions for Lambda
// (stepfunctions-api.md 9.4), EventBridge for KMS (eventbridge-buses.md)
// and for a refusing Lambda or SQS target (iam-enforcement-surfaces.md
// 8.4). Build one with SDKv1, SDKv1Query or SDKv2.
type SDKClient struct {
	generation sdkGeneration
	service    string
	code       sdkCode
}

// sdkCode is which of the callee's error codes a v1 client's suffix names.
type sdkCode uint8

const (
	// wireCodeName: the code a JSON protocol sends (wireCode).
	wireCodeName sdkCode = iota
	// queryCodeName: the callee's awsQuery code (Identity.QueryCode).
	queryCodeName
)

// sdkGeneration is the major version of the AWS SDK for Java a client is
// built from; each writes its own suffix.
type sdkGeneration uint8

const (
	sdkV1 sdkGeneration = iota + 1
	sdkV2
)

// SDKv1 is an AWS SDK for Java v1 client of service, named as the client
// names it (AWSKMS, AWSLambda). Its suffix is
// "(Service: <service>; Status Code: <status>; Error Code: <code>; Request
// ID: <id>; Proxy: null)".
func SDKv1(service string) SDKClient {
	return SDKClient{generation: sdkV1, service: service, code: wireCodeName}
}

// SDKv1Query is SDKv1 for a callee whose v1 client reports its awsQuery
// error code: EventBridge's AmazonSQS client names SQS's denial
// AccessDenied, not AccessDeniedException (iam-enforcement-surfaces.md
// 8.4, aws-recorded).
func SDKv1Query(service string) SDKClient {
	return SDKClient{generation: sdkV1, service: service, code: queryCodeName}
}

// SDKv2 is an AWS SDK for Java v2 client of service, named as the client
// names it (Kms, Lambda). Its suffix is "(Service: <service>, Status Code:
// <status>, Request ID: <id>) (SDK Attempt Count: 1)": the attempt count is
// 1 in every recording.
func SDKv2(service string) SDKClient { return SDKClient{generation: sdkV2, service: service} }

// Message is e's message as the caller reports it through c: the message,
// a space, and c's suffix for e's status, e's error code and requestID, the
// id of the call that failed. The error code is the one a JSON protocol
// sends, the __type without a namespace, or the awsQuery code for an
// SDKv1Query client; a zero c appends nothing.
func (c SDKClient) Message(e *Error, requestID string) string {
	switch c.generation {
	case sdkV1:
		code := wireCode(e.ID)
		if c.code == queryCodeName {
			code = e.ID.QueryCode
		}
		return fmt.Sprintf("%s (Service: %s; Status Code: %d; Error Code: %s; Request ID: %s; Proxy: null)",
			e.Message, c.service, e.ID.Status, code, requestID)
	case sdkV2:
		return fmt.Sprintf("%s (Service: %s, Status Code: %d, Request ID: %s) (SDK Attempt Count: 1)",
			e.Message, c.service, e.ID.Status, requestID)
	}
	return e.Message
}

// String is c's suffix with placeholders for what the error and the call
// fill in, so a fidelity switch holding a client reads in the coverage
// report.
func (c SDKClient) String() string {
	switch c.generation {
	case sdkV1:
		return "(Service: " + c.service + "; Status Code: <status>; Error Code: <code>; Request ID: <id>; Proxy: null)"
	case sdkV2:
		return "(Service: " + c.service + ", Status Code: <status>, Request ID: <id>) (SDK Attempt Count: 1)"
	}
	return "(no SDK client)"
}

// wireCode is id's error code on a JSON protocol: its __type, without the
// namespace a generic error's carries.
func wireCode(id Identity) string {
	if _, name, ok := strings.Cut(id.JSONType, "#"); ok {
		return name
	}
	return id.JSONType
}
