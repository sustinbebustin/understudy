package sts_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/iam"
	"github.com/sustinbebustin/understudy/internal/service/sts"
	"github.com/sustinbebustin/understudy/internal/store"
)

const trust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sts:AssumeRole"}]}`

// harness is a kernel with IAM and STS, whose client calls are signed by
// principal: the default account's root unless a test signs as another.
type harness struct {
	*kerneltest.K
	svc       *sts.Service
	principal identity.Principal
}

func run(t *testing.T, o sts.Options, fn func(h *harness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		iam.New(k.Deps(iamapi.Service()), iam.Options{})
		svc := sts.New(k.Deps(stsapi.Service()), o)
		fn(&harness{K: k, svc: svc, principal: identity.Root{Account: aws.DefaultAccount()}})
	}, kerneltest.Services(iamapi.Service(), stsapi.Service()))
}

// call runs op as a client request in the harness's scope, signed by its
// principal.
func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	c := h.Kernel.NewCall(kernel.ExternalCall{
		RequestID: h.Kernel.IDs().RequestID(),
		Operation: op.Descriptor().ID,
		Scope:     h.Scope,
		Principal: h.principal,
		Origin:    ref.External{Protocol: ref.AWSQuery, Listener: ref.ListenerMain},
		Admitted:  h.Clock.Now(),
		Endpoint:  h.Endpoint,
	})
	return kerneltest.Dispatch[In, Out](h.Context(), h.K, c, in)
}

func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s: %s", op.Descriptor().ID.Name, err.ID.QueryCode, err.Message)
	}
	return out
}

func fails[In, Out any](h *harness, op kernel.Op[In, Out], in *In, code, text string) {
	h.Helper()
	_, err := call(h, op, in)
	switch {
	case err == nil:
		h.Fatalf("%s succeeded, want %s", op.Descriptor().ID.Name, code)
	case err.ID.QueryCode != code || !strings.Contains(err.Message, text):
		h.Fatalf("%s = %s %q, want %s containing %q", op.Descriptor().ID.Name, err.ID.QueryCode, err.Message, code, text)
	}
}

// signWith makes later calls signed with an access key, as the edge
// resolves it.
func (h *harness) signWith(key string) {
	h.principal, _ = h.Identity.Resolve(key)
}

func (h *harness) whoAmI() *stsapi.GetCallerIdentityOutput {
	h.Helper()
	return must(h, stsapi.OpGetCallerIdentity, &stsapi.GetCallerIdentityInput{})
}

func (h *harness) role(name, path string, maxSession int32) iamapi.Role {
	h.Helper()
	return must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: name, Path: new(path), AssumeRolePolicyDocument: trust, MaxSessionDuration: new(maxSession)}).Role
}

func account(h *harness, s string) aws.AccountID {
	h.Helper()
	a, err := aws.ParseAccountID(s)
	if err != nil {
		h.Fatal(err)
	}
	return a
}

func TestGetCallerIdentityOfRoot(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		got := h.whoAmI()
		if *got.Arn != "arn:aws:iam::000000000000:root" || *got.UserId != "000000000000" || *got.Account != "000000000000" {
			t.Errorf("GetCallerIdentity = %s %s %s", *got.Arn, *got.UserId, *got.Account)
		}
		h.signWith("123456789012")
		if got := h.whoAmI(); *got.Arn != "arn:aws:iam::123456789012:root" || *got.Account != "123456789012" {
			t.Errorf("GetCallerIdentity with a 12-digit key = %s %s", *got.Arn, *got.Account)
		}
	})
}

func TestGetCallerIdentityOfUser(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		u := must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "Alice", Path: new("/team/")}).User
		key := must(h, iamapi.OpCreateAccessKey, &iamapi.CreateAccessKeyInput{UserName: new("Alice")}).AccessKey
		h.signWith(key.AccessKeyId)
		got := h.whoAmI()
		if *got.Arn != "arn:aws:iam::000000000000:user/team/Alice" || *got.UserId != u.UserId || *got.Account != "000000000000" {
			t.Errorf("GetCallerIdentity = %s %s %s, want the user's ARN and %s", *got.Arn, *got.UserId, *got.Account, u.UserId)
		}
	})
}

func TestGetCallerIdentityUnsigned(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		h.principal = identity.Anonymous{}
		fails(h, stsapi.OpGetCallerIdentity, &stsapi.GetCallerIdentityInput{}, "MissingAuthenticationToken", "")
	})
}

// An unsigned AssumeRole is refused before the role is looked at, even
// when the role exists.
func TestAssumeRoleUnsigned(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		role := h.role("r", "/", 3600)
		h.principal = identity.Anonymous{}
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "s1"}, "MissingAuthenticationToken", "")
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: "arn:aws:iam::000000000000:role/ghost", RoleSessionName: "s1"}, "MissingAuthenticationToken", "")
	})
}

