package server_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// CreateEventBus with a KmsKeyIdentifier under each iam.enforce mode
// (testdata/parity/events/EBB/05-missing-key.json, 05-default-policy.json;
// eventbridge-buses.md 3.1): a key KMS does not hold is refused under every
// mode, since the lookup is no authorization; a key whose policy does not
// allow events.amazonaws.com kms:GenerateDataKey is refused under enforce,
// journaled under advise and not asked about under off (policy.md). Calls
// are signed as the account root, which the default key policy allows, so
// only EventBridge's own grant is in question.

// createBusWithKey sends CreateEventBus for the bus name with key as root.
func createBusWithKey(t *testing.T, endpoint, name, key string) wire {
	t.Helper()
	body, err := json.Marshal(map[string]string{"Name": name, "KmsKeyIdentifier": key})
	if err != nil {
		t.Fatal(err)
	}
	return jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents.CreateEventBus", string(body))
}

// bodyMembers decodes a JSON body's top-level string members.
func bodyMembers(t *testing.T, w wire) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(w.body), &m); err != nil {
		t.Fatalf("body %s: %v", w.body, err)
	}
	return m
}

// missingKeyMessage is the recorded refusal of a missing key ARN, its
// request id aside (05-missing-key.json).
var missingKeyMessage = regexp.MustCompile(`^Parameter Key '([^']*)' does not exist \(Service: Kms, Status Code: 400, Request ID: [0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\) \(SDK Attempt Count: 1\) is not valid\.$`)

func TestCreateEventBusRefusesAMissingKeyUnderEveryMode(t *testing.T) {
	const missing = "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000"
	for name, mode := range map[string]server.IAMEnforce{"enforce": server.IAMEnforceEnforce, "advise": server.IAMEnforceAdvise, "off": server.IAMEnforceOff} {
		t.Run(name, func(t *testing.T) {
			_, endpoint, _ := enforced(t, mode)
			w := createBusWithKey(t, endpoint, "k4", missing)
			m := bodyMembers(t, w)
			got := missingKeyMessage.FindStringSubmatch(m["message"])
			if w.status != http.StatusBadRequest || m["__type"] != "ValidationException" || got == nil || got[1] != missing ||
				!slices.Equal(slices.Sorted(maps.Keys(m)), []string{"__type", "message"}) {
				t.Errorf("CreateEventBus with a missing key = %d %s, want the recorded 400 ValidationException naming %s", w.status, w.body, missing)
			}
		})
	}
}

func TestCreateEventBusKeyPolicyFollowsIAMEnforce(t *testing.T) {
	denied := func(key string) string {
		return "Access to call kms:GenerateDataKey is denied. Reason: events.amazonaws.com is not authorized to perform: kms:GenerateDataKey on " + key +
			" because the resource does not exist in this Region, no resource-based policies allow access, or a resource-based policy explicitly denies access"
	}
	t.Run("enforce", func(t *testing.T) {
		_, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
		key := createKey(t, endpoint)
		w := createBusWithKey(t, endpoint, "k0", key)
		want := map[string]string{"__type": "AccessDeniedException", "Message": denied(key)}
		if got := bodyMembers(t, w); w.status != http.StatusBadRequest || !maps.Equal(got, want) {
			t.Errorf("CreateEventBus with a default-policy key = %d %s, want 400 %v", w.status, w.body, want)
		}
		allowing := keyAllowing("events.amazonaws.com")(t, endpoint)
		if w := createBusWithKey(t, endpoint, "k1", allowing); w.status != http.StatusOK || bodyMembers(t, w)["KmsKeyIdentifier"] != allowing {
			t.Errorf("CreateEventBus with a key allowing EventBridge = %d %s, want 200 echoing %s", w.status, w.body, allowing)
		}
	})
	t.Run("advise", func(t *testing.T) {
		c, endpoint, _ := enforced(t, server.IAMEnforceAdvise)
		key := createKey(t, endpoint)
		if w := createBusWithKey(t, endpoint, "k0", key); w.status != http.StatusOK {
			t.Fatalf("CreateEventBus under advise = %d %s, want 200", w.status, w.body)
		}
		advised := enforcementAdvisories(t, c, "access-would-deny")
		if !slices.ContainsFunc(advised, func(s string) bool {
			return strings.Contains(s, "events.amazonaws.com") && strings.Contains(s, "kms:GenerateDataKey") && strings.Contains(s, key)
		}) {
			t.Errorf("access-would-deny advisories = %q, want one for events.amazonaws.com's kms:GenerateDataKey on %s", advised, key)
		}
	})
	t.Run("off", func(t *testing.T) {
		c, endpoint, _ := enforced(t, server.IAMEnforceOff)
		key := createKey(t, endpoint)
		if w := createBusWithKey(t, endpoint, "k0", key); w.status != http.StatusOK {
			t.Fatalf("CreateEventBus under off = %d %s, want 200", w.status, w.body)
		}
		if got := enforcementAdvisories(t, c, "access-would-deny"); len(got) != 0 {
			t.Errorf("off journaled access-would-deny advisories %q, want none", got)
		}
	})
}
