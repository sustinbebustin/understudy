package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/config"
)

// The SDKs env prints variables for.
const (
	sdkGo        = "go"
	sdkPython    = "python"
	sdkJS        = "js"
	sdkCLI       = "cli"
	sdkTerraform = "terraform"
)

// envResult is what env prints: the variables, and notes on the settings
// that have no variable.
type envResult struct {
	Variables []envVar `json:"variables"`
	Notes     []string `json:"notes,omitempty"`
}

type envVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// envFlags are the flags of env.
type envFlags struct {
	sdk, format, region string
	tls                 bool
	tlsEndpoint, caFile string
}

func envCmd() *command {
	return leaf("env", "Print the environment that points an SDK, the AWS CLI or Terraform at understudy",
		"env [--sdk go|python|js|cli|terraform] [--format sh|fish|dotenv|github] [--region R] [--tls [--tls-endpoint URL] [--ca-file PATH]]",
		[]string{
			`eval "$(understudy env)"`,
			"understudy env --sdk python --format dotenv > .env",
			"understudy env --sdk terraform --format github >> \"$GITHUB_ENV\"",
			"understudy env --sdk go --format fish | source",
			`eval "$(understudy env --sdk go --tls)"`,
		},
		func(fs *flag.FlagSet) action {
			var f envFlags
			fs.StringVar(&f.sdk, "sdk", "", "go, python, js, cli or terraform (default the variables every one of them reads)")
			fs.StringVar(&f.format, "format", "sh", "sh, fish, dotenv, or github for $GITHUB_ENV")
			fs.StringVar(&f.region, "region", "us-east-1", "the region the clients sign for")
			fs.BoolVar(&f.tls, "tls", false, "point the clients at the TLS listener of 'serve --tls', trusting its self-signed certificate through AWS_CA_BUNDLE (and NODE_EXTRA_CA_CERTS for JS)")
			fs.StringVar(&f.tlsEndpoint, "tls-endpoint", "", "the TLS listener's URL, as the banner and the ready file's tlsEndpoint name it; implies --tls (default the endpoint's host at port 4567, serve --tls's default)")
			fs.StringVar(&f.caFile, "ca-file", "", "where to write the TLS listener's certificate (default understudy/ca-<port>.pem in the user cache directory)")
			return func(ctx context.Context, r *runner, args []string) error {
				if err := wantArgs(args, 0, "no arguments"); err != nil {
					return err
				}
				return r.envCommand(ctx, f)
			}
		})
}

func (r *runner) envCommand(ctx context.Context, f envFlags) error {
	if f.sdk != "" && !slices.Contains([]string{sdkGo, sdkPython, sdkJS, sdkCLI, sdkTerraform}, f.sdk) {
		return &usageError{msg: fmt.Sprintf("--sdk %q is not go, python, js, cli or terraform", f.sdk)}
	}
	if !slices.Contains([]string{"sh", "fish", "dotenv", "github"}, f.format) {
		return &usageError{msg: fmt.Sprintf("--format %q is not sh, fish, dotenv or github", f.format)}
	}
	res := sdkEnv(sdkEndpoint(r.endpointURL()), f.sdk, f.region)
	if f.tls || f.tlsEndpoint != "" || f.caFile != "" {
		trust, err := r.trustTLS(ctx, f)
		if err != nil {
			return err
		}
		res.withTLS(trust, f.sdk)
	}
	if note := r.dynamoDBNote(ctx); note != "" {
		res.Notes = append(res.Notes, note)
	}
	return r.emit(res, func(w io.Writer) error { return r.writeEnv(w, res, f.format) })
}

// tlsTrust is a TLS listener and the file holding the certificate that
// trusts it.
type tlsTrust struct {
	endpoint string
	caFile   string
}

