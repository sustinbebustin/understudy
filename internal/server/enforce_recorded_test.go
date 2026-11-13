package server_test

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
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

// jsonMessageAt reads an awsJson or restJson error body's message under
// exactly key, the spelling the recording carries: a body that spells it
// the other way reads as no message (iam-enforcement-surfaces.md 12.3).
func jsonMessageAt(key string) func(wire) string {
	return func(w wire) string {
		var body map[string]any
		if json.Unmarshal([]byte(w.body), &body) != nil {
			return ""
		}
		if m, ok := body[key].(string); ok {
			return m
		}
		return ""
	}
}

// A session of a role with no policy is refused as the recordings of
// IAM-ENF-01 to IAM-ENF-12, IAM-ENF-33 and IAM-ENF-34 show (recorded
// 2026-11-03, testdata/parity/<service>/IAM-ENF): each service's status and
// code, and the message in the documented grammar with "on resource: <arn>"
// on an implicit denial too, the action in lower case for SQS over JSON and
// Query alike, the prefix "SNS" for SNS, the key ARN for kms:DescribeKey,
// and S3's quoted resource (iam-enforcement-surfaces.md 3.2, 4.1 to 4.3).
// A list operation names its service's wildcard resource, or none for
// Kinesis and SES v1 (12.3, IAM-ENF-35 to IAM-ENF-41). The probes address resources the recordings did, in understudy's
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
	if w := jsonCall(t, dummy, endpoint, "secretsmanager", "1.1", "secretsmanager.CreateSecret", `{"Name":"enf-s","SecretString":"x","ClientRequestToken":"00000000-0000-0000-0000-000000000001"}`); w.status != http.StatusOK {
		t.Fatalf("CreateSecret = %d %s", w.status, w.body)
	}
	const (
		caller = "User: arn:aws:sts::000000000000:assumed-role/none/enf is not authorized to perform: "
		queue  = "arn:aws:sqs:us-east-1:000000000000:q"
	)
	implicit := func(action, resource string) string {
		return caller + action + " on resource: " + resource + " because no identity-based policy allows the " + action + " action"
	}
	// ses28Sender is SES-28's sender; a send's denial names its identity
	// in SES's own grammar.
	const ses28Sender = "us1103-me@example.com"
	ses28Denial := func(action string) string {
		return "User 'arn:aws:sts::000000000000:assumed-role/none/enf' is not authorized to perform '" + action +
			"' on resource 'arn:aws:ses:us-east-1:000000000000:identity/" + ses28Sender + "'"
	}
	xmlCode := func(w wire) string { return parseXMLError(t, w).Code }
	xmlText := func(w wire) string { return parseXMLError(t, w).Message }
	s3Text := func(w wire) string { return parseXMLError(t, w).S3Message }
	s3Code := func(w wire) string { return parseXMLError(t, w).S3Code }
	queueURL := endpoint + "/000000000000/q"
	// Every recorded JSON denial but API Gateway v2's names its message
	// "Message".
	upper, lower := jsonMessageAt("Message"), jsonMessageAt("message")
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
			http.StatusForbidden, jsonType, "AccessDeniedException", upper, implicit("sqs:getqueueattributes", queue),
		},
		{
			"sqs/IAM-ENF/02-query.json",
			func() wire {
				return queryCall(t, s, endpoint, "sqs", url.Values{"Action": {"GetQueueAttributes"}, "Version": {"2012-11-05"}, "QueueUrl": {queueURL}})
			},
			http.StatusForbidden, xmlCode, "AccessDenied", xmlText, implicit("sqs:getqueueattributes", queue),
		},
		{
			// A missing queue answers before authorization; the JSON
			// protocol's key is "message" here (IAM-ENF-14).
			"sqs/IAM-ENF/14-sqs.json",
			func() wire {
				return jsonCall(t, s, endpoint, "sqs", "1.0", "AmazonSQS.GetQueueAttributes", `{"QueueUrl":"`+endpoint+`/000000000000/enf-missing","AttributeNames":["All"]}`)
			},
			http.StatusBadRequest, jsonType, "QueueDoesNotExist", lower, "The specified queue does not exist.",
		},
		{
			"s3/IAM-ENF/14-s3-bucket.json",
			func() wire { return restGet(t, s, "s3", endpoint+"/enf-missing?list-type=2&encoding-type=url") },
			http.StatusNotFound, s3Code, "NoSuchBucket", s3Text, "The specified bucket does not exist",
		},
		{
			// A missing key asks s3:ListBucket about the bucket before
			// s3:GetObject about the key (iam-enforcement-surfaces.md 5.1).
			"s3/IAM-ENF/14-s3-key.json",
			func() wire { return restGet(t, s, "s3", endpoint+"/enf-b/missing") },
			http.StatusForbidden, s3Code, "AccessDenied", s3Text,
			caller + `s3:ListBucket on resource: "arn:aws:s3:::enf-b" because no identity-based policy allows the s3:ListBucket action`,
		},
		{
			"kms/IAM-ENF/14-kms.json",
			func() wire {
				return jsonCall(t, s, endpoint, "kms", "1.1", "TrentService.DescribeKey", `{"KeyId":"00000000-0000-0000-0000-000000000000"}`)
			},
			http.StatusBadRequest, jsonType, "NotFoundException", lower,
			"Key 'arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000' does not exist",
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
			http.StatusBadRequest, jsonType, "AccessDeniedException", upper, implicit("dynamodb:DescribeTable", "arn:aws:dynamodb:us-east-1:000000000000:table/missing"),
		},
		{
			// IAM and Secrets Manager deny a missing resource first, naming
			// it as the request gave it (iam-enforcement-surfaces.md 5.3).
			"iam/IAM-ENF/14-iam.json",
			func() wire {
				return queryCall(t, s, endpoint, "iam", url.Values{"Action": {"GetRole"}, "Version": {"2010-05-08"}, "RoleName": {"enf-missing"}})
			},
			http.StatusForbidden, xmlCode, "AccessDenied", xmlText, implicit("iam:GetRole", "role enf-missing"),
		},
		{
			"iam/IAM-ENF/10-get-role.json",
			func() wire {
				return queryCall(t, s, endpoint, "iam", url.Values{"Action": {"GetRole"}, "Version": {"2010-05-08"}, "RoleName": {"none"}})
			},
			http.StatusForbidden, xmlCode, "AccessDenied", xmlText, implicit("iam:GetRole", "role none"),
		},
		{
			"secretsmanager/IAM-ENF/14-sm.json",
			func() wire {
				return jsonCall(t, s, endpoint, "secretsmanager", "1.1", "secretsmanager.DescribeSecret", `{"SecretId":"enf-missing"}`)
			},
			http.StatusBadRequest, jsonType, "AccessDeniedException", upper, implicit("secretsmanager:DescribeSecret", "enf-missing"),
		},
		{
			// An existing secret is named as the SecretId gave it too, not
			// by its ARN.
			"secretsmanager/IAM-ENF/06-raw.json",
			func() wire {
				return jsonCall(t, s, endpoint, "secretsmanager", "1.1", "secretsmanager.DescribeSecret", `{"SecretId":"enf-s"}`)
			},
			http.StatusBadRequest, jsonType, "AccessDeniedException", upper, implicit("secretsmanager:DescribeSecret", "enf-s"),
		},
		{
			"kms/IAM-ENF/05-describe.json",
			func() wire {
				return jsonCall(t, s, endpoint, "kms", "1.1", "TrentService.DescribeKey", `{"KeyId":"`+key+`"}`)
			},
			http.StatusBadRequest, jsonType, "AccessDeniedException", upper, implicit("kms:DescribeKey", key),
		},
		{
			"logs/IAM-ENF/14-logs.json",
			func() wire {
				return jsonCall(t, s, endpoint, "logs", "1.1", "Logs_20140328.DescribeLogStreams", `{"logGroupName":"/g"}`)
			},
			http.StatusBadRequest, jsonType, "AccessDeniedException", upper, implicit("logs:DescribeLogStreams", "arn:aws:logs:us-east-1:000000000000:log-group:/g"),
		},
		{
			"events/IAM-ENF/14-events.json",
			func() wire {
				return jsonCall(t, s, endpoint, "events", "1.1", "AWSEvents.DescribeRule", `{"Name":"r"}`)
			},
			http.StatusBadRequest, jsonType, "AccessDeniedException", upper, implicit("events:DescribeRule", "arn:aws:events:us-east-1:000000000000:rule/r"),
		},
		{
			"lambda/IAM-ENF/14-lambda.json",
			func() wire { return restGet(t, s, "lambda", endpoint+"/2015-03-31/functions/missing") },
			http.StatusForbidden, jsonType, "AccessDeniedException", upper, implicit("lambda:GetFunction", "arn:aws:lambda:us-east-1:000000000000:function:missing"),
		},
		{
			"s3/IAM-ENF/12-raw.json",
			func() wire { return restGet(t, s, "s3", endpoint+"/enf-b/exists") },
			http.StatusForbidden, s3Code, "AccessDenied", s3Text,
			caller + `s3:GetObject on resource: "arn:aws:s3:::enf-b/exists" because no identity-based policy allows the s3:GetObject action`,
		},
		{
			// The SDK form (Accept: application/json); curl's Accept: */* got
			// vnd.error+json with "message" (33-raw.json, wontfix).
			"apigateway/IAM-ENF/33-apigateway.json",
			func() wire { return restGet(t, s, "apigateway", endpoint+"/restapis") },
			http.StatusForbidden, jsonType, "AccessDeniedException", upper, implicit("apigateway:GET", "arn:aws:apigateway:us-east-1::/restapis"),
		},
		{
			"apigatewayv2/IAM-ENF/34-apigatewayv2.json",
			func() wire { return restGet(t, s, "apigateway", endpoint+"/v2/apis") },
			http.StatusForbidden, jsonType, "AccessDeniedException", lower, implicit("apigateway:GET", "arn:aws:apigateway:us-east-1::/apis"),
		},
		{
			"cloudwatch/IAM-ENF/09-json-qm.json",
			func() wire {
				req := newRequest(t, http.MethodPost, endpoint+"/", "{}")
				req.Header.Set("Content-Type", "application/x-amz-json-1.0")
				req.Header.Set("X-Amz-Target", "GraniteServiceVersion20100801.DescribeAlarms")
				req.Header.Set("X-Amzn-Query-Mode", "true")
				return signedCall(t, s, "monitoring", req, "{}")
			},
			http.StatusForbidden, jsonType, "AccessDeniedException", upper, implicit("cloudwatch:DescribeAlarms", "arn:aws:cloudwatch:us-east-1:000000000000:alarm:*"),
		},
		{
			"cloudwatch/IAM-ENF/09-json.json",
			func() wire {
				req := newRequest(t, http.MethodPost, endpoint+"/", "{}")
				req.Header.Set("Content-Type", "application/x-amz-json-1.0")
				req.Header.Set("X-Amz-Target", "GraniteServiceVersion20100801.DescribeAlarms")
				return signedCall(t, s, "monitoring", req, "{}")
			},
			http.StatusBadRequest, jsonType, "AccessDeniedException", upper, implicit("cloudwatch:DescribeAlarms", "arn:aws:cloudwatch:us-east-1:000000000000:alarm:*"),
		},
		{
			"cloudwatch/IAM-ENF/09-query.json",
			func() wire {
				return queryCall(t, s, endpoint, "monitoring", url.Values{"Action": {"DescribeAlarms"}, "Version": {"2010-08-01"}})
			},
			http.StatusForbidden, xmlCode, "AccessDenied", xmlText, implicit("cloudwatch:DescribeAlarms", "arn:aws:cloudwatch:us-east-1:000000000000:alarm:*"),
		},
		{
			"dynamodb/IAM-ENF/35-streams.json",
			func() wire { return streamsCall(t, s, endpoint, "ListStreams", `{}`) },
			http.StatusBadRequest, jsonType, "AccessDeniedException", upper, implicit("dynamodb:ListStreams", "arn:aws:dynamodb:us-east-1:000000000000:table/*/stream/*"),
		},
		{
			"scheduler/IAM-ENF/37-raw.json",
			func() wire { return restGet(t, s, "scheduler", endpoint+"/schedules") },
			http.StatusForbidden, jsonType, "AccessDeniedException", upper, implicit("scheduler:ListSchedules", "arn:aws:scheduler:us-east-1:000000000000:schedule/*/*"),
		},
		{
			"scheduler/SCH/10-01-denied-get.json",
			func() wire { return restGet(t, s, "scheduler", endpoint+"/schedules/none") },
			http.StatusForbidden, jsonType, "AccessDeniedException", upper, implicit("scheduler:GetSchedule", "arn:aws:scheduler:us-east-1:000000000000:schedule/default/none"),
		},
		{
			"ses/IAM-ENF/39-sesv2.json",
			func() wire {
				req := newRequest(t, http.MethodPost, endpoint+"/v2/email/list-identities", "{}")
				req.Header.Set("Content-Type", "application/json")
				return signedCall(t, s, "ses", req, "{}")
			},
			http.StatusForbidden, jsonType, "AccessDeniedException", upper, implicit("ses:ListEmailIdentities", "*"),
		},
		{
			"ses/IAM-ENF/38-raw.json",
			func() wire {
				return queryCall(t, s, endpoint, "ses", url.Values{"Action": {"ListIdentities"}, "Version": {"2010-12-01"}})
			},
			http.StatusForbidden, xmlCode, "AccessDenied", xmlText,
			caller + "ses:ListIdentities because no identity-based policy allows the ses:ListIdentities action",
		},
		{
			// A send's denial is SES's own text, naming the sender's
			// identity (SES-28); steps 01 (the CLI) and 02 (curl) sent
			// the same v1 SendEmail.
			"ses/SES-28/01-v1-send.json",
			func() wire {
				return queryCall(t, s, endpoint, "ses", url.Values{
					"Action": {"SendEmail"}, "Version": {"2010-12-01"}, "Source": {ses28Sender},
					"Destination.ToAddresses.member.1": {sesSimulatorTo}, "Message.Subject.Data": {"s"}, "Message.Body.Text.Data": {"b"},
				})
			},
			http.StatusForbidden, xmlCode, "AccessDenied", xmlText, ses28Denial("ses:SendEmail"),
		},
		{
			"ses/SES-28/02-v1-raw.json",
			func() wire {
				return queryCall(t, s, endpoint, "ses", url.Values{
					"Action": {"SendEmail"}, "Version": {"2010-12-01"}, "Source": {ses28Sender},
					"Destination.ToAddresses.member.1": {sesSimulatorTo}, "Message.Subject.Data": {"s"}, "Message.Body.Text.Data": {"b"},
				})
			},
			http.StatusForbidden, xmlCode, "AccessDenied", xmlText, ses28Denial("ses:SendEmail"),
		},
		{
			"ses/SES-28/03-v2-simple.json",
			func() wire {
				return sesV2Send(t, s, endpoint, `{"FromEmailAddress":"`+ses28Sender+`","Destination":{"ToAddresses":["`+sesSimulatorTo+`"]},`+
					`"Content":{"Simple":{"Subject":{"Data":"s"},"Body":{"Text":{"Data":"b"}}}}}`)
			},
			http.StatusForbidden, jsonType, "AccessDeniedException", upper, ses28Denial("ses:SendEmail"),
		},
		{
			// Raw content is authorized as ses:SendRawEmail on the
			// identity of the raw From header.
			"ses/SES-28/04-v2-raw.json",
			func() wire {
				raw := base64.StdEncoding.EncodeToString([]byte("From: " + ses28Sender + "\nTo: " + sesSimulatorTo + "\nSubject: r1\n\nb\n"))
				return sesV2Send(t, s, endpoint, `{"Content":{"Raw":{"Data":"`+raw+`"}}}`)
			},
			http.StatusForbidden, jsonType, "AccessDeniedException", upper, ses28Denial("ses:SendRawEmail"),
		},
		{
			"sfn/IAM-ENF/40-raw.json",
			func() wire { return sfnCall(t, s, endpoint, "ListStateMachines", `{}`) },
			http.StatusBadRequest, jsonType, "AccessDeniedException", upper, implicit("states:ListStateMachines", "arn:aws:states:us-east-1:000000000000:stateMachine:*"),
		},
		{
			"ssm/IAM-ENF/41-raw.json",
			func() wire { return ssmCall(t, s, endpoint, "DescribeParameters", `{}`) },
			http.StatusBadRequest, jsonType, "AccessDeniedException", upper, implicit("ssm:DescribeParameters", "arn:aws:ssm:us-east-1:000000000000:*"),
		},
	}
	// The recorded x-amzn-query-error of CloudWatch's JSON denials: sent only
	// under x-amzn-query-mode (iam-enforcement-surfaces.md 12.3).
	queryErrors := map[string]string{
		"cloudwatch/IAM-ENF/09-json.json":    "",
		"cloudwatch/IAM-ENF/09-json-qm.json": "AccessDenied;Sender",
	}
	for _, p := range probes {
		t.Run(p.fixture, func(t *testing.T) {
			w := p.send()
			if w.status != p.status || p.code(w) != p.want {
				t.Errorf("denial = %d %q (%s); want the recorded %d %q", w.status, p.code(w), w.body, p.status, p.want)
			}
			if want, ok := queryErrors[p.fixture]; ok {
				if got := strings.Join(w.header.Values("X-Amzn-Query-Error"), ", "); got != want {
					t.Errorf("x-amzn-query-error = %q; want the recorded %q", got, want)
				}
			}
			if got := p.text(w); got != p.message {
				t.Errorf("message =\n%q\nwant the recorded\n%q", got, p.message)
			}
		})
	}
}

