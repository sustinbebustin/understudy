package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/server"
)

// The access record and step 8 pair each action with the resources it
// applies to (iam-enforcement-surfaces.md 12.6) beyond the blind tests:
// a copy's and a missing key's S3 actions, ReEncrypt's two keys, a move
// task's queues, the role AddRoleToInstanceProfile passes, and the
// secondary actions a create uses only when it sends tags or a role.

// userUses lists the recorded uses of the user u as "<action> on
// <resource>".
func userUses(t *testing.T, c *admin.Client) []string {
	t.Helper()
	got, err := c.IAMUsage(t.Context(), admin.UsageQuery{Principal: userU})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(got))
	for i := range got {
		out = append(out, got[i].Action+" on "+got[i].Resource)
	}
	return out
}

// wantUses checks uses holds every one of want and none of absent.
func wantUses(t *testing.T, uses, want, absent []string) {
	t.Helper()
	for _, w := range want {
		if !slices.Contains(uses, w) {
			t.Errorf("uses = %q, missing %q", uses, w)
		}
	}
	for _, a := range absent {
		if slices.Contains(uses, a) {
			t.Errorf("uses = %q, hold %q, which the call does not need", uses, a)
		}
	}
}

// A copy records s3:GetObject on its source's objects and s3:PutObject on
// its destination's alone; GetObject of a missing key records s3:ListBucket
// on the bucket and keeps s3:GetObject on the bucket's objects, not on the
// bucket the ListBucket question touched (iam-enforcement-surfaces.md 5.1,
// 12.6).
func TestTheRecordPairsCopyAndMissingKeyReadsWithTheirResources(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceOff)
	for _, put := range []struct{ path, body string }{{"/src", ""}, {"/dst", ""}, {"/src/a.txt", "abc"}} {
		if got := s3Call(t, dummy, http.MethodPut, endpoint+put.path, put.body); got.status != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", put.path, got.status, got.body)
		}
	}
	req := newRequest(t, http.MethodPut, endpoint+"/dst/b.txt", "")
	req.Header.Set("X-Amz-Copy-Source", "/src/a.txt")
	if w := signedCall(t, u, "s3", req, ""); w.status != http.StatusOK {
		t.Fatalf("CopyObject = %d %s", w.status, w.body)
	}
	if w := restGet(t, u, "s3", endpoint+"/src/missing.txt"); w.status != http.StatusNotFound || parseXMLError(t, w).S3Code != "NoSuchKey" {
		t.Fatalf("GetObject of a missing key under off = %d %s, want 404 NoSuchKey", w.status, w.body)
	}
	wantUses(t, userUses(t, c),
		[]string{"s3:GetObject on arn:aws:s3:::src/*", "s3:PutObject on arn:aws:s3:::dst/*", "s3:ListBucket on arn:aws:s3:::src"},
		[]string{"s3:GetObject on arn:aws:s3:::dst/*", "s3:PutObject on arn:aws:s3:::src/*", "s3:GetObject on arn:aws:s3:::src"})
}

// HeadObject and GetObjectAttributes answer a missing key as GetObject
// does: 403 without s3:ListBucket on the bucket, 404 NoSuchKey with it
// (iam-enforcement-surfaces.md 5.1, 12.1).
func TestEnforceAnswersMissingKeysOfHeadAndAttributesByListBucket(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/docs", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	head := func() wire {
		return signedCall(t, u, "s3", newRequest(t, http.MethodHead, endpoint+"/docs/missing.txt", ""), "")
	}
	attributes := func() wire {
		req := newRequest(t, http.MethodGet, endpoint+"/docs/missing.txt?attributes", "")
		req.Header.Set("X-Amz-Object-Attributes", "ETag")
		return signedCall(t, u, "s3", req, "")
	}
	reads := `{"Effect":"Allow","Action":["s3:GetObject","s3:GetObjectAttributes"],"Resource":"arn:aws:s3:::docs/*"}`
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+reads+`]}`)
	if w := head(); w.status != http.StatusForbidden {
		t.Errorf("HeadObject of a missing key without s3:ListBucket = %d, want 403", w.status)
	}
	if w := attributes(); w.status != http.StatusForbidden || parseXMLError(t, w).S3Code != "AccessDenied" {
		t.Errorf("GetObjectAttributes of a missing key without s3:ListBucket = %d %s, want 403 AccessDenied", w.status, w.body)
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+reads+`,{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::docs"}]}`)
	if w := head(); w.status != http.StatusNotFound {
		t.Errorf("HeadObject of a missing key with s3:ListBucket = %d, want 404", w.status)
	}
	if w := attributes(); w.status != http.StatusNotFound || parseXMLError(t, w).S3Code != "NoSuchKey" {
		t.Errorf("GetObjectAttributes of a missing key with s3:ListBucket = %d %s, want 404 NoSuchKey", w.status, w.body)
	}
}

