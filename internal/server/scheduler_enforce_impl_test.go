package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/scheduler"
)

// EventBridge Scheduler under iam.enforce, against the real Authorizer
// (scheduler.md 9, 14.11): each operation's action
// on the schedule or group ARN, iam:PassRole with iam:PassedToService
// scheduler.amazonaws.com, the execution role's trust at create and at
// invocation, scheduler:DeleteSchedule per member of a deleted group, the
// caller's KMS actions at create and the role's kms:Decrypt at invocation.

const (
	schRole   = "arn:aws:iam::000000000000:role/sch"
	schTarget = "arn:aws:sqs:us-east-1:000000000000:target"
	schDLQ    = "arn:aws:sqs:us-east-1:000000000000:dlq"
	schTrust  = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"scheduler.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
)

// schCall sends a Scheduler restJson1 request signed by s.
func schCall(t *testing.T, s signer, endpoint, method, path, body string) wire {
	t.Helper()
	req := newRequest(t, method, endpoint+path, body)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return signedCall(t, s, "scheduler", req, body)
}

// schSchedule is a CreateSchedule body: a rate(1 hour) schedule starting
// 30 minutes after the server's frozen start, so each schAdvance makes one
// invocation, with an SQS target run as the sch role, a dead-letter queue
// and no retries, plus extra members.
func schSchedule(extra string) string {
	start := strconv.FormatInt(frozenAt.Add(30*time.Minute).Unix(), 10)
	return `{"ScheduleExpression":"rate(1 hour)","StartDate":` + start + `,"FlexibleTimeWindow":{"Mode":"OFF"},` +
		`"Target":{"Arn":"` + schTarget + `","RoleArn":"` + schRole + `","Input":"hello",` +
		`"DeadLetterConfig":{"Arn":"` + schDLQ + `"},"RetryPolicy":{"MaximumRetryAttempts":0}}` + extra + `}`
}

// schRoleTrusting creates the sch role with trust, as root.
func schRoleTrusting(t *testing.T, endpoint, trust string) {
	t.Helper()
	iamAsRoot(t, endpoint, url.Values{"Action": {"CreateRole"}, "RoleName": {"sch"}, "AssumeRolePolicyDocument": {trust}})
}

// schQueues creates the target and dead-letter queues; the dead-letter
// queue's policy lets any principal of the account send, so a dead letter
// arrives whatever the role may do.
func schQueues(t *testing.T, endpoint string) {
	t.Helper()
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"target"}`)
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"dlq"}`)
	setQueuePolicy(t, endpoint, "dlq", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"`+schDLQ+`"}]}`)
}

// schRolePolicy gives the sch role an inline policy, as root.
func schRolePolicy(t *testing.T, endpoint, document string) {
	t.Helper()
	iamAsRoot(t, endpoint, url.Values{"Action": {"PutRolePolicy"}, "RoleName": {"sch"}, "PolicyName": {"p"}, "PolicyDocument": {document}})
}

// schAdvance moves the emulator clock past the first invocation of a
// rate(1 hour) schedule and settles.
func schAdvance(t *testing.T, c *admin.Client) {
	t.Helper()
	if _, err := c.ChangeClock(t.Context(), admin.Advance{By: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

// deadLetters returns "<ERROR_CODE>: <ERROR_MESSAGE>" of each message the
// dlq holds.
func deadLetters(t *testing.T, endpoint string) []string {
	t.Helper()
	got := mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+endpoint+`/000000000000/dlq","MaxNumberOfMessages":10,"MessageAttributeNames":["All"]}`)
	var out struct {
		Messages []struct {
			MessageAttributes map[string]struct{ StringValue string }
		}
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("ReceiveMessage = %s: %v", got, err)
	}
	codes := make([]string, 0, len(out.Messages))
	for _, m := range out.Messages {
		codes = append(codes, m.MessageAttributes["ERROR_CODE"].StringValue+": "+m.MessageAttributes["ERROR_MESSAGE"].StringValue)
	}
	return codes
}

