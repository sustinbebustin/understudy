//go:build docker

package ddblocal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/config/configtest"
	"github.com/sustinbebustin/understudy/internal/docker"
)

// sinkImage runs the sink binary; any image with a Linux userland would do,
// and this one is what the other Docker tests already pull.
const sinkImage = "public.ecr.aws/lambda/provided:al2023"

// egress is the harness of dynamodb-local-terms.md 5.3 and 7: an --internal
// network with no route off the host and a sink on it whose network
// namespace DynamoDB Local shares. The sink routes every address to that
// namespace, answers every DNS query with its own address, and records
// every DNS query and every TCP SYN or UDP datagram the namespace sends, so
// a connection to any address on any port is seen.
type egress struct {
	t       *testing.T
	d       *docker.Client
	id      string
	labels  docker.Labels
	network string
	bin     string
	sink    string
	ip      string
	resolv  string
}

func newEgress(t *testing.T) *egress {
	t.Helper()
	d := configtest.EngineClient(t)
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(b)
	e := &egress{t: t, d: d, id: id, labels: docker.Labels{Ownership: docker.Ownership{Instance: "t33-egress-" + id, Owner: "t33-egress-" + id}, Test: true}}
	labels, err := e.labels.Map()
	if err != nil {
		t.Fatal(err)
	}
	e.network = "understudy-egress-" + id
	if _, err := d.NetworkCreate(t.Context(), e.network, docker.NetworkOptions{Internal: true, Labels: labels}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.NetworkRemove(context.WithoutCancel(t.Context()), e.network); err != nil && !docker.IsNotFound(err) {
			t.Errorf("removing network %s: %v", e.network, err)
		}
	})
	e.startSink()
	return e
}