// The iam.assumeRole switch reports what backs the configured behavior:
// AWS's documentation for strict, nothing for lenient.
func TestAssumeRoleSwitchLabel(t *testing.T) {
	for _, tc := range []struct {
		opts  sts.Options
		value string
		label evidence.Label
	}{
		{sts.Options{}, "strict", evidence.AWSDoc},
		{sts.Options{AssumeRole: sts.RoleLenient}, "lenient", evidence.Inferred},
	} {
		kerneltest.Run(t, func(k *kerneltest.K) {
			got := sts.New(k.Deps(stsapi.Service()), tc.opts).Switches()
			if len(got) != 1 || got[0].Name != "iam.assumeRole" || got[0].Value != tc.value || got[0].Label != tc.label {
				t.Errorf("Switches() with %s = %+v, want iam.assumeRole %s labeled %s", tc.value, got, tc.value, tc.label)
			}
		}, kerneltest.Services(stsapi.Service()))
	}
}

// AssumeRole mints ASIA credentials; GetCallerIdentity signed with them
// renders the assumed-role ARN, with the role name and no path.
func TestAssumeRoleThenGetCallerIdentity(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		role := h.role("demo", "/svc/", 3600)
		out := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "TestAR"})
		creds := out.Credentials
		if !strings.HasPrefix(creds.AccessKeyId, "ASIA") || len(creds.AccessKeyId) != 20 || len(creds.SecretAccessKey) != 40 || creds.SessionToken == "" {
			t.Errorf("Credentials = %s, %d-character secret, token %q", creds.AccessKeyId, len(creds.SecretAccessKey), creds.SessionToken)
		}
		if want := h.Clock.Wall().Add(time.Hour); !creds.Expiration.Equal(want) {
			t.Errorf("Expiration = %v, want wall time plus an hour, %v", creds.Expiration, want)
		}
		const arn = "arn:aws:sts::000000000000:assumed-role/demo/TestAR"
		if out.AssumedRoleUser.Arn != arn || out.AssumedRoleUser.AssumedRoleId != role.RoleId+":TestAR" || out.SourceIdentity != nil {
			t.Errorf("AssumedRoleUser = %+v, SourceIdentity %v", out.AssumedRoleUser, out.SourceIdentity)
		}

		h.signWith(creds.AccessKeyId)
		got := h.whoAmI()
		if *got.Arn != arn || *got.UserId != role.RoleId+":TestAR" || *got.Account != "000000000000" {
			t.Errorf("GetCallerIdentity with the session key = %s %s %s", *got.Arn, *got.UserId, *got.Account)
		}
	})
}

// STS keeps no state: the identity store holds the sessions it mints, so a
// snapshot after AssumeRole has no STS section, and a snapshot that claims
// one is refused.
func TestSnapshotHoldsNoSTSState(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		role := h.role("r", "/", 3600)
		must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "s1"})
		sections, err := h.svc.Export()
		if err != nil || len(sections) != 0 {
			t.Errorf("Export after AssumeRole = %v, %v; want no sections", sections, err)
		}
		if scopes := h.svc.Scopes(); len(scopes) != 0 {
			t.Errorf("Scopes after AssumeRole = %v, want none", scopes)
		}
		if err := h.svc.Import(nil); err != nil {
			t.Errorf("Import(nil) = %v, want nil", err)
		}
		const path = "000000000000.json"
		switch err := h.svc.Import([]store.Section{{Path: path, Version: 1, Data: []byte("{}")}}); {
		case err == nil:
			t.Error("Import of an STS section succeeded, want an error")
		case !strings.Contains(err.Error(), path):
			t.Errorf("Import of an STS section = %q, want the refused path %s named", err, path)
		}
	})
}

func TestAssumeRoleOfAMissingRole(t *testing.T) {
	const missing = "arn:aws:iam::000000000000:role/ghost"
	run(t, sts.Options{}, func(h *harness) {
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: missing, RoleSessionName: "s1"}, "AccessDenied",
			"User: arn:aws:iam::000000000000:root is not authorized to perform: sts:AssumeRole on resource: "+missing)
		// The path is part of the role's identity.
		h.role("pathed", "/a/", 3600)
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: "arn:aws:iam::000000000000:role/pathed", RoleSessionName: "s1"}, "AccessDenied", "")
	})
	run(t, sts.Options{AssumeRole: sts.RoleLenient}, func(h *harness) {
		out := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: missing, RoleSessionName: "s1", DurationSeconds: new(int32(43200))})
		again := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: missing, RoleSessionName: "s1"})
		id := out.AssumedRoleUser.AssumedRoleId
		if !strings.HasPrefix(id, "AROA") || id != again.AssumedRoleUser.AssumedRoleId || out.AssumedRoleUser.Arn != "arn:aws:sts::000000000000:assumed-role/ghost/s1" {
			t.Errorf("lenient AssumedRoleUser = %+v then %+v", out.AssumedRoleUser, again.AssumedRoleUser)
		}
	})
}

