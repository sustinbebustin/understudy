//go:build smoke && docker

package smoke_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dockerTests is true under the docker tag: the workload invokes the zip
// function, DynamoDB lists tables when its licence is accepted, and the
// workload also runs against the Docker image.
const dockerTests = true

// socket is the Docker daemon's socket the in-container run mounts.
const socket = "/var/run/docker.sock"

// docker runs the docker CLI and returns its trimmed stdout.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		stderr := ""
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return strings.TrimSpace(string(out))
}

// containerInstance is the understudy.dev/instance label of container id.
func containerInstance(t *testing.T, id string) string {
	t.Helper()
	instance := docker(t, "inspect", "--format", `{{index .Config.Labels "understudy.dev/instance"}}`, id)
	if instance == "" || instance == "<no value>" {
		t.Fatalf("the function's container %s carries no understudy.dev/instance label", id)
	}
	return instance
}

// assertDrained fails the test when a container of the stopped emulator
// instance, a function environment or DynamoDB Local, outlived its
// graceful stop, and removes it. An empty instance means the workload
// stopped before it invoked the function, so nothing is checked.
func assertDrained(t *testing.T, instance string) {
	t.Helper()
	if instance == "" {
		return
	}
	left := docker(t, "ps", "--all", "--filter", "label=understudy.dev/instance="+instance, "--format", "{{.Names}}")
	if left == "" {
		return
	}
	t.Errorf("containers of understudy instance %s outlived its graceful stop:\n%s", instance, left)
	docker(t, append([]string{"rm", "--force", "--volumes"}, strings.Fields(left)...)...)
}

// TestSmokeInContainer builds the image and runs the workload against
// understudy inside it, with the Docker socket mounted: the InContainer
// topology, where function containers join understudy's network.
func TestSmokeInContainer(t *testing.T) {
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("the in-container run mounts %s, which is not there (%v)", socket, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s has no unix ownership", socket)
	}
	id := strings.ToLower(rand.Text()[:10])
	tag := "understudy:smoke-" + id
	docker(t, "build", "--label", "understudy.dev/test=1", "-t", tag, "../..")
	t.Cleanup(func() { docker(t, "image", "rm", "--force", tag) })

	name := "understudy-smoke-" + id
	args := []string{
		"run", "--detach", "--name", name, "--label", "understudy.dev/test=1",
		"--publish", "127.0.0.1::4566",
		"--volume", socket + ":" + socket, "--group-add", strconv.FormatUint(uint64(st.Gid), 10),
	}
	accepted := os.Getenv(acceptEnv) == "1"
	if accepted {
		args = append(args, "--env", acceptEnv+"=1")
	}
	w := &workload{invoke: true, ddbAccepted: accepted}
	docker(t, append(args, tag, "serve", "--clock", "frozen", "--seed", "1")...)
	t.Cleanup(func() {
		// SIGTERM drains the function environments and DynamoDB Local; the
		// logs help when a step failed.
		docker(t, "stop", "--time", "60", name)
		assertDrained(t, w.instance)
		if t.Failed() {
			t.Logf("understudy's output in the container:\n%s", docker(t, "logs", name))
		}
		docker(t, "rm", "--force", "--volumes", name)
	})
	addr := docker(t, "port", name, "4566/tcp")
	w.emu = &emulator{endpoint: "http://" + strings.Split(addr, "\n")[0], client: &http.Client{Timeout: 3 * time.Minute}}
	awaitHealth(t, w.emu)
	w.run(t)
}
