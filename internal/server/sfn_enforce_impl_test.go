package server_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// Step Functions under iam.enforce (spec "Enforce matrix"): the caller's
// iam:PassRole carries iam:PassedToService states.amazonaws.com, the
// execution role must trust states.amazonaws.com and, with logging on, may
// deliver logs, and a Lambda task invokes as the role. Every denial is
// SFN's AccessDeniedException at 400 (switch sfn.denial.status).

const (
	sfnRole       = "arn:aws:iam::000000000000:role/sfn"
	sfnLogGroup   = "arn:aws:logs:us-east-1:000000000000:log-group:sfn-logs:*"
	sfnDefinition = `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`
)

// trustOf is a trust policy that lets service assume the role.
func trustOf(service string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"` + service + `"},"Action":"sts:AssumeRole"}]}`
}

// iamAsRoot sends an IAM action as root and fails the test unless it
// succeeds.
func iamAsRoot(t *testing.T, endpoint string, form url.Values) {
	t.Helper()
	form.Set("Version", "2010-05-08")
	if got := query(t, dummy, endpoint, "iam", form); got.status != http.StatusOK {
		t.Fatalf("%s = %d %s", form.Get("Action"), got.status, got.body)
	}
}

// sfnRoleTrusting creates the role sfn, trusted by service.
func sfnRoleTrusting(t *testing.T, endpoint, service string) {
	t.Helper()
	iamAsRoot(t, endpoint, url.Values{"Action": {"CreateRole"}, "RoleName": {"sfn"}, "AssumeRolePolicyDocument": {trustOf(service)}})
}

// sfnCall sends a Step Functions action signed by s.
func sfnCall(t *testing.T, s signer, endpoint, action, body string) wire {
	t.Helper()
	return jsonCall(t, s, endpoint, "states", "1.0", "AWSStepFunctions."+action, body)
}

// createMachine asks for a machine named name running the sfn role, with
// extra members appended to the request.
func createMachine(t *testing.T, s signer, endpoint, name, definition, extra string) wire {
	t.Helper()
	def, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	return sfnCall(t, s, endpoint, "CreateStateMachine", `{"name":"`+name+`","definition":`+string(def)+`,"roleArn":"`+sfnRole+`"`+extra+`}`)
}

// wantSFNDenied checks w is SFN's AccessDeniedException with a message
// holding text.
func wantSFNDenied(t *testing.T, what string, w wire, text string) {
	t.Helper()
	var body struct{ Message string }
	if err := json.Unmarshal([]byte(w.body), &body); err != nil {
		t.Errorf("%s = %d %s, not a JSON error: %v", what, w.status, w.body, err)
		return
	}
	if w.status != http.StatusBadRequest || jsonType(w) != "AccessDeniedException" || !strings.Contains(body.Message, text) {
		t.Errorf("%s = %d %s, want 400 AccessDeniedException with %q", what, w.status, w.body, text)
	}
}

// passRoleTo is the caller's policy: states:* and iam:PassRole on the role
// when it is passed to service.
func passRoleTo(service string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"states:*","Resource":"*"},` +
		`{"Effect":"Allow","Action":"iam:PassRole","Resource":"` + sfnRole + `","Condition":{"StringEquals":{"iam:PassedToService":"` + service + `"}}}]}`
}

