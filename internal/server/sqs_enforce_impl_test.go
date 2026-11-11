package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// kmsSuffixRequestID matches the request id in a KMS client suffix.
var kmsSuffixRequestID = regexp.MustCompile(`Request ID: [0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// A send to an SSE-KMS queue by a caller allowed sqs:SendMessage but not
// kms:GenerateDataKey on the queue's key answers KmsAccessDenied: 400,
// x-amzn-query-error KMS.AccessDeniedException;Sender, and KMS's denial
// followed by the KMS client's SDK v1 suffix (IAM-ENF-29 29-raw, recorded
// 2026-11-03; switch sqs.sse-kms.denied-text). SendMessageBatch is refused
// whole with the same answer (inferred). Once the role may use the key,
// both send.
func TestEnforceSendToKMSQueueNeedsGenerateDataKey(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	keyARN := createKey(t, endpoint)
	keyID := keyARN[strings.LastIndex(keyARN, "/")+1:]
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"enf-kq","Attributes":{"KmsMasterKeyId":"`+keyID+`"}}`)
	const queueARN = "arn:aws:sqs:us-east-1:000000000000:enf-kq"
	queueURL := endpoint + "/000000000000/enf-kq"

	iamAsRoot(t, endpoint, url.Values{"Action": {"CreateRole"}, "RoleName": {"enf-data"}, "AssumeRolePolicyDocument": {
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sts:AssumeRole"}]}`,
	}})
	sqsOnly := `{"Effect":"Allow","Action":["sqs:SendMessage","sqs:ReceiveMessage"],"Resource":"` + queueARN + `"}`
	iamAsRoot(t, endpoint, url.Values{"Action": {"PutRolePolicy"}, "RoleName": {"enf-data"}, "PolicyName": {"data"}, "PolicyDocument": {
		`{"Version":"2012-10-17","Statement":[` + sqsOnly + `]}`,
	}})
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"*"}]}`)
	w := queryCall(t, u, endpoint, "sts", url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {"arn:aws:iam::000000000000:role/enf-data"}, "RoleSessionName": {"enf"}})
	if w.status != http.StatusOK {
		t.Fatalf("AssumeRole = %d %s", w.status, w.body)
	}
	s := signer{key: xmlElement(t, w.body, "AccessKeyId"), secret: xmlElement(t, w.body, "SecretAccessKey"), token: xmlElement(t, w.body, "SessionToken")}

	const want = "User: arn:aws:sts::000000000000:assumed-role/enf-data/enf is not authorized to perform: kms:GenerateDataKey on resource: "
	wantMessage := want + keyARN + " because no identity-based policy allows the kms:GenerateDataKey action" +
		" (Service: AWSKMS; Status Code: 400; Error Code: AccessDeniedException; Request ID: <uuid>; Proxy: null)"
	send := func() wire {
		return jsonCall(t, s, endpoint, "sqs", "1.0", "AmazonSQS.SendMessage", `{"QueueUrl":"`+queueURL+`","MessageBody":"x"}`)
	}
	batch := func() wire {
		return jsonCall(t, s, endpoint, "sqs", "1.0", "AmazonSQS.SendMessageBatch", `{"QueueUrl":"`+queueURL+`","Entries":[{"Id":"a","MessageBody":"x"}]}`)
	}
	for _, tc := range []struct {
		name string
		call func() wire
	}{{"SendMessage", send}, {"SendMessageBatch", batch}} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.call()
			var body struct {
				Type    string `json:"__type"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(w.body), &body); err != nil {
				t.Fatalf("body %q: %v", w.body, err)
			}
			if w.status != http.StatusBadRequest || body.Type != "com.amazonaws.sqs#KmsAccessDenied" {
				t.Errorf("denial = %d %q (%s); want the recorded 400 com.amazonaws.sqs#KmsAccessDenied", w.status, body.Type, w.body)
			}
			if got := w.header.Get("X-Amzn-Query-Error"); got != "KMS.AccessDeniedException;Sender" {
				t.Errorf("x-amzn-query-error = %q; want the recorded KMS.AccessDeniedException;Sender", got)
			}
			if got := kmsSuffixRequestID.ReplaceAllString(body.Message, "Request ID: <uuid>"); got != wantMessage {
				t.Errorf("message =\n%q\nwant the recorded\n%q", got, wantMessage)
			}
		})
	}

	iamAsRoot(t, endpoint, url.Values{"Action": {"PutRolePolicy"}, "RoleName": {"enf-data"}, "PolicyName": {"data"}, "PolicyDocument": {
		`{"Version":"2012-10-17","Statement":[` + sqsOnly + `,{"Effect":"Allow","Action":"kms:GenerateDataKey","Resource":"` + keyARN + `"}]}`,
	}})
	if w := send(); w.status != http.StatusOK {
		t.Errorf("SendMessage allowed kms:GenerateDataKey = %d %s, want 200", w.status, w.body)
	}
	if w := batch(); w.status != http.StatusOK || strings.Contains(w.body, "Failed") {
		t.Errorf("SendMessageBatch allowed kms:GenerateDataKey = %d %s, want 200 with no failed entry", w.status, w.body)
	}
}

// A send to a queue whose key KMS does not hold answers KmsNotFound with
// KMS's text (recorded by IAM-ENF-14 for DescribeKey) and the KMS
// client's suffix, whatever iam.enforce says (switch sqs.sse-kms.key-error,
// inferred in the frame IAM-ENF-29 recorded for the denial).
func TestSendToQueueWithMissingKeyAnswersKmsNotFound(t *testing.T) {
	_, endpoint := adminServer(t, server.Config{})
	const missing = "00000000-0000-0000-0000-000000000000"
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"kq","Attributes":{"KmsMasterKeyId":"`+missing+`"}}`)
	status, _, out := sqsCall(t, endpoint, "SendMessage", `{"QueueUrl":"`+endpoint+`/000000000000/kq","MessageBody":"x"}`)
	var body struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("body %q: %v", out, err)
	}
	want := "Key 'arn:aws:kms:us-east-1:000000000000:key/" + missing + "' does not exist" +
		" (Service: AWSKMS; Status Code: 400; Error Code: NotFoundException; Request ID: <uuid>; Proxy: null)"
	if status != http.StatusBadRequest || body.Type != "com.amazonaws.sqs#KmsNotFound" {
		t.Errorf("SendMessage = %d %q (%s), want 400 com.amazonaws.sqs#KmsNotFound", status, body.Type, out)
	}
	if got := kmsSuffixRequestID.ReplaceAllString(body.Message, "Request ID: <uuid>"); got != want {
		t.Errorf("message =\n%q\nwant\n%q", got, want)
	}
}
