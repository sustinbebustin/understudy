//go:build smoke && !docker

package smoke_test

import "testing"

// dockerTests is false without the docker tag: the emulator runs with
// Docker disabled, the workload creates the function but does not invoke
// it, and DynamoDB answers 501.
const dockerTests = false

// containerInstance is never reached without the docker tag, where the
// function is not invoked; it fails the test if it is.
func containerInstance(t *testing.T, id string) string {
	t.Helper()
	t.Fatalf("container %s cannot be inspected without the docker tag", id)
	return ""
}

// assertDrained has nothing to check without the docker tag: the emulator
// starts no container.
func assertDrained(*testing.T, string) {}