// Under enforce, a PassRole statement conditioned on
// states.amazonaws.com lets the create pass the role and one conditioned on
// lambda.amazonaws.com is denied: the pass carries
// iam:PassedToService=states.amazonaws.com (stepfunctions-api.md 5.1, 9.5).
func TestSFNPassRoleCarriesPassedToService(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	sfnRoleTrusting(t, endpoint, "states.amazonaws.com")
	putUserPolicy(t, endpoint, passRoleTo("lambda.amazonaws.com"))
	wantSFNDenied(t, "create with PassRole to lambda.amazonaws.com", createMachine(t, u, endpoint, "m", sfnDefinition, ""), "iam:PassRole")
	putUserPolicy(t, endpoint, passRoleTo("states.amazonaws.com"))
	if w := createMachine(t, u, endpoint, "m", sfnDefinition, ""); w.status != http.StatusOK {
		t.Errorf("create with PassRole to states.amazonaws.com = %d %s, want 200", w.status, w.body)
	}
	if w := sfnCall(t, u, endpoint, "UpdateStateMachine", `{"stateMachineArn":"arn:aws:states:us-east-1:000000000000:stateMachine:m","roleArn":"`+sfnRole+`"}`); w.status != http.StatusOK {
		t.Errorf("update passing the role = %d %s, want 200", w.status, w.body)
	}
}

// The execution role's trust policy is checked only under enforce: a role
// that trusts lambda.amazonaws.com alone, or no role at all, refuses the
// create and the update with understudy's text (UNVERIFIED, SFN-08);
// advise serves the call and journals the would-be denial; off checks the
// ARN's syntax only.
func TestSFNExecutionRoleTrust(t *testing.T) {
	const want = "Step Functions cannot assume the execution role " + sfnRole
	t.Run("enforce", func(t *testing.T) {
		_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
		putUserPolicy(t, endpoint, passRoleTo("states.amazonaws.com"))
		wantSFNDenied(t, "create with no such role", createMachine(t, u, endpoint, "m", sfnDefinition, ""), want)
		sfnRoleTrusting(t, endpoint, "lambda.amazonaws.com")
		wantSFNDenied(t, "create with a role trusting lambda.amazonaws.com", createMachine(t, u, endpoint, "m", sfnDefinition, ""), want)
		if w := sfnCall(t, u, endpoint, "DescribeStateMachine", `{"stateMachineArn":"arn:aws:states:us-east-1:000000000000:stateMachine:m"}`); w.status != http.StatusBadRequest || jsonType(w) != "StateMachineDoesNotExist" {
			t.Errorf("a refused create left a machine: %d %s", w.status, w.body)
		}
		iamAsRoot(t, endpoint, url.Values{"Action": {"UpdateAssumeRolePolicy"}, "RoleName": {"sfn"}, "PolicyDocument": {trustOf("states.amazonaws.com")}})
		if w := createMachine(t, u, endpoint, "m", sfnDefinition, ""); w.status != http.StatusOK {
			t.Fatalf("create with a role trusting states.amazonaws.com = %d %s, want 200", w.status, w.body)
		}
		iamAsRoot(t, endpoint, url.Values{"Action": {"UpdateAssumeRolePolicy"}, "RoleName": {"sfn"}, "PolicyDocument": {trustOf("lambda.amazonaws.com")}})
		wantSFNDenied(t, "update passing a role trusting lambda.amazonaws.com",
			sfnCall(t, u, endpoint, "UpdateStateMachine", `{"stateMachineArn":"arn:aws:states:us-east-1:000000000000:stateMachine:m","roleArn":"`+sfnRole+`"}`), want)
		if w := sfnCall(t, u, endpoint, "UpdateStateMachine", `{"stateMachineArn":"arn:aws:states:us-east-1:000000000000:stateMachine:m","definition":"{\"StartAt\":\"Q\",\"States\":{\"Q\":{\"Type\":\"Pass\",\"End\":true}}}"}`); w.status != http.StatusOK {
			t.Errorf("update of the definition alone = %d %s, want 200: the role is not passed again", w.status, w.body)
		}
	})
	t.Run("advise", func(t *testing.T) {
		c, endpoint, u := enforced(t, server.IAMEnforceAdvise)
		sfnRoleTrusting(t, endpoint, "lambda.amazonaws.com")
		if w := createMachine(t, u, endpoint, "m", sfnDefinition, ""); w.status != http.StatusOK {
			t.Fatalf("create under advise = %d %s, want 200", w.status, w.body)
		}
		advised := enforcementAdvisories(t, c, "access-would-deny")
		if !slices.ContainsFunc(advised, func(s string) bool {
			return strings.Contains(s, "states.amazonaws.com") && strings.Contains(s, "sts:AssumeRole") && strings.Contains(s, sfnRole)
		}) {
			t.Errorf("access-would-deny advisories = %q, want one for states.amazonaws.com assuming %s", advised, sfnRole)
		}
	})
	t.Run("off", func(t *testing.T) {
		c, endpoint, u := enforced(t, server.IAMEnforceOff)
		if w := createMachine(t, u, endpoint, "m", sfnDefinition, ""); w.status != http.StatusOK {
			t.Fatalf("create with no such role under off = %d %s, want 200", w.status, w.body)
		}
		if got := enforcementAdvisories(t, c, "access-would-deny"); len(got) != 0 {
			t.Errorf("off journaled access-would-deny advisories %q, want none", got)
		}
	})
}