// withTLS points the environment at the TLS listener and adds the
// variables that trust its certificate: AWS_CA_BUNDLE, which the Go and
// Python SDKs, the AWS CLI and Terraform read, and NODE_EXTRA_CA_CERTS for
// JS, whose SDK does not read AWS_CA_BUNDLE.
func (res *envResult) withTLS(t tlsTrust, sdk string) {
	for i := range res.Variables {
		if res.Variables[i].Name == "AWS_ENDPOINT_URL" {
			res.Variables[i].Value = t.endpoint
		}
	}
	if sdk != sdkJS {
		res.Variables = append(res.Variables, envVar{"AWS_CA_BUNDLE", t.caFile})
	}
	if sdk == "" || sdk == sdkJS {
		res.Variables = append(res.Variables, envVar{"NODE_EXTRA_CA_CERTS", t.caFile})
	}
}

// trustTLS fetches the certificate the TLS listener presents, checks that
// it is a self-signed certificate valid for the listener's host, as serve
// --tls generates, and writes it as PEM.
func (r *runner) trustTLS(ctx context.Context, f envFlags) (tlsTrust, error) {
	endpoint := f.tlsEndpoint
	if endpoint == "" {
		u, err := url.Parse(sdkEndpoint(r.endpointURL()))
		if err != nil {
			return tlsTrust{}, &usageError{msg: fmt.Sprintf("the endpoint %q is not a URL: %v", r.endpointURL(), err)}
		}
		endpoint = "https://" + net.JoinHostPort(u.Hostname(), strconv.Itoa(config.DefaultTLSPort))
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return tlsTrust{}, &usageError{msg: fmt.Sprintf("--tls-endpoint %q is not an https:// URL", endpoint)}
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	cert, err := peerCertificate(ctx, u.Hostname(), port)
	if err != nil {
		return tlsTrust{}, err
	}
	caFile := f.caFile
	if caFile == "" {
		// The user's cache directory, not the shared temporary one, where
		// another user could plant a link at a predictable name.
		cache, err := os.UserCacheDir()
		if err == nil {
			err = os.MkdirAll(filepath.Join(cache, "understudy"), 0o700)
		}
		if err != nil {
			return tlsTrust{}, &localError{op: "finding a directory for the certificate", err: err, next: "pass a writable path with --ca-file"}
		}
		caFile = filepath.Join(cache, "understudy", "ca-"+port+".pem")
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(caFile, data, 0o600); err != nil {
		return tlsTrust{}, &localError{op: "writing the certificate to " + caFile, err: err, next: "pass a writable path with --ca-file"}
	}
	return tlsTrust{endpoint: strings.TrimSuffix(u.String(), "/"), caFile: caFile}, nil
}

// peerCertificate is the certificate the TLS listener at host:port
// presents, when it is self-signed and valid for host. It is read without
// verification, since trusting it is the point; nothing is sent.
func peerCertificate(ctx context.Context, host, port string) (*x509.Certificate, error) {
	quick, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}} //nolint:gosec // the certificate is checked below: env trusts only a self-signed one valid for host
	addr := net.JoinHostPort(host, port)
	conn, err := d.DialContext(quick, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("no TLS listener answers at %s; start understudy with 'understudy serve --tls', or pass the listener's URL with --tls-endpoint: %w",
			addr, &admin.UnreachableError{Endpoint: "https://" + addr, Err: err})
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return nil, errors.Join(fmt.Errorf("the TLS dialer returned a %T for %s, not a TLS connection", conn, addr), conn.Close())
	}
	state := tlsConn.ConnectionState()
	if err := conn.Close(); err != nil {
		return nil, fmt.Errorf("closing the connection to the TLS listener at %s: %w", addr, err)
	}
	if len(state.PeerCertificates) == 0 {
		return nil, fmt.Errorf("the TLS listener at %s:%s presented no certificate", host, port)
	}
	cert := state.PeerCertificates[0]
	if !cert.IsCA || cert.CheckSignatureFrom(cert) != nil {
		return nil, fmt.Errorf("the TLS listener at %s:%s presents a certificate that is not self-signed, so it is not understudy's; check --tls-endpoint", host, port)
	}
	if err := cert.VerifyHostname(host); err != nil {
		return nil, fmt.Errorf("the certificate of the TLS listener at %s:%s does not cover %s (%w); use localhost or 127.0.0.1 in --tls-endpoint", host, port, host, err)
	}
	return cert, nil
}