// ReEncrypt needs kms:ReEncryptFrom on the key that sealed the ciphertext
// and kms:ReEncryptTo on the destination key, each on its own key: the
// record keeps exactly those, and a policy granting exactly those allows
// the call where one without ReEncryptTo does not
// (iam-enforcement-surfaces.md 10.2, 12.6).
func TestReEncryptPairsEachActionWithItsKey(t *testing.T) {
	for _, mode := range []server.IAMEnforce{server.IAMEnforceOff, server.IAMEnforceEnforce} {
		t.Run(string(mode), func(t *testing.T) {
			c, endpoint, u := enforced(t, mode)
			kms := func(s signer, target, body string) wire {
				return jsonCall(t, s, endpoint, "kms", "1.1", "TrentService."+target, body)
			}
			keyARN := func() string {
				var out struct{ KeyMetadata struct{ Arn string } }
				w := kms(dummy, "CreateKey", "{}")
				if w.status != http.StatusOK || json.Unmarshal([]byte(w.body), &out) != nil {
					t.Fatalf("CreateKey = %d %s", w.status, w.body)
				}
				return out.KeyMetadata.Arn
			}
			from, to := keyARN(), keyARN()
			var sealed struct{ CiphertextBlob string }
			if w := kms(dummy, "Encrypt", `{"KeyId":"`+from+`","Plaintext":"aGk="}`); w.status != http.StatusOK || json.Unmarshal([]byte(w.body), &sealed) != nil {
				t.Fatalf("Encrypt = %d %s", w.status, w.body)
			}
			reEncrypt := func() wire {
				return kms(u, "ReEncrypt", `{"CiphertextBlob":"`+sealed.CiphertextBlob+`","DestinationKeyId":"`+to+`"}`)
			}
			if mode == server.IAMEnforceEnforce {
				putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:ReEncryptFrom","Resource":["`+from+`","`+to+`"]}]}`)
				if w := reEncrypt(); w.status != kmsapi.DenialError().Status || jsonType(w) != "AccessDeniedException" {
					t.Errorf("ReEncrypt without kms:ReEncryptTo = %d %s, want the KMS denial", w.status, w.body)
				}
				putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+
					`{"Effect":"Allow","Action":"kms:ReEncryptFrom","Resource":"`+from+`"},`+
					`{"Effect":"Allow","Action":"kms:ReEncryptTo","Resource":"`+to+`"}]}`)
			}
			if w := reEncrypt(); w.status != http.StatusOK {
				t.Fatalf("ReEncrypt with ReEncryptFrom on the source and ReEncryptTo on the destination = %d %s, want 200", w.status, w.body)
			}
			wantUses(t, userUses(t, c),
				[]string{"kms:ReEncryptFrom on " + from, "kms:ReEncryptTo on " + to},
				[]string{"kms:ReEncryptFrom on " + to, "kms:ReEncryptTo on " + from})
		})
	}
}