// With logging on, the execution role needs the log-delivery actions under
// enforce, else AWS's documented text (stepfunctions-api.md 5.1, 9.5);
// with logging OFF nothing is asked.
func TestSFNLoggingNeedsTheRolesLogDelivery(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, passRoleTo("states.amazonaws.com"))
	sfnRoleTrusting(t, endpoint, "states.amazonaws.com")
	if w := jsonCall(t, dummy, endpoint, "logs", "1.1", "Logs_20140328.CreateLogGroup", `{"logGroupName":"sfn-logs"}`); w.status != http.StatusOK {
		t.Fatalf("CreateLogGroup = %d %s", w.status, w.body)
	}
	logging := `,"loggingConfiguration":{"level":"ALL","destinations":[{"cloudWatchLogsLogGroup":{"logGroupArn":"` + sfnLogGroup + `"}}]}`
	wantSFNDenied(t, "create logging with a role without log delivery", createMachine(t, u, endpoint, "m", sfnDefinition, logging),
		"The state machine IAM Role is not authorized to access the Log Destination")
	if w := createMachine(t, u, endpoint, "off", sfnDefinition, `,"loggingConfiguration":{"level":"OFF"}`); w.status != http.StatusOK {
		t.Errorf("create with logging OFF = %d %s, want 200", w.status, w.body)
	}
	wantSFNDenied(t, "update turning logging on", sfnCall(t, u, endpoint, "UpdateStateMachine",
		`{"stateMachineArn":"arn:aws:states:us-east-1:000000000000:stateMachine:off"`+logging+`}`),
		"The state machine IAM Role is not authorized to access the Log Destination")
	iamAsRoot(t, endpoint, url.Values{"Action": {"PutRolePolicy"}, "RoleName": {"sfn"}, "PolicyName": {"logs"}, "PolicyDocument": {
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogDelivery","logs:GetLogDelivery","logs:UpdateLogDelivery",` +
			`"logs:DeleteLogDelivery","logs:ListLogDeliveries","logs:PutResourcePolicy","logs:DescribeResourcePolicies","logs:DescribeLogGroups"],"Resource":"*"}]}`,
	}})
	if w := createMachine(t, u, endpoint, "m", sfnDefinition, logging); w.status != http.StatusOK {
		t.Errorf("create logging with a role allowed log delivery = %d %s, want 200", w.status, w.body)
	}
}