// startSink builds the sink as a static Linux binary, runs it on the
// network and writes the resolv.conf that names it.
func (e *egress) startSink() {
	t := e.t
	dir := t.TempDir()
	bin := filepath.Join(dir, "sink")
	e.bin = bin
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./testdata/sink")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the sink: %v\n%s", err, out)
	}
	if _, err := e.d.ImageInspect(t.Context(), sinkImage); docker.IsNotFound(err) {
		err = e.d.ImagePull(t.Context(), sinkImage, docker.Platform{}, nil)
		if err != nil {
			t.Fatalf("pulling %s: %v", sinkImage, err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	id, err := e.createSink("understudy-egress-sink-" + e.id)
	if err != nil {
		t.Fatal(err)
	}
	e.sink = id
	e.removeOnCleanup(id)
	if err := e.d.ContainerStart(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the sink to start", func(out string) bool {
		_, after, ok := strings.Cut(out, "ready ")
		e.ip, _, _ = strings.Cut(after, "\n")
		return ok && strings.Contains(after, "\n")
	})
	e.resolv = filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(e.resolv, []byte("nameserver "+e.ip+"\n"), 0o644); err != nil { //nolint:gosec // the container's resolver reads it as another user
		t.Fatal(err)
	}
}

// sinkCreate is the create request of the sink: docker.HostConfig serves
// understudy's own containers and has no CapAdd, and the sink needs
// NET_ADMIN to route every address to its namespace.
type sinkCreate struct {
	docker.Config
	HostConfig       sinkHostConfig          `json:"HostConfig"`
	NetworkingConfig docker.NetworkingConfig `json:"NetworkingConfig"`
}

type sinkHostConfig struct {
	docker.HostConfig
	CapAdd []string `json:"CapAdd"`
}

// createSink creates the sink through the Engine API directly and returns
// its id.
func (e *egress) createSink(name string) (string, error) {
	labels, err := e.labels.Map()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(sinkCreate{
		Image: sinkImage, Entrypoint: []string{"/sink"}, User: "root", Labels: labels,
		HostConfig: sinkHostConfig{
			HostConfig: docker.HostConfig{
				NetworkMode: e.network,
				Mounts:      []docker.Mount{{Type: "bind", Source: e.bin, Target: "/sink", ReadOnly: true}},
				LogConfig:   docker.LogConfig{Type: "json-file"},
			},
			CapAdd: []string{"NET_ADMIN"},
		},
		NetworkingConfig: docker.NetworkingConfig{EndpointsConfig: map[string]docker.EndpointSettings{e.network: {}}},
	})
	if err != nil {
		return "", err
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", e.d.Socket())
		},
	}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(e.t.Context(), http.MethodPost, "http://docker/containers/create?name="+url.QueryEscape(name), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("creating the sink: %w", err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("creating the sink: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("creating the sink: the daemon answered %d %s", resp.StatusCode, answer)
	}
	var created docker.Created
	if err := json.Unmarshal(answer, &created); err != nil {
		return "", fmt.Errorf("creating the sink: %w", err)
	}
	return created.ID, nil
}

func (e *egress) removeOnCleanup(id string) {
	e.t.Cleanup(func() { e.remove(id) })
}

// remove removes a container now; one already gone is fine.
func (e *egress) remove(id string) {
	err := e.d.ContainerRemove(context.WithoutCancel(e.t.Context()), id, docker.RemoveOptions{Force: true, Volumes: true})
	if err != nil && !docker.IsNotFound(err) {
		e.t.Errorf("removing container %s: %v", id, err)
	}
}

// output is everything the sink has written to stdout.
func (e *egress) output() string {
	s, err := e.d.ContainerLogs(e.t.Context(), e.sink, docker.LogsOptions{})
	if err != nil {
		e.t.Fatal(err)
	}
	defer s.Close()
	var out bytes.Buffer
	for {
		f, err := s.Next()
		if errors.Is(err, io.EOF) {
			return out.String()
		}
		if err != nil {
			e.t.Fatal(err)
		}
		if f.Stream == docker.Stdout {
			out.Write(f.Payload)
		}
	}
}

// recorded is the sink's output after its ready line: one line per DNS
// query, TCP SYN or UDP datagram.
func (e *egress) recorded() []string {
	lines := strings.Split(strings.TrimSpace(e.output()), "\n")
	return slices.DeleteFunc(lines, func(l string) bool { return l == "" || strings.HasPrefix(l, "ready ") })
}

// waitFor polls the sink's output until done accepts it, for a minute.
func (e *egress) waitFor(what string, done func(out string) bool) {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if done(e.output()) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s; the sink wrote:\n%s", what, e.output())
}

// inSinkNamespace creates a container in the sink's network namespace.
func (e *egress) inSinkNamespace(name string, cfg docker.Config, host docker.HostConfig) string {
	t := e.t
	host.NetworkMode = "container:" + e.sink
	created, err := e.d.ContainerCreate(t.Context(), name+"-"+e.id, cfg, host, docker.NetworkingConfig{})
	if err != nil {
		t.Fatal(err)
	}
	e.removeOnCleanup(created.ID)
	if err := e.d.ContainerStart(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

// checkHarness shows the sink records what the test relies on it to see:
// a connection and a datagram to a literal address outside the network,
// on a port nothing listens on.
func (e *egress) checkHarness() {
	labels, err := e.labels.Map()
	if err != nil {
		e.t.Fatal(err)
	}
	const target = "203.0.113.7:4242"
	e.inSinkNamespace("understudy-egress-probe",
		docker.Config{Image: sinkImage, Entrypoint: []string{"/sink", "probe", target}, User: "root", Labels: labels},
		docker.HostConfig{Mounts: []docker.Mount{{Type: "bind", Source: e.bin, Target: "/sink", ReadOnly: true}}, LogConfig: docker.LogConfig{Type: "json-file"}})
	e.waitFor("the sink to record the probe", func(out string) bool {
		return strings.Contains(out, "syn ") && strings.Contains(out, "-> "+target) && strings.Contains(out, "udp ")
	})
}

// runLocal runs the pinned image in the sink's namespace with understudy's
// exact container spec (Manager.spec), the sink as its resolver, and
// adjust applied, waits until it answers and returns the container and
// where it answers.
func (e *egress) runLocal(name string, adjust func(*docker.Config)) (*Manager, string, *url.URL) {
	t := e.t
	m, err := New(Config{Docker: e.d, Licence: Accepted{Digest: Digest}, Labels: e.labels})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.transport.CloseIdleConnections)
	ref, err := ensureImage(t.Context(), e.d)
	if err != nil {
		t.Fatal(err)
	}
	cfg, host, _, err := m.spec(ref)
	if err != nil {
		t.Fatal(err)
	}
	host.Mounts = append(host.Mounts, docker.Mount{Type: "bind", Source: e.resolv, Target: "/etc/resolv.conf", ReadOnly: true})
	adjust(&cfg)
	id := e.inSinkNamespace("understudy-egress-"+name, cfg, host)
	endpoint := &url.URL{Scheme: "http", Host: net.JoinHostPort(e.ip, "8000")}
	if err := m.waitReady(t.Context(), id, endpoint); err != nil {
		t.Fatal(err)
	}
	return m, id, endpoint
}

// cycle drives Local through a table's life, as the brief's lock table
// does: CreateTable, PutItem, Scan.
func cycle(t *testing.T, m *Manager, endpoint *url.URL) {
	t.Helper()
	for _, call := range []struct{ op, body string }{
		{"CreateTable", `{"AttributeDefinitions":[{"AttributeName":"LockID","AttributeType":"S"}],"TableName":"Locks","KeySchema":[{"AttributeName":"LockID","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`},
		{"PutItem", `{"TableName":"Locks","Item":{"LockID":{"S":"a"},"ExpiresAt":{"N":"1"}}}`},
		{"Scan", `{"TableName":"Locks"}`},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint.String()+"/", strings.NewReader(call.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("X-Amz-Target", "DynamoDB_20120810."+call.op)
		req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=111111111111/20261010/us-east-1/dynamodb/aws4_request, SignedHeaders=host, Signature=00")
		resp, err := m.transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		if cerr := resp.Body.Close(); err == nil {
			err = cerr
		}
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d %s %v", call.op, resp.StatusCode, body, err)
		}
	}
}

// TestDynamoDBLocalMakesNoNetworkAttempts is the permanent egress test of
// dynamodb-local-terms.md 5.3: started with understudy's exact arguments,
// the pinned image makes no DNS lookup, no connection and sends no datagram
// to any address over a table cycle and ten idle seconds. The probe shows
// the sink sees an attempt to a literal outside address on any port; the
// control run, without the two telemetry opt-outs, shows it sees Local's
// telemetry, so the silence is Local's and not the harness's.
func TestDynamoDBLocalMakesNoNetworkAttempts(t *testing.T) {
	configtest.DynamoDBLicence(t)
	e := newEgress(t)
	e.checkHarness()
	before := len(e.recorded())

	m, id, endpoint := e.runLocal("understudy", func(*docker.Config) {})
	cycle(t, m, endpoint)
	time.Sleep(10 * time.Second)
	if got := e.recorded()[before:]; len(got) != 0 {
		t.Errorf("DynamoDB Local with understudy's arguments made network attempts:\n%s", strings.Join(got, "\n"))
	}
	e.remove(id)

	e.runLocal("control", func(c *docker.Config) {
		c.Cmd = slices.DeleteFunc(slices.Clone(c.Cmd), func(a string) bool { return a == "-disableTelemetry" })
		c.Env = nil
	})
	e.waitFor("the control run's telemetry lookup", func(out string) bool { return strings.Contains(out, "dns ") })
	got := strings.Join(e.recorded()[before:], "\n")
	t.Logf("the control run's attempts:\n%s", got)
	if !strings.Contains(got, "amazonaws.com") {
		t.Errorf("the control run's attempts do not name amazonaws.com:\n%s", got)
	}
}