// IAM and Secrets Manager deny a missing resource first (IAM-ENF-14), but a
// caller allowed the action still learns that it is missing: a missing
// role, user or instance profile is asked about on its ARN at the path
// "/", and a missing secret on the ARN its SecretId implies, so a grant on
// "*", on role/* or on secret:* covers it (inferred: which string AWS
// evaluates is unrecorded; iam-enforcement-surfaces.md 5.3).
func TestMissingResourceDeniedFirstStillNotFoundWhenAllowed(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":"iam:GetRole","Resource":"*"},`+
		`{"Effect":"Allow","Action":"iam:DeleteRole","Resource":"arn:aws:iam::000000000000:role/*"},`+
		`{"Effect":"Allow","Action":"iam:GetInstanceProfile","Resource":"arn:aws:iam::000000000000:instance-profile/*"},`+
		`{"Effect":"Allow","Action":"secretsmanager:DescribeSecret","Resource":"arn:aws:secretsmanager:us-east-1:000000000000:secret:*"}]}`)
	iamCode := func(form url.Values) wire {
		form.Set("Version", "2010-05-08")
		return queryCall(t, u, endpoint, "iam", form)
	}
	for _, form := range []url.Values{
		{"Action": {"GetRole"}, "RoleName": {"enf-missing"}},
		{"Action": {"DeleteRole"}, "RoleName": {"enf-missing"}},
		{"Action": {"GetInstanceProfile"}, "InstanceProfileName": {"enf-missing"}},
	} {
		action := form.Get("Action")
		if w := iamCode(form); w.status != http.StatusNotFound || parseXMLError(t, w).Code != "NoSuchEntity" {
			t.Errorf("allowed %s of a missing entity = %d %s, want NoSuchEntity", action, w.status, w.body)
		}
	}
	for _, id := range []string{"enf-missing", "arn:aws:secretsmanager:us-east-1:000000000000:secret:enf-missing-AbCdEf"} {
		w := jsonCall(t, u, endpoint, "secretsmanager", "1.1", "secretsmanager.DescribeSecret", `{"SecretId":"`+id+`"}`)
		if jsonType(w) != "ResourceNotFoundException" {
			t.Errorf("allowed DescribeSecret of missing %s = %d %s, want ResourceNotFoundException", id, w.status, w.body)
		}
	}
	// Denied, a missing instance profile is named as a role is
	// (inferred from IAM-ENF-14).
	w := iamCode(url.Values{"Action": {"DeleteInstanceProfile"}, "InstanceProfileName": {"enf-missing"}})
	want := "User: arn:aws:iam::000000000000:user/u is not authorized to perform: iam:DeleteInstanceProfile on resource: instance-profile enf-missing because no identity-based policy allows the iam:DeleteInstanceProfile action"
	if w.status != http.StatusForbidden || parseXMLError(t, w).Message != want {
		t.Errorf("denied DeleteInstanceProfile of a missing profile = %d %s, want %q", w.status, w.body, want)
	}
}

