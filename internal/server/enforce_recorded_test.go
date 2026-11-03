package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// sessionOf is a session of the role none, which holds no policy, assumed
// by u, whose policy allows only that.
func sessionOf(t *testing.T, endpoint string, u signer) signer {
	t.Helper()
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sts:AssumeRole"}]}`
	iamAsRoot(t, endpoint, url.Values{"Action": {"CreateRole"}, "RoleName": {"none"}, "AssumeRolePolicyDocument": {trust}})
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"*"}]}`)
	w := queryCall(t, u, endpoint, "sts", url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {"arn:aws:iam::000000000000:role/none"}, "RoleSessionName": {"enf"}})
	if w.status != http.StatusOK {
		t.Fatalf("AssumeRole = %d %s", w.status, w.body)
	}
	return signer{key: xmlElement(t, w.body, "AccessKeyId"), secret: xmlElement(t, w.body, "SecretAccessKey"), token: xmlElement(t, w.body, "SessionToken")}
}

// jsonMessage is an awsJson or restJson error body's message, under either
// spelling of its key.
func jsonMessage(w wire) string {
	var body map[string]any
	if json.Unmarshal([]byte(w.body), &body) != nil {
		return ""
	}
	for _, k := range []string{"message", "Message"} {
		if m, ok := body[k].(string); ok {
			return m
		}
	}
	return ""
}

// A session of a role with no policy is refused as the recordings of
// IAM-ENF-01 to IAM-ENF-12, IAM-ENF-33 and IAM-ENF-34 show (recorded
// 2026-11-03, testdata/parity/<service>/IAM-ENF): each service's status and
// code, and the message in the documented grammar with "on resource: <arn>"
// on an implicit denial too, the action in lower case for SQS over JSON and
// Query alike, the prefix "SNS" for SNS, the key ARN for kms:DescribeKey,
// and S3's quoted resource (iam-enforcement-surfaces.md 3.2, 4.1 to 4.3).
// The probes address resources the recordings did, in understudy's
// account and Region; a probe on a missing resource stands for one on an
// existing one where IAM-ENF-14 recorded the same denial for both.
func TestEnforceDenialsMatchTheRecordings(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	s := sessionOf(t, endpoint, u)
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"q"}`)
	key := createKey(t, endpoint)
	if w := s3Call(t, dummy, http.MethodPut, endpoint+"/enf-b", ""); w.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", w.status, w.body)
	}
	if w := s3Call(t, dummy, http.MethodPut, endpoint+"/enf-b/exists", "x"); w.status != http.StatusOK {
		t.Fatalf("PutObject = %d %s", w.status, w.body)
	}
	const (
		caller = "User: arn:aws:sts::000000000000:assumed-role/none/enf is not authorized to perform: "
		queue  = "arn:aws:sqs:us-east-1:000000000000:q"
	)
	implicit := func(action, resource string) string {
		return caller + action + " on resource: " + resource + " because no identity-based policy allows the " + action + " action"
	}
	xmlCode := func(w wire) string { return parseXMLError(t, w).Code }
	xmlText := func(w wire) string { return parseXMLError(t, w).Message }
	s3Text := func(w wire) string { return parseXMLError(t, w).S3Message }
	s3Code := func(w wire) string { return parseXMLError(t, w).S3Code }
	queueURL := endpoint + "/000000000000/q"
	probes := []struct {
		fixture string
		send    func() wire
		status  int
		code    func(wire) string
		want    string
		text    func(wire) string
		message string
	}{
		{
			"sqs/IAM-ENF/01-raw.json",
			func() wire {
				return jsonCall(t, s, endpoint, "sqs", "1.0", "AmazonSQS.GetQueueAttributes", `{"QueueUrl":"`+queueURL+`","AttributeNames":["All"]}`)
			},
			http.StatusForbidden, jsonType, "AccessDeniedException", jsonMessage, implicit("sqs:getqueueattributes", queue),
		},
		{
			"sqs/IAM-ENF/02-query.json",
			func() wire {
				return queryCall(t, s, endpoint, "sqs", url.Values{"Action": {"GetQueueAttributes"}, "Version": {"2012-11-05"}, "QueueUrl": {queueURL}})
			},
			http.StatusForbidden, xmlCode, "AccessDenied", xmlText, implicit("sqs:getqueueattributes", queue),
		},
		{
			"sns/IAM-ENF/14-sns.json",
			func() wire {
				return queryCall(t, s, endpoint, "sns", url.Values{"Action": {"GetTopicAttributes"}, "Version": {"2010-03-31"}, "TopicArn": {"arn:aws:sns:us-east-1:000000000000:missing"}})
			},
			http.StatusForbidden, xmlCode, "AuthorizationError", xmlText, implicit("SNS:GetTopicAttributes", "arn:aws:sns:us-east-1:000000000000:missing"),
		},
		{
			"dynamodb/IAM-ENF/14-ddb.json",
			func() wire {
				return jsonCall(t, s, endpoint, "dynamodb", "1.0", "DynamoDB_20120810.DescribeTable", `{"TableName":"missing"}`)
			},
			http.StatusBadRequest, jsonType, "AccessDeniedException", jsonMessage, implicit("dynamodb:DescribeTable", "arn:aws:dynamodb:us-east-1:000000000000:table/missing"),
		},
		{
			"kms/IAM-ENF/05-describe.json",
			func() wire {
				return jsonCall(t, s, endpoint, "kms", "1.1", "TrentService.DescribeKey", `{"KeyId":"`+key+`"}`)
			},
			http.StatusBadRequest, jsonType, "AccessDeniedException", jsonMessage, implicit("kms:DescribeKey", key),
		},
		{
			"logs/IAM-ENF/14-logs.json",
			func() wire {
				return jsonCall(t, s, endpoint, "logs", "1.1", "Logs_20140328.DescribeLogStreams", `{"logGroupName":"/g"}`)
			},
			http.StatusBadRequest, jsonType, "AccessDeniedException", jsonMessage, implicit("logs:DescribeLogStreams", "arn:aws:logs:us-east-1:000000000000:log-group:/g"),
		},
		{
			"events/IAM-ENF/14-events.json",
			func() wire {
				return jsonCall(t, s, endpoint, "events", "1.1", "AWSEvents.DescribeRule", `{"Name":"r"}`)
			},
			http.StatusBadRequest, jsonType, "AccessDeniedException", jsonMessage, implicit("events:DescribeRule", "arn:aws:events:us-east-1:000000000000:rule/r"),
		},
		{
			"lambda/IAM-ENF/14-lambda.json",
			func() wire { return restGet(t, s, "lambda", endpoint+"/2015-03-31/functions/missing") },
			http.StatusForbidden, jsonType, "AccessDeniedException", jsonMessage, implicit("lambda:GetFunction", "arn:aws:lambda:us-east-1:000000000000:function:missing"),
		},
		{
			"s3/IAM-ENF/12-raw.json",
			func() wire { return restGet(t, s, "s3", endpoint+"/enf-b/exists") },
			http.StatusForbidden, s3Code, "AccessDenied", s3Text,
			caller + `s3:GetObject on resource: "arn:aws:s3:::enf-b/exists" because no identity-based policy allows the s3:GetObject action`,
		},
		{
			"apigateway/IAM-ENF/33-raw.json",
			func() wire { return restGet(t, s, "apigateway", endpoint+"/restapis") },
			http.StatusForbidden, jsonType, "AccessDeniedException", jsonMessage, implicit("apigateway:GET", "arn:aws:apigateway:us-east-1::/restapis"),
		},
		{
			"apigatewayv2/IAM-ENF/34-raw.json",
			func() wire { return restGet(t, s, "apigateway", endpoint+"/v2/apis") },
			http.StatusForbidden, jsonType, "AccessDeniedException", jsonMessage, implicit("apigateway:GET", "arn:aws:apigateway:us-east-1::/apis"),
		},
	}
	for _, p := range probes {
		t.Run(p.fixture, func(t *testing.T) {
			w := p.send()
			if w.status != p.status || p.code(w) != p.want {
				t.Errorf("denial = %d %q (%s); want the recorded %d %q", w.status, p.code(w), w.body, p.status, p.want)
			}
			if got := p.text(w); got != p.message {
				t.Errorf("message =\n%q\nwant the recorded\n%q", got, p.message)
			}
		})
	}
}