// A Lambda task whose role may not invoke the function fails under enforce
// with Lambda.AWSLambdaException and an AccessDeniedException cause
// (switch sfn.integration.lambda-error); the function never runs, so no
// container is needed.
func TestSFNLambdaTaskDeniedUnderEnforce(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	sfnRoleTrusting(t, endpoint, "states.amazonaws.com")
	pkg := base64.StdEncoding.EncodeToString(zipped(t, "unused"))
	body := `{"FunctionName":"fn","Runtime":"provided.al2023","Handler":"bootstrap","Role":"arn:aws:iam::000000000000:role/fn","Code":{"ZipFile":"` + pkg + `"}}`
	req := newRequest(t, http.MethodPost, endpoint+"/2015-03-31/functions", body)
	req.Header.Set("Content-Type", "application/json")
	if w := signedCall(t, dummy, "lambda", req, body); w.status != http.StatusCreated {
		t.Fatalf("CreateFunction = %d %s", w.status, w.body)
	}
	const fn = "arn:aws:lambda:us-east-1:000000000000:function:fn"
	if w := createMachine(t, dummy, endpoint, "task", `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"`+fn+`","End":true}}}`, ""); w.status != http.StatusOK {
		t.Fatalf("create = %d %s", w.status, w.body)
	}
	started := sfnCall(t, dummy, endpoint, "StartExecution", `{"stateMachineArn":"arn:aws:states:us-east-1:000000000000:stateMachine:task","input":"{}"}`)
	var out struct{ ExecutionArn string }
	if err := json.Unmarshal([]byte(started.body), &out); started.status != http.StatusOK || err != nil {
		t.Fatalf("StartExecution = %d %s", started.status, started.body)
	}
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	described := sfnCall(t, dummy, endpoint, "DescribeExecution", `{"executionArn":"`+out.ExecutionArn+`"}`)
	var d struct{ Status, Error, Cause string }
	if err := json.Unmarshal([]byte(described.body), &d); err != nil {
		t.Fatalf("DescribeExecution = %d %s", described.status, described.body)
	}
	if d.Status != "FAILED" || d.Error != "Lambda.AWSLambdaException" || !strings.Contains(d.Cause, "Error Code: AccessDeniedException") {
		t.Errorf("execution = %s %s %q, want FAILED Lambda.AWSLambdaException with an AccessDeniedException cause", d.Status, d.Error, d.Cause)
	}
	if got := userPolicyOf(t, c, sfnRole); !strings.Contains(got, `"lambda:InvokeFunction"`) || !strings.Contains(got, fn) {
		t.Errorf("/iam/policy for the role = %s, want lambda:InvokeFunction on %s", got, fn)
	}
}

// /iam/policy for a caller who created a machine without tags lists
// states:CreateStateMachine and iam:PassRole on the role with its
// iam:PassedToService condition, and nothing else (iam-enforcement-surfaces.md
// 12.6; spec ticket 13).
func TestSFNPolicyListsPassRoleWithItsCondition(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceOff)
	if w := createMachine(t, u, endpoint, "m", sfnDefinition, ""); w.status != http.StatusOK {
		t.Fatalf("create = %d %s", w.status, w.body)
	}
	var doc struct {
		Statement []struct {
			Action    json.RawMessage
			Resource  json.RawMessage
			Condition json.RawMessage
		}
	}
	got := userPolicyOf(t, c, userU)
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	actions := make([]string, 0, len(doc.Statement))
	for _, s := range doc.Statement {
		var one string
		if json.Unmarshal(s.Action, &one) != nil {
			t.Fatalf("statement actions %s, want one action each", s.Action)
		}
		actions = append(actions, one)
		if one == "iam:PassRole" && (string(s.Resource) != `"`+sfnRole+`"` || string(s.Condition) != `{"StringEquals":{"iam:PassedToService":"states.amazonaws.com"}}`) {
			t.Errorf("PassRole statement on %s with condition %s, want the role on iam:PassedToService states.amazonaws.com", s.Resource, s.Condition)
		}
	}
	slices.Sort(actions)
	if !slices.Equal(actions, []string{"iam:PassRole", "states:CreateStateMachine"}) {
		t.Errorf("/iam/policy = %s, want iam:PassRole and states:CreateStateMachine only", got)
	}
}

// userPolicyOf is the /iam/policy document for principal.
func userPolicyOf(t *testing.T, c *admin.Client, principal string) string {
	t.Helper()
	p, err := c.IAMPolicy(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	return string(p.Document)
}
