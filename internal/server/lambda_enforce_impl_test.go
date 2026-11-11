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

// Lambda's execution role under iam.enforce: CreateFunction, and an
// UpdateFunctionConfiguration that passes a Role, ask whether
// lambda.amazonaws.com may assume the role.

const lambdaRole = "arn:aws:iam::000000000000:role/fn"

// trustAccount is a trust policy that lets the account, and no service,
// assume the role, as the recording's role us1103-enf-none did.
const trustAccount = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sts:AssumeRole"}]}`

// roleTrustingAccount creates the role name, which the account and no
// service may assume.
func roleTrustingAccount(t *testing.T, endpoint, name string) {
	t.Helper()
	iamAsRoot(t, endpoint, url.Values{"Action": {"CreateRole"}, "RoleName": {name}, "AssumeRolePolicyDocument": {trustAccount}})
}

// roleTrustingLambda creates the role name, which lambda.amazonaws.com may
// assume: the execution role a function needs to be created under enforce.
func roleTrustingLambda(t *testing.T, endpoint, name string) {
	t.Helper()
	iamAsRoot(t, endpoint, url.Values{"Action": {"CreateRole"}, "RoleName": {name}, "AssumeRolePolicyDocument": {trustOf("lambda.amazonaws.com")}})
}

// createFunction asks, signed by s, for a zip function named name whose
// execution role is lambdaRole.
func createFunction(t *testing.T, s signer, endpoint, name string) wire {
	t.Helper()
	pkg := base64.StdEncoding.EncodeToString(zipped(t, "unused"))
	body := `{"FunctionName":"` + name + `","Runtime":"provided.al2023","Handler":"bootstrap","Role":"` + lambdaRole + `","Code":{"ZipFile":"` + pkg + `"}}`
	req := newRequest(t, http.MethodPost, endpoint+"/2015-03-31/functions", body)
	req.Header.Set("Content-Type", "application/json")
	return signedCall(t, s, "lambda", req, body)
}

// updateConfiguration sends, signed by s, an UpdateFunctionConfiguration
// of the function name with body.
func updateConfiguration(t *testing.T, s signer, endpoint, name, body string) wire {
	t.Helper()
	req := newRequest(t, http.MethodPut, endpoint+"/2015-03-31/functions/"+name+"/configuration", body)
	req.Header.Set("Content-Type", "application/json")
	return signedCall(t, s, "lambda", req, body)
}

// wantUnassumable checks w is the recorded refusal of a role Lambda cannot
// assume: 400 InvalidParameterValueException, the body
// {"Type":"User","message":"The role defined for the function cannot be
// assumed by Lambda."} member for member (iam-enforcement-surfaces.md 4.3,
// testdata/parity/lambda/IAM-ENF/26-bad-trust.json).
func wantUnassumable(t *testing.T, what string, w wire) {
	t.Helper()
	want := map[string]string{"Type": "User", "message": "The role defined for the function cannot be assumed by Lambda."}
	var got map[string]string
	if err := json.Unmarshal([]byte(w.body), &got); err != nil || w.status != http.StatusBadRequest ||
		w.header.Get("X-Amzn-Errortype") != "InvalidParameterValueException" || !maps.Equal(got, want) {
		t.Errorf("%s = %d %s %s, want 400 InvalidParameterValueException %v", what, w.status, w.header.Get("X-Amzn-Errortype"), w.body, want)
	}
}

