package gosdk_test

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Research: docs/research/phase1/iam-sts-kms.md section 4, error-matrix.md 5.11.

func TestSTSGetCallerIdentity(t *testing.T) {
	t.Parallel()
	out, err := sts.NewFromConfig(awsCfg).GetCallerIdentity(t.Context(), &sts.GetCallerIdentityInput{})
	ok(t, err, "GetCallerIdentity")
	acct := aws.ToString(out.Account)
	if !regexp.MustCompile(`^\d{12}$`).MatchString(acct) {
		t.Fatalf("Account = %q, want 12 digits", acct)
	}
	// The suite signs with the access key "test", the account root.
	if got, want := aws.ToString(out.Arn), "arn:aws:iam::"+acct+":root"; got != want {
		t.Errorf("Arn = %q, want %q (the account root's ARN)", got, want)
	}
	if got := aws.ToString(out.UserId); got != acct {
		t.Errorf("UserId = %q, want the account id %q for the root", got, acct)
	}
}

func TestSTSAssumeRole(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	role := createRole(t, name("sts-role"), lambdaTrustPolicy)
	client := sts.NewFromConfig(awsCfg)
	acct := account(t)
	session := "conformance-session"

	before := time.Now().Add(-time.Minute)
	out, err := client.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         role.Arn,
		RoleSessionName: aws.String(session),
		DurationSeconds: aws.Int32(900),
	})
	ok(t, err, "AssumeRole")
	wantArn := "arn:aws:sts::" + acct + ":assumed-role/" + aws.ToString(role.RoleName) + "/" + session
	if got := aws.ToString(out.AssumedRoleUser.Arn); got != wantArn {
		t.Errorf("AssumedRoleUser.Arn = %q, want %q", got, wantArn)
	}
	if got, want := aws.ToString(out.AssumedRoleUser.AssumedRoleId), aws.ToString(role.RoleId)+":"+session; got != want {
		t.Errorf("AssumedRoleUser.AssumedRoleId = %q, want %q", got, want)
	}
	creds := out.Credentials
	if creds == nil {
		t.Fatal("AssumeRole returned no Credentials")
	}
	if !strings.HasPrefix(aws.ToString(creds.AccessKeyId), "ASIA") {
		t.Errorf("Credentials.AccessKeyId = %q, want an ASIA temporary key", aws.ToString(creds.AccessKeyId))
	}
	if aws.ToString(creds.SecretAccessKey) == "" || aws.ToString(creds.SessionToken) == "" {
		t.Errorf("Credentials lack a SecretAccessKey or SessionToken: %+v", creds)
	}
	if exp := aws.ToTime(creds.Expiration); exp.Before(before.Add(900*time.Second)) || exp.After(time.Now().Add(900*time.Second+time.Minute)) {
		t.Errorf("Credentials.Expiration = %s, want about 900 s from now", exp)
	}

	t.Run("caller identity of the session", func(t *testing.T) {
		cfg := awsCfg.Copy()
		cfg.Credentials = credentials.NewStaticCredentialsProvider(aws.ToString(creds.AccessKeyId), aws.ToString(creds.SecretAccessKey), aws.ToString(creds.SessionToken))
		id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		ok(t, err, "GetCallerIdentity with the assumed-role credentials")
		if got := aws.ToString(id.Arn); got != wantArn {
			t.Errorf("Arn = %q, want the assumed-role ARN %q", got, wantArn)
		}
		if got, want := aws.ToString(id.UserId), aws.ToString(role.RoleId)+":"+session; got != want {
			t.Errorf("UserId = %q, want %q", got, want)
		}
		if got := aws.ToString(id.Account); got != acct {
			t.Errorf("Account = %q, want the role's account %q", got, acct)
		}
	})

	t.Run("session name pattern", func(t *testing.T) {
		_, err := client.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: aws.String("Session1:2")})
		apiErr(t, err, "AssumeRole with RoleSessionName Session1:2", "ValidationError")
		wantStatus(t, err, "AssumeRole with RoleSessionName Session1:2", 400)
		wantMessage(t, err, "AssumeRole with RoleSessionName Session1:2",
			`1 validation error detected: Value 'Session1:2' at 'roleSessionName' failed to satisfy constraint: Member must satisfy regular expression pattern: [\w+=,.@-]*`)
	})

	t.Run("duration above the role maximum", func(t *testing.T) {
		_, err := client.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: aws.String(session), DurationSeconds: aws.Int32(7200)})
		apiErr(t, err, "AssumeRole with DurationSeconds above MaxSessionDuration", "ValidationError")
		wantMessage(t, err, "AssumeRole with DurationSeconds above MaxSessionDuration", "The requested DurationSeconds exceeds the MaxSessionDuration set for this role.")
	})

	t.Run("role that does not exist", func(t *testing.T) {
		_, err := client.AssumeRole(ctx, &sts.AssumeRoleInput{
			RoleArn:         aws.String("arn:aws:iam::" + acct + ":role/" + name("missing")),
			RoleSessionName: aws.String(session),
		})
		apiErr(t, err, "AssumeRole of a missing role", "AccessDenied")
		wantStatus(t, err, "AssumeRole of a missing role", 403)
	})
}

func TestSTSGetSessionToken(t *testing.T) {
	t.Parallel()
	out, err := sts.NewFromConfig(awsCfg).GetSessionToken(t.Context(), &sts.GetSessionTokenInput{})
	ok(t, err, "GetSessionToken")
	creds := out.Credentials
	if creds == nil {
		t.Fatal("GetSessionToken returned no Credentials")
	}
	if !strings.HasPrefix(aws.ToString(creds.AccessKeyId), "ASIA") {
		t.Errorf("AccessKeyId = %q, want an ASIA temporary key", aws.ToString(creds.AccessKeyId))
	}
	if aws.ToString(creds.SecretAccessKey) == "" || aws.ToString(creds.SessionToken) == "" {
		t.Errorf("Credentials lack a SecretAccessKey or SessionToken: %+v", creds)
	}
	if !aws.ToTime(creds.Expiration).After(time.Now()) {
		t.Errorf("Expiration = %s, want a time in the future", aws.ToTime(creds.Expiration))
	}
}

// lambdaTrustPolicy lets Lambda assume a role.
const lambdaTrustPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

// createRole creates an IAM role with the trust policy and deletes it when
// the test ends.
func createRole(t *testing.T, roleName, trust string) *iamRole {
	t.Helper()
	client := iam.NewFromConfig(awsCfg)
	out, err := client.CreateRole(t.Context(), &iam.CreateRoleInput{RoleName: aws.String(roleName), AssumeRolePolicyDocument: aws.String(trust)})
	ok(t, err, "CreateRole "+roleName)
	t.Cleanup(func() {
		_, _ = client.DeleteRole(context.Background(), &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
	})
	return &iamRole{RoleName: out.Role.RoleName, RoleId: out.Role.RoleId, Arn: out.Role.Arn}
}

// iamRole is what tests need of a created role.
type iamRole struct {
	RoleName, RoleId, Arn *string
}