func TestAssumeRoleDurations(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		role := h.role("r", "/", 7200)
		got := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "s1", DurationSeconds: new(int32(7200))})
		if want := h.Clock.Wall().Add(2 * time.Hour); !got.Credentials.Expiration.Equal(want) {
			t.Errorf("Expiration = %v, want %v", got.Credentials.Expiration, want)
		}
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "s1", DurationSeconds: new(int32(7201))},
			"ValidationError", "The requested DurationSeconds exceeds the MaxSessionDuration set for this role.")
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "s1", DurationSeconds: new(int32(43201))},
			"ValidationError", "Value '43201' at 'durationSeconds' failed to satisfy constraint: Member must have value less than or equal to 43200")
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "s1", DurationSeconds: new(int32(899))},
			"ValidationError", "greater than or equal to 900")

		// Role chaining: a role session assumes a role for at most an hour.
		h.signWith(got.Credentials.AccessKeyId)
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "s1", DurationSeconds: new(int32(3601))},
			"ValidationError", "1 hour session limit for roles assumed by role chaining")
		chained := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "chained"})
		if chained.AssumedRoleUser.Arn != "arn:aws:sts::000000000000:assumed-role/r/chained" {
			t.Errorf("chained session = %s", chained.AssumedRoleUser.Arn)
		}
	})
}

func TestAssumeRoleValidation(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		role := h.role("r", "/", 3600)
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "Session1:2"}, "ValidationError",
			`1 validation error detected: Value 'Session1:2' at 'roleSessionName' failed to satisfy constraint: Member must satisfy regular expression pattern: [\w+=,.@-]*`)
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: "arn::b:::something/test-role", RoleSessionName: "s1"}, "ValidationError", "arn::b:::something/test-role is invalid")
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: "arn:aws:iam::000000000000:user/u", RoleSessionName: "s1"}, "ValidationError", "is invalid")
		out := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "s1", SourceIdentity: new("Alice")})
		if out.SourceIdentity == nil || *out.SourceIdentity != "Alice" {
			t.Errorf("SourceIdentity = %v, want Alice echoed", out.SourceIdentity)
		}
	})
}

// A role of another account is assumed in that account: the session key
// encodes it, and the session acts there.
func TestAssumeRoleAcrossAccounts(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		home := h.Scope.Account
		h.Scope.Account = account(h, "111111111111")
		h.principal = identity.Root{Account: h.Scope.Account}
		role := h.role("cross", "/", 3600)
		h.Scope.Account = home
		h.principal = identity.Root{Account: home}

		out := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "s1"})
		if got, ok := ids.DecodeAccount(out.Credentials.AccessKeyId); !ok || got != "111111111111" {
			t.Errorf("DecodeAccount(session key) = %s, %v", got, ok)
		}
		h.signWith(out.Credentials.AccessKeyId)
		if got := h.whoAmI(); *got.Account != "111111111111" || *got.Arn != "arn:aws:sts::111111111111:assumed-role/cross/s1" {
			t.Errorf("GetCallerIdentity = %s %s", *got.Account, *got.Arn)
		}
	})
}

func TestGetSessionToken(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		// A root session lasts at most an hour, however long it asks for.
		root := must(h, stsapi.OpGetSessionToken, &stsapi.GetSessionTokenInput{DurationSeconds: new(int32(7200))}).Credentials
		if want := h.Clock.Wall().Add(time.Hour); !root.Expiration.Equal(want) || !strings.HasPrefix(root.AccessKeyId, "ASIA") {
			t.Errorf("root session = %s until %v, want ASIA until %v", root.AccessKeyId, root.Expiration, want)
		}
		h.signWith(root.AccessKeyId)
		if got := h.whoAmI(); *got.Arn != "arn:aws:iam::000000000000:root" {
			t.Errorf("GetCallerIdentity with a root session = %s", *got.Arn)
		}

		h.principal = identity.Root{Account: aws.DefaultAccount()}
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		key := must(h, iamapi.OpCreateAccessKey, &iamapi.CreateAccessKeyInput{UserName: new("u")}).AccessKey
		h.signWith(key.AccessKeyId)
		user := must(h, stsapi.OpGetSessionToken, &stsapi.GetSessionTokenInput{}).Credentials
		if want := h.Clock.Wall().Add(12 * time.Hour); !user.Expiration.Equal(want) {
			t.Errorf("user session until %v, want twelve hours, %v", user.Expiration, want)
		}
		h.signWith(user.AccessKeyId)
		if got := h.whoAmI(); *got.Arn != "arn:aws:iam::000000000000:user/u" {
			t.Errorf("GetCallerIdentity with a user session = %s", *got.Arn)
		}

		h.principal = identity.Root{Account: aws.DefaultAccount()}
		role := h.role("r", "/", 3600)
		session := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "s1"}).Credentials
		h.signWith(session.AccessKeyId)
		fails(h, stsapi.OpGetSessionToken, &stsapi.GetSessionTokenInput{}, "AccessDenied", "session credentials")
	})
}