// lambdaCaller is the user u's policy: lambda:* and iam:PassRole on every
// role.
const lambdaCaller = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["lambda:*","iam:PassRole"],"Resource":"*"}]}`

// Under enforce, CreateFunction refuses a role that does not trust
// lambda.amazonaws.com, and one that does not exist, with the recorded
// answer (26-bad-trust), and leaves no function; a role that trusts Lambda
// is accepted. UpdateFunctionConfiguration passing an untrusting Role is
// refused alike, which is inferred: only CreateFunction was recorded. An
// update that passes no Role asks nothing of the role.
func TestLambdaExecutionRoleTrustUnderEnforce(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, lambdaCaller)
	wantUnassumable(t, "create with no such role", createFunction(t, u, endpoint, "fn"))
	roleTrustingAccount(t, endpoint, "fn")
	wantUnassumable(t, "create with a role trusting the account only", createFunction(t, u, endpoint, "fn"))
	if w := restGet(t, u, "lambda", endpoint+"/2015-03-31/functions/fn"); w.status != http.StatusNotFound {
		t.Errorf("a refused create left a function: %d %s", w.status, w.body)
	}
	iamAsRoot(t, endpoint, url.Values{"Action": {"UpdateAssumeRolePolicy"}, "RoleName": {"fn"}, "PolicyDocument": {trustOf("lambda.amazonaws.com")}})
	if w := createFunction(t, u, endpoint, "fn"); w.status != http.StatusCreated {
		t.Fatalf("create with a role trusting lambda.amazonaws.com = %d %s, want 201", w.status, w.body)
	}
	roleTrustingAccount(t, endpoint, "other")
	wantUnassumable(t, "update passing a role trusting the account only",
		updateConfiguration(t, u, endpoint, "fn", `{"Role":"arn:aws:iam::000000000000:role/other"}`))
	if w := updateConfiguration(t, u, endpoint, "fn", `{"Timeout":10}`); w.status != http.StatusOK {
		t.Errorf("update of the timeout alone = %d %s, want 200: the role is not passed again", w.status, w.body)
	}
}

// A caller without iam:PassRole is refused the pass before the trust is
// asked about, as 26-no-passrole recorded: 403 AccessDeniedException
// naming iam:PassRole on the role, though the role does not trust Lambda
// either.
func TestLambdaPassRoleDenialPrecedesTheTrustCheck(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:CreateFunction","Resource":"*"}]}`)
	roleTrustingAccount(t, endpoint, "fn")
	w := createFunction(t, u, endpoint, "fn")
	want := "User: " + userU + " is not authorized to perform: iam:PassRole on resource: " + lambdaRole + " because no identity-based policy allows the iam:PassRole action"
	var body struct{ Message string }
	if err := json.Unmarshal([]byte(w.body), &body); err != nil || w.status != http.StatusForbidden || jsonType(w) != "AccessDeniedException" || body.Message != want {
		t.Errorf("create without iam:PassRole = %d %s, want 403 AccessDeniedException %q", w.status, w.body, want)
	}
}

// /iam/policy grants a CreateFunction's iam:PassRole on the role on the
// iam:PassedToService the pass carries, lambda.amazonaws.com
// (iam-enforcement-surfaces.md 10.2).
func TestLambdaPolicyGrantsPassRoleToLambda(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceOff)
	if w := createFunction(t, u, endpoint, "fn"); w.status != http.StatusCreated {
		t.Fatalf("create = %d %s", w.status, w.body)
	}
	type statement struct{ Action, Resource, Condition json.RawMessage }
	var doc struct{ Statement []statement }
	got := userPolicyOf(t, c, userU)
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(doc.Statement, func(s statement) bool {
		return string(s.Action) == `"iam:PassRole"` && string(s.Resource) == `"`+lambdaRole+`"` &&
			string(s.Condition) == `{"StringEquals":{"iam:PassedToService":"lambda.amazonaws.com"}}`
	}) {
		t.Errorf("/iam/policy = %s, want iam:PassRole on %s on iam:PassedToService lambda.amazonaws.com", got, lambdaRole)
	}
}

// Under advise the create is served and the would-be denial of
// lambda.amazonaws.com assuming the role is journaled; under off nothing
// is asked.
func TestLambdaExecutionRoleTrustUnderAdviseAndOff(t *testing.T) {
	t.Run("advise", func(t *testing.T) {
		c, endpoint, u := enforced(t, server.IAMEnforceAdvise)
		roleTrustingAccount(t, endpoint, "fn")
		if w := createFunction(t, u, endpoint, "fn"); w.status != http.StatusCreated {
			t.Fatalf("create under advise = %d %s, want 201", w.status, w.body)
		}
		advised := enforcementAdvisories(t, c, "access-would-deny")
		if !slices.ContainsFunc(advised, func(s string) bool {
			return strings.Contains(s, "lambda.amazonaws.com") && strings.Contains(s, "sts:AssumeRole") && strings.Contains(s, lambdaRole)
		}) {
			t.Errorf("access-would-deny advisories = %q, want one for lambda.amazonaws.com assuming %s", advised, lambdaRole)
		}
	})
	t.Run("off", func(t *testing.T) {
		c, endpoint, u := enforced(t, server.IAMEnforceOff)
		if w := createFunction(t, u, endpoint, "fn"); w.status != http.StatusCreated {
			t.Fatalf("create with no such role under off = %d %s, want 201", w.status, w.body)
		}
		if got := enforcementAdvisories(t, c, "access-would-deny"); len(got) != 0 {
			t.Errorf("off journaled access-would-deny advisories %q, want none", got)
		}
	})
}
