package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// keyAccount is the account the tests act in: a numeric access key names it.
const keyAccount = "111111111111"

// An access key IAM minted keeps resolving to its user across a --persist
// restart, so the calls signed with it stay in the user's account.
func TestRestartKeepsMintedAccessKeys(t *testing.T) {
	cfg := Config{Persist: PersistDir{Path: t.TempDir()}, Docker: DockerDisabled{}, LogOutput: io.Discard}
	first, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	root := auth.Signer{AccessKeyID: keyAccount, SecretAccessKey: "test"}
	queryCall(t, first, "iam", root, url.Values{"Action": {"CreateUser"}, "UserName": {"u1"}})
	created := queryCall(t, first, "iam", root, url.Values{"Action": {"CreateAccessKey"}, "UserName": {"u1"}})
	user := auth.Signer{AccessKeyID: xmlElement(t, created, "AccessKeyId"), SecretAccessKey: xmlElement(t, created, "SecretAccessKey")}
	const want = "arn:aws:iam::" + keyAccount + ":user/u1"
	if got := callerARN(t, first, user); got != want {
		t.Fatalf("GetCallerIdentity before the restart = %s, want %s", got, want)
	}
	if err := first.wired.persist.checkpoint(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	if got := callerARN(t, second, user); got != want {
		t.Errorf("GetCallerIdentity after the restart = %s, want %s", got, want)
	}
}

// An AssumeRole session survives a snapshot loaded into a fresh instance:
// the session's key resolves to the assumed role in its account.
func TestSnapshotKeepsAssumeRoleSessions(t *testing.T) {
	first, err := New(Config{Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	root := auth.Signer{AccessKeyID: keyAccount, SecretAccessKey: "test"}
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + keyAccount + `:root"},"Action":"sts:AssumeRole"}]}`
	queryCall(t, first, "iam", root, url.Values{"Action": {"CreateRole"}, "RoleName": {"r1"}, "AssumeRolePolicyDocument": {trust}})
	assumed := queryCall(t, first, "sts", root, url.Values{
		"Action": {"AssumeRole"}, "RoleArn": {"arn:aws:iam::" + keyAccount + ":role/r1"}, "RoleSessionName": {"s1"},
	})
	session := auth.Signer{
		AccessKeyID:     xmlElement(t, assumed, "AccessKeyId"),
		SecretAccessKey: xmlElement(t, assumed, "SecretAccessKey"),
		SessionToken:    xmlElement(t, assumed, "SessionToken"),
	}
	const want = "arn:aws:sts::" + keyAccount + ":assumed-role/r1/s1"
	if got := callerARN(t, first, session); got != want {
		t.Fatalf("GetCallerIdentity before the save = %s, want %s", got, want)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566"+admin.Prefix+"/snapshots", http.NoBody)
	rec := httptest.NewRecorder()
	first.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /snapshots = %d %s", rec.Code, rec.Body)
	}
	archive := filepath.Join(t.TempDir(), "fixture.tar")
	if err := os.WriteFile(archive, rec.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := New(Config{LoadSnapshot: archive, Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	if got := callerARN(t, second, session); got != want {
		t.Errorf("GetCallerIdentity after the load = %s, want %s", got, want)
	}
}

// queryCall sends one awsQuery request for service, signed by s in
// us-east-1, to srv's handler and returns the body, failing unless it
// succeeds.
func queryCall(t *testing.T, srv *Server, service string, s auth.Signer, form url.Values) string {
	t.Helper()
	versions := map[string]string{"iam": "2010-05-08", "sts": "2011-06-15"}
	form.Set("Version", versions[service])
	body := form.Encode()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.Region, s.Service = aws.DefaultRegion(), service
	auth.Sign(req, []byte(body), s, time.Now())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s = %d %s", service, form.Get("Action"), rec.Code, rec.Body)
	}
	return rec.Body.String()
}

// callerARN is the Arn GetCallerIdentity answers for a call signed by s.
func callerARN(t *testing.T, srv *Server, s auth.Signer) string {
	t.Helper()
	return xmlElement(t, queryCall(t, srv, "sts", s, url.Values{"Action": {"GetCallerIdentity"}}), "Arn")
}

// xmlElement returns the text of the first <name> element of an XML answer.
func xmlElement(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile("<" + name + ">([^<]*)</" + name + ">").FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no <%s> in %s", name, body)
	}
	return m[1]
}