// The wildcard a list operation's denial names is text only: a policy
// allowing the action on "*" lets the call past authorization, and the
// access record's policy grants it on "*" (iam-enforcement-surfaces.md
// 12.3; whether AWS evaluates the wildcard is unrecorded, so understudy
// keeps "*"). DynamoDB Streams answers 501 without Docker, after
// authorization, so a call passes when it is not a denial.
func TestListDenialWildcardLeavesEvaluationOnAnyResource(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	calls := map[string]func() wire{
		"cloudwatch:DescribeAlarms": func() wire {
			req := newRequest(t, http.MethodPost, endpoint+"/", "{}")
			req.Header.Set("Content-Type", "application/x-amz-json-1.0")
			req.Header.Set("X-Amz-Target", "GraniteServiceVersion20100801.DescribeAlarms")
			req.Header.Set("X-Amzn-Query-Mode", "true")
			return signedCall(t, u, "monitoring", req, "{}")
		},
		"dynamodb:ListStreams":     func() wire { return streamsCall(t, u, endpoint, "ListStreams", `{}`) },
		"scheduler:ListSchedules":  func() wire { return restGet(t, u, "scheduler", endpoint+"/schedules") },
		"states:ListStateMachines": func() wire { return sfnCall(t, u, endpoint, "ListStateMachines", `{}`) },
		"ssm:DescribeParameters":   func() wire { return ssmCall(t, u, endpoint, "DescribeParameters", `{}`) },
		"ses:ListEmailIdentities": func() wire {
			req := newRequest(t, http.MethodPost, endpoint+"/v2/email/list-identities", "{}")
			req.Header.Set("Content-Type", "application/json")
			return signedCall(t, u, "ses", req, "{}")
		},
	}
	actions := slices.Sorted(maps.Keys(calls))
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["`+strings.Join(actions, `","`)+`"],"Resource":"*"}]}`)
	for _, action := range actions {
		if w := calls[action](); strings.Contains(w.body, "AccessDenied") {
			t.Errorf("%s allowed on * = %d %s, want it past authorization", action, w.status, w.body)
		}
	}
	if got := granted(t, c, userU)["*"]; !slices.Equal(got, actions) {
		t.Errorf("generated policy grants on * %q, want %q", got, actions)
	}
}
