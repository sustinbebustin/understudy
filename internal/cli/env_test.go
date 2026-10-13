package cli_test

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// env --tls trusts the TLS listener of serve --tls: the CA file it writes
// verifies the listener, and each SDK gets the variable it reads.
func TestEnvTLS(t *testing.T) {
	s := serve(t, "--tls")
	defer s.stop()
	if s.tlsEndpoint == "" {
		t.Fatalf("serve --tls wrote no tlsEndpoint:\n%s", s.stderr)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	r := runCLI(t.Context(), s.endpoint, nil, "env", "--sdk", "go", "--format", "dotenv", "--tls-endpoint", s.tlsEndpoint, "--ca-file", ca)
	got := stripComments(r.stdout)
	if r.code != admin.ExitOK || !strings.Contains(got, "AWS_ENDPOINT_URL="+s.tlsEndpoint+"\n") || !strings.Contains(got, "AWS_CA_BUNDLE="+ca+"\n") {
		t.Fatalf("env --tls --sdk go = %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	pemData, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemData) {
		t.Fatalf("%s holds no certificate: %q", ca, pemData)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.tlsEndpoint+admin.Prefix+"/health", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET health over TLS trusting only %s: %v", ca, err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	js := runCLI(t.Context(), s.endpoint, nil, "env", "--sdk", "js", "--format", "dotenv", "--tls-endpoint", s.tlsEndpoint, "--ca-file", ca)
	if !strings.Contains(js.stdout, "NODE_EXTRA_CA_CERTS="+ca+"\n") || strings.Contains(js.stdout, "AWS_CA_BUNDLE") {
		t.Errorf("env --tls --sdk js:\n%s", js.stdout)
	}
	plain := runCLI(t.Context(), s.endpoint, nil, "env", "--tls-endpoint", s.endpoint)
	if plain.code != admin.ExitUsage {
		t.Errorf("env --tls-endpoint http://... = %d, want 2", plain.code)
	}
}

// env prints the exact variables an SDK reads, in each format.
func TestEnvFormats(t *testing.T) {
	endpoint := closedEndpoint(t)
	// The loopback IP literal becomes localhost, the endpoint ADR 0013
	// documents, so clients can address <bucket>.localhost and
	// <account>.localhost.
	sdk := "http://localhost:" + endpoint[strings.LastIndexByte(endpoint, ':')+1:]
	cases := map[string]string{
		"sh":     "export AWS_ENDPOINT_URL='" + sdk + "'\nexport AWS_ACCESS_KEY_ID='test'\nexport AWS_SECRET_ACCESS_KEY='test'\nexport AWS_REGION='eu-west-1'\n",
		"fish":   "set -gx AWS_ENDPOINT_URL '" + sdk + "'\nset -gx AWS_ACCESS_KEY_ID 'test'\nset -gx AWS_SECRET_ACCESS_KEY 'test'\nset -gx AWS_REGION 'eu-west-1'\n",
		"dotenv": "AWS_ENDPOINT_URL=" + sdk + "\nAWS_ACCESS_KEY_ID=test\nAWS_SECRET_ACCESS_KEY=test\nAWS_REGION=eu-west-1\n",
		"github": "AWS_ENDPOINT_URL=" + sdk + "\nAWS_ACCESS_KEY_ID=test\nAWS_SECRET_ACCESS_KEY=test\nAWS_REGION=eu-west-1\n",
	}
	for format, want := range cases {
		r := runCLI(t.Context(), endpoint, nil, "env", "--sdk", "go", "--format", format, "--region", "eu-west-1")
		got := r.stdout
		if format != "github" {
			got = stripComments(got)
		}
		if r.code != admin.ExitOK || got != want {
			t.Errorf("env --format %s = %d\n%s\nwant\n%s", format, r.code, got, want)
		}
		if !strings.Contains(r.stdout+r.stderr, "UsePathStyle") {
			t.Errorf("env --sdk go --format %s does not name UsePathStyle", format)
		}
	}
	r := runCLI(t.Context(), endpoint, nil, "env", "--sdk", "python", "--format", "dotenv")
	if !strings.Contains(r.stdout, "AWS_DEFAULT_REGION=us-east-1\n") {
		t.Errorf("env --sdk python lacks AWS_DEFAULT_REGION:\n%s", r.stdout)
	}
	// Terraform 6.x reads bucket tags through S3 Control at
	// <account>.<endpoint host>, so it needs skip_requesting_account_id
	// where *.localhost does not resolve.
	tf := runCLI(t.Context(), endpoint, nil, "env", "--sdk", "terraform")
	if !strings.Contains(tf.stdout, "skip_requesting_account_id = true") || !strings.Contains(tf.stdout, "s3_use_path_style = true") {
		t.Errorf("env --sdk terraform names neither skip_requesting_account_id nor s3_use_path_style:\n%s", tf.stdout)
	}
	// A hostname endpoint is kept as given.
	named := runCLI(t.Context(), "http://understudy:4566", nil, "env", "--format", "dotenv")
	if !strings.Contains(named.stdout, "AWS_ENDPOINT_URL=http://understudy:4566\n") {
		t.Errorf("env with a hostname endpoint rewrote it:\n%s", named.stdout)
	}
}

// env refuses a --region no region could be, and notes one that is no AWS
// region, which serve accepts only when its configuration file's regions
// list names it.
func TestEnvRegion(t *testing.T) {
	bad := runCLI(t.Context(), closedEndpoint(t), nil, "env", "--region", "Mars_1")
	if bad.code != admin.ExitUsage || !strings.Contains(bad.stderr, "malformed") {
		t.Errorf("env --region Mars_1 = %d, stderr %q; want 2", bad.code, bad.stderr)
	}
	const note = `{"regions": ["mars"]}`
	mars := runCLI(t.Context(), closedEndpoint(t), nil, "env", "--region", "mars")
	if mars.code != admin.ExitOK || !strings.Contains(mars.stdout, "# region \"mars\" is not an AWS region name") || !strings.Contains(mars.stdout, note) {
		t.Errorf("env --region mars = %d:\n%s%s", mars.code, mars.stdout, mars.stderr)
	}
	known := runCLI(t.Context(), closedEndpoint(t), nil, "env", "--region", "eu-west-1")
	if strings.Contains(known.stdout, `"regions"`) {
		t.Errorf("env --region eu-west-1 notes the regions list:\n%s", known.stdout)
	}
}

// The access key id env prints selects the account: --account prints that
// account's 12-digit id, which a call then acts in, and without it env
// notes how to pick one. A value that is no account id is refused.
func TestEnvAccount(t *testing.T) {
	inst := start(t)
	r := runCLI(t.Context(), inst.Endpoint(), nil, "env", "--account", "111111111111", "--format", "dotenv")
	if r.code != admin.ExitOK || !strings.Contains(r.stdout, "AWS_ACCESS_KEY_ID=111111111111\n") || strings.Contains(r.stdout, "--account") {
		t.Errorf("env --account 111111111111 = %d:\n%s%s", r.code, r.stdout, r.stderr)
	}
	plain := runCLI(t.Context(), inst.Endpoint(), nil, "env")
	if !strings.Contains(plain.stdout, "AWS_ACCESS_KEY_ID='test'") || !strings.Contains(plain.stdout, "# the access key id selects the account") {
		t.Errorf("env without --account:\n%s", plain.stdout)
	}
	bad := runCLI(t.Context(), inst.Endpoint(), nil, "env", "--account", "acme")
	if bad.code != admin.ExitUsage || !strings.Contains(bad.stderr, `--account "acme" is not a 12-digit account id`) {
		t.Errorf("env --account acme = %d, stderr %q; want 2", bad.code, bad.stderr)
	}
}

// With DynamoDB Local down for want of Docker, env's note names DynamoDB
// once and Docker as the cause.
func TestEnvDynamoDBNote(t *testing.T) {
	inst := start(t)
	r := runCLI(t.Context(), inst.Endpoint(), nil, "env")
	if r.code != admin.ExitOK || !strings.Contains(r.stdout, "# DynamoDB: needs Docker, which is disabled") || strings.Contains(r.stdout, "DynamoDB: DynamoDB") {
		t.Errorf("env against a server without Docker = %d:\n%s", r.code, r.stdout)
	}
}

func stripComments(s string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		if !strings.HasPrefix(line, "#") {
			b.WriteString(line)
		}
	}
	return b.String()
}
