package alarm_test

import (
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch"
)

var (
	switchesOnce   sync.Once
	switchesByName map[string]fidelity.Entry
)

// switches are the fidelity switches the cloudwatch service registers, by
// name. The pure package registers none itself, so its tests read the
// service's (cloudwatch-alarms.md 12.2).
func switches(t *testing.T) map[string]fidelity.Entry {
	t.Helper()
	switchesOnce.Do(func() {
		kerneltest.Run(t, func(k *kerneltest.K) {
			svc := cloudwatch.New(k.Deps(cloudwatchapi.Service()), cloudwatch.Options{})
			switchesByName = map[string]fidelity.Entry{}
			for _, e := range svc.Switches() {
				switchesByName[e.Name] = e
			}
		}, kerneltest.Services(cloudwatchapi.Service()))
	})
	return switchesByName
}

// switchCase reads switch name and returns the expectation the test holds
// for its value. A behavior a switch governs is UNVERIFIED or inferred, so
// a test never hard-codes it: it names the switch and follows its value,
// and fails when the switch is missing or takes a value the test has no
// expectation for.
func switchCase[T any](t *testing.T, name string, cases map[string]T) T {
	t.Helper()
	e, ok := switches(t)[name]
	if !ok {
		t.Fatalf("the cloudwatch service registers no switch %s (cloudwatch-alarms.md 12.2)", name)
	}
	want, ok := cases[e.Value]
	if !ok {
		t.Fatalf("switch %s = %q, a value this test has no expectation for", name, e.Value)
	}
	return want
}

// followsSwitch fails unless switch name has value; the assertions after
// it hold for that value only.
func followsSwitch(t *testing.T, name, value string) {
	t.Helper()
	switchCase(t, name, map[string]struct{}{value: {}})
}