// wantSchedulerDenied checks w is Scheduler's denial identity,
// AccessDeniedException at the status of scheduler.denial.status, with a
// message holding text (iam-enforcement-surfaces.md 4.1, 12.3).
func wantSchedulerDenied(t *testing.T, what string, w wire, text string) {
	t.Helper()
	var body struct{ Message string }
	if err := json.Unmarshal([]byte(w.body), &body); err != nil {
		t.Errorf("%s = %d %s, not a JSON error: %v", what, w.status, w.body, err)
		return
	}
	if w.status != scheduler.DenialStatus().Value || jsonType(w) != "AccessDeniedException" || !strings.Contains(body.Message, text) {
		t.Errorf("%s = %d %s, want %d AccessDeniedException with %q", what, w.status, w.body, scheduler.DenialStatus().Value, text)
	}
}

// A GetSchedule the caller's policies do not allow answers
// AccessDeniedException at scheduler.denial.status's 403 with the
// documented grammar, which names the resource on an implicit
// identity-based denial too (IAM-ENF-37, recorded 2026-11-03;
// iam-enforcement-surfaces.md 4.1 and 12.3; scheduler.md 2.4).
func TestSchedulerDenialIdentityOverTheWire(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	w := schCall(t, u, endpoint, http.MethodGet, "/schedules/s", "")
	const message = "User: " + userU + " is not authorized to perform: scheduler:GetSchedule on resource: arn:aws:scheduler:us-east-1:000000000000:schedule/default/s because no identity-based policy allows the scheduler:GetSchedule action"
	// A map, since a struct field would match the key case-insensitively:
	// the key is the recorded capital Message (IS 12.3).
	var body map[string]string
	if err := json.Unmarshal([]byte(w.body), &body); err != nil || w.status != http.StatusForbidden || w.status != scheduler.DenialStatus().Value ||
		jsonType(w) != "AccessDeniedException" || body["Message"] != message {
		t.Errorf("denied GetSchedule = %d %s; want 403 AccessDeniedException %q", w.status, w.body, message)
	}
}

