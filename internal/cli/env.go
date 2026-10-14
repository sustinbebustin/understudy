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
	"github.com/sustinbebustin/understudy/internal/aws"
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
	account             string
	tls                 bool
	tlsEndpoint, caFile string
}

func envCmd() *command {
	return leaf("env", "Print the environment that points an SDK, the AWS CLI or Terraform at understudy",
		"env [--sdk go|python|js|cli|terraform] [--format sh|fish|dotenv|github] [--region R] [--account A] [--tls [--tls-endpoint URL] [--ca-file PATH]]",
		[]string{
			`eval "$(understudy env)"`,
			`eval "$(understudy env --account 111111111111)"`,
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
			fs.StringVar(&f.account, "account", "", "the 12-digit account the clients act in, which their access key id names (default the server's default account, 000000000000 unless accounts.default sets another)")
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
	// A name that is no AWS region is still valid once the regions list of
	// serve's configuration file names it, which env cannot see; it is
	// noted, not refused.
	var regionNote string
	if _, err := aws.KnownRegions().Parse(f.region); err != nil {
		if bad, ok := errors.AsType[*aws.RegionError](err); ok && bad.Malformed {
			return &usageError{msg: "--region: " + err.Error()}
		}
		regionNote = fmt.Sprintf(`region %q is not an AWS region name; understudy accepts it only when serve's configuration file lists it, as in {"regions": [%q]}`, f.region, f.region)
	}
	// The access key id selects the account (ADR 0012): a 12-digit key id
	// is that account, anything else the server's default account.
	accessKey, accountNote := "test", "the access key id selects the account: a 12-digit id acts in that account, any other, such as 'test', in the default account, "+
		"000000000000 unless accounts.default in serve's configuration sets another; 'understudy env --account ID' prints another account's"
	if f.account != "" {
		if _, err := aws.ParseAccountID(f.account); err != nil {
			return &usageError{msg: fmt.Sprintf("--account %q is not a 12-digit account id such as 111111111111", f.account)}
		}
		accessKey, accountNote = f.account, ""
	}
	// The S3 notes depend on the endpoint's host form, so they are chosen
	// for the endpoint env prints: the TLS listener's when one is asked for.
	if f.tls || f.tlsEndpoint != "" || f.caFile != "" {
		trust, err := r.trustTLS(ctx, f)
		if err != nil {
			return err
		}
		res := sdkEnv(trust.endpoint, f.sdk, f.region, accessKey)
		res.trust(trust.caFile, f.sdk)
		return r.emitEnv(ctx, res, f.format, accountNote, regionNote)
	}
	return r.emitEnv(ctx, sdkEnv(r.endpointURL(), f.sdk, f.region, accessKey), f.format, accountNote, regionNote)
}

// emitEnv appends the account, region and DynamoDB notes to res and
// prints it.
func (r *runner) emitEnv(ctx context.Context, res envResult, format string, notes ...string) error {
	for _, note := range notes {
		if note != "" {
			res.Notes = append(res.Notes, note)
		}
	}
	if note := r.dynamoDBNote(ctx); note != "" {
		res.Notes = append(res.Notes, note)
	}
	return r.emit(res, func(w io.Writer) error { return r.writeEnv(w, res, format) })
}

// tlsTrust is a TLS listener and the file holding the certificate that
// trusts it.
type tlsTrust struct {
	endpoint string
	caFile   string
}

// trust adds the variables that trust the TLS listener's certificate in
// caFile: AWS_CA_BUNDLE, which the Go and Python SDKs, the AWS CLI and
// Terraform read, and NODE_EXTRA_CA_CERTS for JS, whose SDK does not read
// AWS_CA_BUNDLE.
func (res *envResult) trust(caFile, sdk string) {
	if sdk != sdkJS {
		res.Variables = append(res.Variables, envVar{"AWS_CA_BUNDLE", caFile})
	}
	if sdk == "" || sdk == sdkJS {
		res.Variables = append(res.Variables, envVar{"NODE_EXTRA_CA_CERTS", caFile})
	}
}

// trustTLS fetches the certificate the TLS listener presents, checks that
// it is a self-signed certificate valid for the listener's host, as serve
// --tls generates, and writes it as PEM.
func (r *runner) trustTLS(ctx context.Context, f envFlags) (tlsTrust, error) {
	endpoint := f.tlsEndpoint
	if endpoint == "" {
		u, err := url.Parse(r.endpointURL())
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
	if !constrainedToLoopback(cert) {
		return nil, fmt.Errorf("the TLS listener at %s:%s presents a self-signed certificate without understudy's name constraints (critical, localhost and loopback addresses only), "+
			"so installing it as a root could let its key vouch for any site; env wrote no CA file. Check --tls-endpoint", host, port)
	}
	return cert, nil
}

// constrainedToLoopback reports whether cert carries exactly the critical
// name constraints of understudy's self-signed certificate: DNS names
// under localhost, and the loopback addresses 127.0.0.0/8 and ::1.
func constrainedToLoopback(cert *x509.Certificate) bool {
	if !cert.PermittedDNSDomainsCritical || !slices.Equal(cert.PermittedDNSDomains, []string{"localhost"}) ||
		len(cert.ExcludedDNSDomains) > 0 || len(cert.ExcludedIPRanges) > 0 {
		return false
	}
	ranges := make([]string, len(cert.PermittedIPRanges))
	for i, r := range cert.PermittedIPRanges {
		ranges[i] = r.String()
	}
	slices.Sort(ranges)
	return slices.Equal(ranges, []string{"127.0.0.0/8", "::1/128"})
}

// isIPLiteral reports whether endpoint is a URL whose host is an IP
// address, such as http://127.0.0.1:4566.
func isIPLiteral(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	_, err = netip.ParseAddr(u.Hostname())
	return err == nil
}

// sdkEnv is the environment for sdk (every SDK when empty), as
// sigv4-checksums-endpoints.md 5 and ADR 0013 give it: AWS_ENDPOINT_URL
// routes every service, accessKey selects the account, and the S3
// addressing knobs that have no variable become notes, chosen for
// endpoint's host form. endpoint is printed as given: the form serve's
// banner announces, or the TLS listener's URL.
func sdkEnv(endpoint, sdk, region, accessKey string) envResult {
	res := envResult{Variables: []envVar{
		{"AWS_ENDPOINT_URL", endpoint},
		{"AWS_ACCESS_KEY_ID", accessKey},
		{"AWS_SECRET_ACCESS_KEY", "test"},
		{"AWS_REGION", region},
	}}
	if sdk == "" || sdk == sdkPython || sdk == sdkCLI || sdk == sdkTerraform {
		res.Variables = append(res.Variables, envVar{"AWS_DEFAULT_REGION", region})
	}
	pathStyle := "unless *.localhost resolves here ('understudy doctor' checks)"
	goNote := "Go: S3 path-style addressing has no variable; set o.UsePathStyle = true in s3.NewFromConfig " + pathStyle
	jsNote := "JS: S3 path-style addressing has no variable; pass forcePathStyle: true to S3Client " + pathStyle
	tfNote := "Terraform: set s3_use_path_style = true and skip_requesting_account_id = true in the aws provider " + pathStyle
	if isIPLiteral(endpoint) {
		// The S3 endpoint rules of the SDKs address an IP-literal endpoint
		// path style, since <bucket>.<IP> is no host name. Terraform's S3
		// Control bucket tag reads go to <account>.<IP>, which never
		// resolves (s3.md finding 1), so only skipping the account id
		// lookup avoids them.
		virtualHosted := "virtual-hosted <bucket>.localhost addressing needs a localhost endpoint and *.localhost resolving here ('understudy doctor' checks)"
		goNote = "Go: the SDK addresses S3 path style at an IP-literal endpoint without o.UsePathStyle; " + virtualHosted
		jsNote = "JS: the SDK addresses S3 path style at an IP-literal endpoint without forcePathStyle; " + virtualHosted
		tfNote = "Terraform: skip_requesting_account_id = true is required in the aws provider at an IP-literal endpoint: " +
			"it reads S3 bucket tags through S3 Control at <account>.<endpoint host>, which cannot resolve for an IP address; " +
			"a host-name endpoint such as localhost, where *.localhost resolves, does not need it; " +
			"s3_use_path_style = true is optional here, since the provider addresses S3 path style at an IP-literal endpoint"
	}
	notes := map[string]string{
		sdkGo:        goNote,
		sdkJS:        jsNote,
		sdkPython:    "boto3 uses path-style S3 addressing with a custom endpoint by default",
		sdkCLI:       "the AWS CLI uses path-style S3 addressing with a custom endpoint by default",
		sdkTerraform: tfNote,
	}
	for _, s := range []string{sdkGo, sdkJS, sdkPython, sdkCLI, sdkTerraform} {
		if sdk == "" || sdk == s {
			res.Notes = append(res.Notes, notes[s])
		}
	}
	return res
}

// dynamoDBNote says why the server has DynamoDB Local disabled: Docker,
// or else its licence, whose line the server gives already naming
// DynamoDB. It is empty when no server answers quickly or DynamoDB Local
// is not disabled.
func (r *runner) dynamoDBNote(ctx context.Context) string {
	c, err := r.client()
	if err != nil {
		return ""
	}
	quick, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	h, err := c.Health(quick)
	switch {
	case err != nil || h.DynamoDBLocal.Status != admin.Disabled:
		return ""
	case h.Docker.Status != admin.Enabled && h.Docker.Reason != "":
		return "DynamoDB: needs Docker, which is " + string(h.Docker.Status) + ": " + h.Docker.Reason
	case h.Docker.Status != admin.Enabled:
		return "DynamoDB: needs Docker, which is " + string(h.Docker.Status)
	}
	// The health reason sits on the dynamodb-local row, so it does not
	// name DynamoDB itself.
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