// sdkEndpoint is endpoint as clients should dial it (ADR 0013): a loopback
// IP literal (127.0.0.1, [::1]) becomes localhost, keeping the scheme and
// port. Clients build hosts under the endpoint's host, such as
// <bucket>.localhost and, for Terraform's S3 Control reads,
// <account>.localhost, which an IP literal cannot carry; an IP literal
// also silently forces path-style S3 in the Go and JS SDKs. Any other
// host, and anything that is not a URL, is kept as given.
func sdkEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() {
		return endpoint
	}
	host := "localhost"
	if port := u.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	}
	u.Host = host
	return u.String()
}

// sdkEnv is the environment for sdk (every SDK when empty), as
// sigv4-checksums-endpoints.md 5 and ADR 0013 give it: AWS_ENDPOINT_URL
// routes every service, the test keys resolve to account 000000000000, and
// the S3 addressing knobs that have no variable become notes.
func sdkEnv(endpoint, sdk, region string) envResult {
	res := envResult{Variables: []envVar{
		{"AWS_ENDPOINT_URL", endpoint},
		{"AWS_ACCESS_KEY_ID", "test"},
		{"AWS_SECRET_ACCESS_KEY", "test"},
		{"AWS_REGION", region},
	}}
	if sdk == "" || sdk == sdkPython || sdk == sdkCLI || sdk == sdkTerraform {
		res.Variables = append(res.Variables, envVar{"AWS_DEFAULT_REGION", region})
	}
	pathStyle := "unless *.localhost resolves here ('understudy doctor' checks)"
	notes := map[string]string{
		sdkGo:        "Go: S3 path-style addressing has no variable; set o.UsePathStyle = true in s3.NewFromConfig " + pathStyle,
		sdkJS:        "JS: S3 path-style addressing has no variable; pass forcePathStyle: true to S3Client " + pathStyle,
		sdkPython:    "boto3 uses path-style S3 addressing with a custom endpoint by default",
		sdkCLI:       "the AWS CLI uses path-style S3 addressing with a custom endpoint by default",
		sdkTerraform: "Terraform: set s3_use_path_style = true and skip_requesting_account_id = true in the aws provider " + pathStyle,
	}
	for _, s := range []string{sdkGo, sdkJS, sdkPython, sdkCLI, sdkTerraform} {
		if sdk == "" || sdk == s {
			res.Notes = append(res.Notes, notes[s])
		}
	}
	return res
}

// dynamoDBNote is the server's DynamoDB Local line when the service waits
// for its licence; empty when no server answers quickly or it runs.
func (r *runner) dynamoDBNote(ctx context.Context) string {
	c, err := r.client()
	if err != nil {
		return ""
	}
	quick, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	h, err := c.Health(quick)
	if err != nil || h.DynamoDBLocal.Status != admin.Disabled {
		return ""
	}
	return "DynamoDB: " + h.DynamoDBLocal.Reason
}

// writeEnv writes the variables in format; the github format holds only
// assignments, so its notes go to stderr.
func (r *runner) writeEnv(w io.Writer, res envResult, format string) error {
	var b, notes strings.Builder
	for _, n := range res.Notes {
		notes.WriteString("# " + n + "\n")
	}
	if format != "github" {
		b.WriteString(notes.String())
	}
	for _, v := range res.Variables {
		switch format {
		case "fish":
			fmt.Fprintf(&b, "set -gx %s %s\n", v.Name, shellQuote(v.Value))
		case "dotenv", "github":
			fmt.Fprintf(&b, "%s=%s\n", v.Name, v.Value)
		default:
			fmt.Fprintf(&b, "export %s=%s\n", v.Name, shellQuote(v.Value))
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	if format == "github" {
		_, err := io.WriteString(r.o.Stderr, notes.String())
		return err
	}
	return nil
}

// shellQuote single-quotes s for sh and fish.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