// schedulerPassRole is the caller's policy: scheduler:* and iam:PassRole on
// the role when it is passed to service.
func schedulerPassRole(service, extra string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"scheduler:*","Resource":"*"},` +
		`{"Effect":"Allow","Action":"iam:PassRole","Resource":"` + schRole + `","Condition":{"StringEquals":{"iam:PassedToService":"` + service + `"}}}` + extra + `]}`
}

// The caller's iam:PassRole carries iam:PassedToService
// scheduler.amazonaws.com (scheduler.md 9, 14.11): a
// statement conditioned on lambda.amazonaws.com does not allow the create
// and stores nothing; one conditioned on scheduler.amazonaws.com does.
func TestSchedulerPassRoleCarriesPassedToService(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	schRoleTrusting(t, endpoint, schTrust)
	putUserPolicy(t, endpoint, schedulerPassRole("lambda.amazonaws.com", ""))
	wantSchedulerDenied(t, "create with PassRole to lambda.amazonaws.com", schCall(t, u, endpoint, http.MethodPost, "/schedules/s", schSchedule("")), "iam:PassRole")
	if w := schCall(t, u, endpoint, http.MethodGet, "/schedules/s", ""); w.status != http.StatusNotFound {
		t.Errorf("a refused create left a schedule: %d %s", w.status, w.body)
	}
	putUserPolicy(t, endpoint, schedulerPassRole("scheduler.amazonaws.com", ""))
	if w := schCall(t, u, endpoint, http.MethodPost, "/schedules/s", schSchedule("")); w.status != http.StatusOK {
		t.Errorf("create with PassRole to scheduler.amazonaws.com = %d %s, want 200", w.status, w.body)
	}
	if w := schCall(t, u, endpoint, http.MethodPut, "/schedules/s", schSchedule("")); w.status != http.StatusOK {
		t.Errorf("update passing the role = %d %s, want 200", w.status, w.body)
	}
}

// The execution role's trust is checked at create and update only under
// enforce: a missing role or one trusting lambda.amazonaws.com alone
// answers the captured ValidationException and stores nothing (scheduler.md
// 1 item 10, 2.4, 14.11); advise serves the create and journals the
// would-be denial naming the role; off asks nothing.
func TestSchedulerExecutionRoleTrustAtWrite(t *testing.T) {
	const want = "The execution role you provide must allow AWS EventBridge Scheduler to assume the role."
	wantUntrusted := func(t *testing.T, what string, w wire) {
		t.Helper()
		var body struct{ Message string }
		if json.Unmarshal([]byte(w.body), &body) != nil || w.status != http.StatusBadRequest || jsonType(w) != "ValidationException" || body.Message != want {
			t.Errorf("%s = %d %s, want 400 ValidationException %q", what, w.status, w.body, want)
		}
	}
	t.Run("enforce", func(t *testing.T) {
		_, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
		wantUntrusted(t, "create with no such role", schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", schSchedule("")))
		schRoleTrusting(t, endpoint, trustOf("lambda.amazonaws.com"))
		wantUntrusted(t, "create with a role trusting lambda.amazonaws.com", schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", schSchedule("")))
		if w := schCall(t, dummy, endpoint, http.MethodGet, "/schedules/s", ""); w.status != http.StatusNotFound {
			t.Errorf("a refused create left a schedule: %d %s", w.status, w.body)
		}
		iamAsRoot(t, endpoint, url.Values{"Action": {"UpdateAssumeRolePolicy"}, "RoleName": {"sch"}, "PolicyDocument": {schTrust}})
		if w := schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", schSchedule("")); w.status != http.StatusOK {
			t.Fatalf("create with a role trusting scheduler.amazonaws.com = %d %s, want 200", w.status, w.body)
		}
		iamAsRoot(t, endpoint, url.Values{"Action": {"UpdateAssumeRolePolicy"}, "RoleName": {"sch"}, "PolicyDocument": {trustOf("lambda.amazonaws.com")}})
		wantUntrusted(t, "update with a role trusting lambda.amazonaws.com", schCall(t, dummy, endpoint, http.MethodPut, "/schedules/s", schSchedule("")))
	})
	t.Run("advise", func(t *testing.T) {
		c, endpoint, _ := enforced(t, server.IAMEnforceAdvise)
		schRoleTrusting(t, endpoint, trustOf("lambda.amazonaws.com"))
		if w := schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", schSchedule("")); w.status != http.StatusOK {
			t.Fatalf("create under advise = %d %s, want 200", w.status, w.body)
		}
		advised := enforcementAdvisories(t, c, "access-would-deny")
		if !slices.ContainsFunc(advised, func(s string) bool {
			return strings.Contains(s, "scheduler.amazonaws.com") && strings.Contains(s, "sts:AssumeRole") && strings.Contains(s, schRole)
		}) {
			t.Errorf("access-would-deny advisories = %q, want one for scheduler.amazonaws.com assuming %s", advised, schRole)
		}
	})
	t.Run("off", func(t *testing.T) {
		c, endpoint, _ := enforced(t, server.IAMEnforceOff)
		if w := schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", schSchedule("")); w.status != http.StatusOK {
			t.Fatalf("create with no such role under off = %d %s, want 200", w.status, w.body)
		}
		if got := enforcementAdvisories(t, c, "access-would-deny"); len(got) != 0 {
			t.Errorf("off journaled access-would-deny advisories %q, want none", got)
		}
	})
}

// A role deleted after create cannot be assumed at invocation: under
// enforce the target is never called and the invocation is dead-lettered
// with roleUnassumableCode, AccessDenied (UNVERIFIED, SCH-08 step x5);
// under advise it is delivered as a session
// of the named role and the would-be denial journaled.
func TestSchedulerRoleMissingAtInvocation(t *testing.T) {
	t.Run("enforce", func(t *testing.T) {
		c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
		schQueues(t, endpoint)
		schRoleTrusting(t, endpoint, schTrust)
		if w := schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", schSchedule("")); w.status != http.StatusOK {
			t.Fatalf("create = %d %s", w.status, w.body)
		}
		iamAsRoot(t, endpoint, url.Values{"Action": {"DeleteRole"}, "RoleName": {"sch"}})
		schAdvance(t, c)
		if got := queueBodies(t, c, endpoint, "target"); len(got) != 0 {
			t.Errorf("the target received %q from a role that no longer exists", got)
		}
		if got := deadLetters(t, endpoint); len(got) != 1 || !strings.HasPrefix(got[0], "AccessDenied: ") ||
			!strings.Contains(got[0], "scheduler.amazonaws.com") || !strings.Contains(got[0], "sts:AssumeRole") {
			t.Errorf("dead letters = %q, want one AccessDenied naming scheduler.amazonaws.com and sts:AssumeRole", got)
		}
	})
	t.Run("advise", func(t *testing.T) {
		c, endpoint, _ := enforced(t, server.IAMEnforceAdvise)
		schQueues(t, endpoint)
		schRoleTrusting(t, endpoint, schTrust)
		if w := schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", schSchedule("")); w.status != http.StatusOK {
			t.Fatalf("create = %d %s", w.status, w.body)
		}
		iamAsRoot(t, endpoint, url.Values{"Action": {"DeleteRole"}, "RoleName": {"sch"}})
		schAdvance(t, c)
		if got := queueBodies(t, c, endpoint, "target"); !slices.Contains(got, "hello") {
			t.Errorf("the target holds %q under advise, want the invocation", got)
		}
		advised := enforcementAdvisories(t, c, "access-would-deny")
		if !slices.ContainsFunc(advised, func(s string) bool {
			return strings.Contains(s, "scheduler.amazonaws.com") && strings.Contains(s, "sts:AssumeRole") && strings.Contains(s, schRole)
		}) {
			t.Errorf("access-would-deny advisories = %q, want one for scheduler.amazonaws.com assuming the deleted role", advised)
		}
	})
}

// Under enforce the role session's own sqs:SendMessage on the target is
// SQS's step 8: a role with no permission is refused, a client-side error
// that is dead-lettered, never delivered; with it, delivered
// (scheduler.md 9, 14.13.6).
func TestSchedulerRoleSessionTargetPermission(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	schQueues(t, endpoint)
	schRoleTrusting(t, endpoint, schTrust)
	if w := schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", schSchedule("")); w.status != http.StatusOK {
		t.Fatalf("create = %d %s", w.status, w.body)
	}
	schAdvance(t, c)
	if got := queueBodies(t, c, endpoint, "target"); len(got) != 0 {
		t.Errorf("the target received %q from a role without sqs:SendMessage", got)
	}
	// SQS spells the action in lower case (IAM-ENF-22, recorded 2026-11-03).
	if got := deadLetters(t, endpoint); len(got) != 1 || !strings.Contains(got[0], "sqs:sendmessage") {
		t.Errorf("dead letters = %q, want one with SQS's denial of sqs:sendmessage", got)
	}
	schRolePolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"`+schTarget+`"}]}`)
	schAdvance(t, c)
	if got := queueBodies(t, c, endpoint, "target"); !slices.Contains(got, "hello") {
		t.Errorf("the target holds %q, want the invocation once the role may send", got)
	}
}