// A move task receives from, deletes from and reads the attributes of its
// source, and sends to where the messages go: DestinationArn, or else the
// queues the source is the dead-letter queue of (sqs.md 9.3). Each action
// is recorded on its own queue (iam-enforcement-surfaces.md 10.2, 12.6).
func TestAMoveTaskPairsItsActionsWithTheirQueues(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceOff)
	const work, dlq, other = "arn:aws:sqs:us-east-1:000000000000:work", "arn:aws:sqs:us-east-1:000000000000:work-dlq", "arn:aws:sqs:us-east-1:000000000000:other"
	for _, name := range []string{"work", "work-dlq", "other"} {
		mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"`+name+`"}`)
	}
	redrive, err := json.Marshal(map[string]any{
		"QueueUrl":   endpoint + "/000000000000/work",
		"Attributes": map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"` + dlq + `","maxReceiveCount":"3"}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustSQS(t, endpoint, "SetQueueAttributes", string(redrive))
	if w := jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.StartMessageMoveTask", `{"SourceArn":"`+dlq+`"}`); w.status != http.StatusOK {
		t.Fatalf("StartMessageMoveTask to the sources = %d %s", w.status, w.body)
	}
	wantUses(t, userUses(t, c),
		[]string{
			"sqs:StartMessageMoveTask on " + dlq, "sqs:ReceiveMessage on " + dlq, "sqs:DeleteMessage on " + dlq, "sqs:GetQueueAttributes on " + dlq,
			"sqs:SendMessage on " + work,
		},
		[]string{
			"sqs:StartMessageMoveTask on " + work, "sqs:ReceiveMessage on " + work, "sqs:DeleteMessage on " + work, "sqs:GetQueueAttributes on " + work,
			"sqs:SendMessage on " + dlq,
		})

	var tasks struct{ Results []struct{ TaskHandle string } }
	if w := jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.ListMessageMoveTasks", `{"SourceArn":"`+dlq+`"}`); json.Unmarshal([]byte(w.body), &tasks) != nil || len(tasks.Results) == 0 {
		t.Fatalf("ListMessageMoveTasks = %d %s", w.status, w.body)
	}
	if w := jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.StartMessageMoveTask", `{"SourceArn":"`+dlq+`","DestinationArn":"`+other+`"}`); w.status != http.StatusOK {
		t.Fatalf("StartMessageMoveTask to a destination = %d %s", w.status, w.body)
	}
	wantUses(t, userUses(t, c),
		[]string{"sqs:SendMessage on " + other, "sqs:ListMessageMoveTasks on " + dlq},
		[]string{"sqs:SendMessage on " + dlq, "sqs:ReceiveMessage on " + other})
}

// AddRoleToInstanceProfile passes its role: it needs iam:PassRole on the
// role with iam:PassedToService ec2.amazonaws.com, which understudy sends,
// and iam:AddRoleToInstanceProfile on the profile alone
// (iam-enforcement-surfaces.md 10.2, 12.6).
func TestAddRoleToInstanceProfilePassesItsRole(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	const role, profile = "arn:aws:iam::000000000000:role/app", "arn:aws:iam::000000000000:instance-profile/app"
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	for _, form := range []url.Values{
		{"Action": {"CreateRole"}, "RoleName": {"app"}, "AssumeRolePolicyDocument": {trust}},
		{"Action": {"CreateInstanceProfile"}, "InstanceProfileName": {"app"}},
	} {
		form.Set("Version", "2010-05-08")
		if got := query(t, dummy, endpoint, "iam", form); got.status != http.StatusOK {
			t.Fatalf("%s = %d %s", form.Get("Action"), got.status, got.body)
		}
	}
	add := func() wire {
		return queryCall(t, u, endpoint, "iam", url.Values{"Action": {"AddRoleToInstanceProfile"}, "Version": {"2010-05-08"}, "InstanceProfileName": {"app"}, "RoleName": {"app"}})
	}
	addOnProfile := `{"Effect":"Allow","Action":"iam:AddRoleToInstanceProfile","Resource":"` + profile + `"}`
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+addOnProfile+`]}`)
	if w := add(); w.status != http.StatusForbidden || parseXMLError(t, w).Code != "AccessDenied" {
		t.Errorf("AddRoleToInstanceProfile without iam:PassRole = %d %s, want the IAM denial", w.status, w.body)
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+addOnProfile+`,`+
		`{"Effect":"Allow","Action":"iam:PassRole","Resource":"`+role+`","Condition":{"StringEquals":{"iam:PassedToService":"ec2.amazonaws.com"}}}]}`)
	if w := add(); w.status != http.StatusOK {
		t.Errorf("AddRoleToInstanceProfile with iam:PassRole to ec2.amazonaws.com = %d %s, want 200", w.status, w.body)
	}
	wantUses(t, userUses(t, c),
		[]string{"iam:PassRole on " + role, "iam:AddRoleToInstanceProfile on " + profile},
		[]string{"iam:AddRoleToInstanceProfile on " + role, "iam:PassRole on " + profile})
}

// SetTopicAttributes, Subscribe and SetSubscriptionAttributes record
// iam:PassRole on the role their attributes pass, and a call that passes none
// records none (iam-enforcement-surfaces.md 10.2, 10.4).
func TestSNSAttributeCallsRecordPassRoleOnlyOnTheRolesTheyPass(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceOff)
	const topic = "arn:aws:sns:us-east-1:000000000000:alerts"
	const feedback = "arn:aws:iam::000000000000:role/feedback"
	const subRole = "arn:aws:iam::000000000000:role/sub"
	const subRole2 = "arn:aws:iam::000000000000:role/sub2"
	sns := func(form url.Values) wire {
		t.Helper()
		form.Set("Version", "2010-03-31")
		w := queryCall(t, u, endpoint, "sns", form)
		if w.status != http.StatusOK {
			t.Fatalf("SNS %s = %d %s", form.Get("Action"), w.status, w.body)
		}
		return w
	}
	passRoles := func() []string {
		var out []string
		for _, use := range userUses(t, c) {
			if strings.HasPrefix(use, "iam:PassRole on ") {
				out = append(out, use)
			}
		}
		return out
	}
	sns(url.Values{"Action": {"CreateTopic"}, "Name": {"alerts"}})
	sns(url.Values{"Action": {"SetTopicAttributes"}, "TopicArn": {topic}, "AttributeName": {"DisplayName"}, "AttributeValue": {"x"}})
	if got := passRoles(); len(got) != 0 {
		t.Fatalf("calls that pass no role recorded %q", got)
	}

	sns(url.Values{"Action": {"SetTopicAttributes"}, "TopicArn": {topic}, "AttributeName": {"LambdaSuccessFeedbackRoleArn"}, "AttributeValue": {feedback}})
	wantUses(t, userUses(t, c), []string{"iam:PassRole on " + feedback}, nil)

	subscribed := sns(url.Values{
		"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:q"},
		"Attributes.entry.1.key": {"SubscriptionRoleArn"}, "Attributes.entry.1.value": {subRole},
	})
	wantUses(t, userUses(t, c), []string{"iam:PassRole on " + subRole}, nil)

	arn := xmlElement(t, subscribed.body, "SubscriptionArn")
	sns(url.Values{"Action": {"SetSubscriptionAttributes"}, "SubscriptionArn": {arn}, "AttributeName": {"SubscriptionRoleArn"}, "AttributeValue": {subRole2}})
	wantUses(t, userUses(t, c), []string{"iam:PassRole on " + subRole2}, nil)
}

// A create records its tag action only when it sends tags, and CreateTopic
// records iam:PassRole on each delivery-status role it passes
// (iam-enforcement-surfaces.md 10.2, 10.4).
func TestACreateRecordsItsTagAndPassRoleActionsOnlyWhenSent(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceOff)
	const tagged, plain = "arn:aws:sqs:us-east-1:000000000000:tagged", "arn:aws:sqs:us-east-1:000000000000:plain"
	if w := jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.CreateQueue", `{"QueueName":"tagged","tags":{"team":"a"}}`); w.status != http.StatusOK {
		t.Fatalf("CreateQueue with tags = %d %s", w.status, w.body)
	}
	if w := jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.CreateQueue", `{"QueueName":"plain"}`); w.status != http.StatusOK {
		t.Fatalf("CreateQueue = %d %s", w.status, w.body)
	}
	const feedback = "arn:aws:iam::000000000000:role/feedback"
	if w := queryCall(t, u, endpoint, "sns", url.Values{
		"Action": {"CreateTopic"}, "Version": {"2010-03-31"}, "Name": {"alerts"},
		"Attributes.entry.1.key": {"LambdaSuccessFeedbackRoleArn"}, "Attributes.entry.1.value": {feedback},
	}); w.status != http.StatusOK {
		t.Fatalf("CreateTopic with a feedback role = %d %s", w.status, w.body)
	}
	wantUses(t, userUses(t, c),
		[]string{"sqs:TagQueue on " + tagged, "sqs:CreateQueue on " + plain, "iam:PassRole on " + feedback},
		[]string{"sqs:TagQueue on " + plain, "sns:TagResource on arn:aws:sns:us-east-1:000000000000:alerts"})
}