// DeleteScheduleGroup asks for scheduler:DeleteSchedule on
// schedule/<group>/*, the resource AWS's denial names (SCH-10
// 10-03-group-delete-denied, recorded 2026-11-03; scheduler.md 9): a caller
// allowed the group delete but not that is denied naming the action, a
// grant on the member's own ARN does not do, and the group and its
// schedule remain.
func TestSchedulerDeleteGroupNeedsEachMember(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	schRoleTrusting(t, endpoint, schTrust)
	if w := schCall(t, dummy, endpoint, http.MethodPost, "/schedule-groups/g", `{}`); w.status != http.StatusOK {
		t.Fatalf("CreateScheduleGroup = %d %s", w.status, w.body)
	}
	if w := schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", schSchedule(`,"GroupName":"g"`)); w.status != http.StatusOK {
		t.Fatalf("create = %d %s", w.status, w.body)
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"scheduler:DeleteScheduleGroup","Resource":"arn:aws:scheduler:us-east-1:000000000000:schedule-group/g"}]}`)
	wantSchedulerDenied(t, "DeleteScheduleGroup without the member's DeleteSchedule",
		schCall(t, u, endpoint, http.MethodDelete, "/schedule-groups/g", ""), "scheduler:DeleteSchedule")
	// The resource is the literal schedule/g/*: a grant on the member's own
	// ARN does not match it.
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"scheduler:DeleteScheduleGroup","Resource":"arn:aws:scheduler:us-east-1:000000000000:schedule-group/g"},`+
		`{"Effect":"Allow","Action":"scheduler:DeleteSchedule","Resource":"arn:aws:scheduler:us-east-1:000000000000:schedule/g/s"}]}`)
	wantSchedulerDenied(t, "DeleteScheduleGroup allowed DeleteSchedule on the member's ARN only",
		schCall(t, u, endpoint, http.MethodDelete, "/schedule-groups/g", ""), "scheduler:DeleteSchedule")
	if w := schCall(t, dummy, endpoint, http.MethodGet, "/schedules/s?groupName=g", ""); w.status != http.StatusOK {
		t.Errorf("the member is gone after a denied group delete: %d %s", w.status, w.body)
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"scheduler:DeleteScheduleGroup","Resource":"arn:aws:scheduler:us-east-1:000000000000:schedule-group/g"},`+
		`{"Effect":"Allow","Action":"scheduler:DeleteSchedule","Resource":"arn:aws:scheduler:us-east-1:000000000000:schedule/g/*"}]}`)
	if w := schCall(t, u, endpoint, http.MethodDelete, "/schedule-groups/g", ""); w.status != http.StatusOK {
		t.Errorf("DeleteScheduleGroup with DeleteSchedule on schedule/g/* = %d %s, want 200", w.status, w.body)
	}
}

// createKey creates a KMS key as root and returns its ARN; its default key
// policy delegates to IAM (iam-evaluation.md 4.1).
func createKey(t *testing.T, endpoint string) string {
	t.Helper()
	var created struct{ KeyMetadata struct{ Arn string } }
	if w := jsonCall(t, dummy, endpoint, "kms", "1.1", "TrentService.CreateKey", "{}"); w.status != http.StatusOK || json.Unmarshal([]byte(w.body), &created) != nil {
		t.Fatalf("CreateKey = %d %s", w.status, w.body)
	}
	return created.KeyMetadata.Arn
}

// KmsKeyArn under enforce: the caller needs kms:DescribeKey,
// kms:GenerateDataKey and kms:Decrypt on the key at create (aws-doc
// encryption-rest; scheduler.md 9, 14.11), each denial naming its action;
// at invocation the role session needs kms:Decrypt before the target is
// called, else the invocation is dead-lettered with keyDeniedCode,
// AccessDeniedException (UNVERIFIED, SCH-04 04-kms-missing).
func TestSchedulerKMSKey(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	schQueues(t, endpoint)
	schRoleTrusting(t, endpoint, schTrust)
	key := createKey(t, endpoint)
	keyed := schSchedule(`,"KmsKeyArn":"` + key + `"`)
	putUserPolicy(t, endpoint, schedulerPassRole(principalScheduler, ""))
	wantSchedulerDenied(t, "create with a key the caller may not describe", schCall(t, u, endpoint, http.MethodPost, "/schedules/s", keyed), "kms:DescribeKey")
	putUserPolicy(t, endpoint, schedulerPassRole(principalScheduler, `,{"Effect":"Allow","Action":["kms:DescribeKey","kms:GenerateDataKey"],"Resource":"`+key+`"}`))
	wantSchedulerDenied(t, "create with a key the caller may not decrypt with", schCall(t, u, endpoint, http.MethodPost, "/schedules/s", keyed), "kms:Decrypt")
	if w := schCall(t, dummy, endpoint, http.MethodGet, "/schedules/s", ""); w.status != http.StatusNotFound {
		t.Errorf("a refused create left a schedule: %d %s", w.status, w.body)
	}
	putUserPolicy(t, endpoint, schedulerPassRole(principalScheduler, `,{"Effect":"Allow","Action":["kms:DescribeKey","kms:GenerateDataKey","kms:Decrypt"],"Resource":"`+key+`"}`))
	if w := schCall(t, u, endpoint, http.MethodPost, "/schedules/s", keyed); w.status != http.StatusOK {
		t.Fatalf("create with the three KMS actions = %d %s, want 200", w.status, w.body)
	}

	schRolePolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"`+schTarget+`"}]}`)
	schAdvance(t, c)
	if got := queueBodies(t, c, endpoint, "target"); len(got) != 0 {
		t.Errorf("the target received %q from a role that may not decrypt", got)
	}
	if got := deadLetters(t, endpoint); len(got) != 1 || !strings.HasPrefix(got[0], "AccessDeniedException: ") || !strings.Contains(got[0], "kms:Decrypt") {
		t.Errorf("dead letters = %q, want one AccessDeniedException naming kms:Decrypt", got)
	}
	schRolePolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"`+schTarget+`"},`+
		`{"Effect":"Allow","Action":"kms:Decrypt","Resource":"`+key+`"}]}`)
	schAdvance(t, c)
	if got := queueBodies(t, c, endpoint, "target"); !slices.Contains(got, "hello") {
		t.Errorf("the target holds %q, want the invocation once the role may decrypt", got)
	}
}

// A KmsKeyArn that names no key is accepted under enforce, with no KMS
// action asked, and echoed as the capture shows (SCH-04 04-kms-missing,
// 200); UpdateSchedule takes it too.
func TestSchedulerKMSKeyMissingIsAccepted(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	schQueues(t, endpoint)
	schRoleTrusting(t, endpoint, schTrust)
	putUserPolicy(t, endpoint, schedulerPassRole(principalScheduler, ""))
	missing := "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000"
	keyed := schSchedule(`,"KmsKeyArn":"` + missing + `"`)
	if w := schCall(t, u, endpoint, http.MethodPost, "/schedules/s", keyed); w.status != http.StatusOK {
		t.Fatalf("create with a missing key = %d %s, want 200", w.status, w.body)
	}
	if w := schCall(t, u, endpoint, http.MethodPut, "/schedules/s", keyed); w.status != http.StatusOK {
		t.Fatalf("update with a missing key = %d %s, want 200", w.status, w.body)
	}
	if w := schCall(t, dummy, endpoint, http.MethodGet, "/schedules/s", ""); w.status != http.StatusOK || !strings.Contains(w.body, missing) {
		t.Errorf("get = %d %s, want the key ARN echoed", w.status, w.body)
	}
}

const principalScheduler = "scheduler.amazonaws.com"

// A role session needs no grant in the target's resource policy
// (scheduler.md 1 item 1, 14.4.4): a queue whose policy names no Scheduler
// statement takes the invocation and no policy-would-deny advisory is
// journaled for it.
func TestSchedulerRoleSessionSkipsTheResourcePolicyAdvisory(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceOff)
	schQueues(t, endpoint)
	setQueuePolicy(t, endpoint, "target", accountPolicy("sns.amazonaws.com", "target", "000000000000"))
	schRoleTrusting(t, endpoint, schTrust)
	if w := schCall(t, dummy, endpoint, http.MethodPost, "/schedules/s", schSchedule("")); w.status != http.StatusOK {
		t.Fatalf("create = %d %s", w.status, w.body)
	}
	schAdvance(t, c)
	if got := queueBodies(t, c, endpoint, "target"); !slices.Contains(got, "hello") {
		t.Errorf("the target holds %q, want the invocation", got)
	}
	if got := enforcementAdvisories(t, c, "policy-would-deny"); len(got) != 0 {
		t.Errorf("policy-would-deny advisories = %q, want none for a role-session delivery", got)
	}
}

// /iam/policy for a caller who created an untagged group and a schedule in
// it lists the operations' actions on their ARNs and iam:PassRole on the
// role with Condition StringEquals iam:PassedToService
// scheduler.amazonaws.com, and no scheduler:TagResource; for the role it
// lists sqs:SendMessage on the target and the dead-letter queue its
// invocations used (iam-enforcement-surfaces.md 12.6;
// scheduler.md 14.11).
func TestSchedulerPolicyListsCallerAndRoleActions(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceOff)
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"dlq"}`)
	schRoleTrusting(t, endpoint, schTrust)
	if w := schCall(t, u, endpoint, http.MethodPost, "/schedule-groups/g", `{}`); w.status != http.StatusOK {
		t.Fatalf("CreateScheduleGroup = %d %s", w.status, w.body)
	}
	// The target queue does not exist, so the invocation is dead-lettered.
	if w := schCall(t, u, endpoint, http.MethodPost, "/schedules/s", schSchedule(`,"GroupName":"g"`)); w.status != http.StatusOK {
		t.Fatalf("create = %d %s", w.status, w.body)
	}
	schAdvance(t, c)

	type statement struct {
		Action    json.RawMessage
		Resource  json.RawMessage
		Condition json.RawMessage
	}
	statements := func(principal string) []statement {
		var doc struct{ Statement []statement }
		got := userPolicyOf(t, c, principal)
		if err := json.Unmarshal([]byte(got), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Statement
	}
	pairs := func(principal string) []string {
		got := statements(principal)
		out := make([]string, 0, len(got))
		for _, s := range got {
			var actions, resources []string
			if json.Unmarshal(s.Action, &actions) != nil {
				var one string
				if json.Unmarshal(s.Action, &one) != nil {
					t.Fatalf("statement action %s", s.Action)
				}
				actions = []string{one}
			}
			if json.Unmarshal(s.Resource, &resources) != nil {
				var one string
				if json.Unmarshal(s.Resource, &one) != nil {
					t.Fatalf("statement resource %s", s.Resource)
				}
				resources = []string{one}
			}
			for _, a := range actions {
				for _, r := range resources {
					out = append(out, a+" "+r)
				}
			}
			if slices.Contains(actions, "iam:PassRole") && string(s.Condition) != `{"StringEquals":{"iam:PassedToService":"scheduler.amazonaws.com"}}` {
				t.Errorf("PassRole statement on %s with condition %s, want iam:PassedToService scheduler.amazonaws.com", s.Resource, s.Condition)
			}
		}
		slices.Sort(out)
		return out
	}
	wantCaller := []string{
		"iam:PassRole " + schRole,
		"scheduler:CreateSchedule arn:aws:scheduler:us-east-1:000000000000:schedule/g/s",
		"scheduler:CreateScheduleGroup arn:aws:scheduler:us-east-1:000000000000:schedule-group/g",
	}
	if got := pairs(userU); !slices.Equal(got, wantCaller) {
		t.Errorf("/iam/policy for the caller = %q, want %q", got, wantCaller)
	}
	wantRole := []string{"sqs:SendMessage " + schDLQ, "sqs:SendMessage " + schTarget}
	if got := pairs(schRole); !slices.Equal(got, wantRole) {
		t.Errorf("/iam/policy for the role = %q, want %q", got, wantRole)
	}
}
