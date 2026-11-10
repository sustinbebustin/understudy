package ssm_test

import (
	"bytes"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// reserved is the clause the coverage notes share.
const reserved = "a name under /aws/service/ or /aws/reference/secretsmanager/ makes the call answer 501"

// reservedText is the clause every operation but PutParameter carries for
// the reserved-name text, recorded for PutParameter only (SSM-04; SSM-02
// step 05 shows GetParameter's namespace text under /aws/service/).
const reservedText = "unrecorded, understudy's choice: another name whose first level is aws answers PutParameter's recorded " +
	"AccessDeniedException text (No access to reserved parameter name: <name>.), where AWS's text for this operation may differ"

// The clauses the five value-bearing operations' notes carry for the
// default key (ssm-parameters.md 5.6) and for a KMS failure.
const (
	defaultKeyEncrypt = "under iam.enforce, a SecureString on the default key alias/aws/ssm is encrypted as SSM itself, so an explicit identity-policy deny of kms:Encrypt on that key is not applied"
	defaultKeyDecrypt = "under iam.enforce, a SecureString on the default key alias/aws/ssm is decrypted as SSM itself, so an explicit identity-policy deny of kms:Decrypt on that key is not applied"
	// kmsMessage is the KMS failure clause (SSM-22).
	kmsMessage = `a KMS failure carries KMS's message without the KMS client's "(Service: AWSKMS; ...; Request ID: ...; Proxy: null)" suffix AWS appends`
)

// The thirteen Parameter Store operations are bound Partial with their
// coverage notes verbatim (ssm-parameters.md 12.1);
// every other SSM operation, the service settings and resource policies
// included, stays unbound and answers 501 (invariant 3).
func TestCoverageMarks(t *testing.T) {
	tagNote := reserved + "; ResourceType Parameter only: the other resource types name nothing understudy stores, so they answer InvalidResourceId"
	want := map[string]string{
		"PutParameter":            reserved + "; parameter policies answer 501; DataType aws:ec2:image answers 501, since understudy has no EC2 to validate an AMI; an Advanced-tier SecureString, including an Intelligent-Tiering one that resolves to Advanced, answers 501; AllowedPattern is compiled as Go RE2, where AWS's dialect is Java's: a pattern RE2 cannot compile, such as a lookahead or a possessive quantifier, answers 501, except an unbalanced bracket or parenthesis, a trailing backslash, a repetition without an argument or a reversed character range, which answer InvalidAllowedPatternException; no Parameter Store Change events are sent; the default tier is always Standard (the service setting is unbound); a {{...}} value's refusal leaves out the value AWS's message quotes; " + kmsMessage + "; unrecorded, understudy's choice: the count refusal's message is understudy's own; " + defaultKeyEncrypt,
		"GetParameter":            "no AWS public parameters: names under /aws/service/ answer 501, where AWS answers AccessDeniedException for a name it does not publish; Secrets Manager references (names under /aws/reference/secretsmanager/) answer 501; the legacy Query protocol (?Action= without X-Amz-Target), which AWS also serves, answers 501 for this and every SSM operation; " + kmsMessage + "; " + defaultKeyDecrypt + "; " + reservedText,
		"GetParameters":           "no AWS public parameters: a name under /aws/service/ makes the call answer 501; a Secrets Manager reference (a name under /aws/reference/secretsmanager/) makes the call answer 501; unrecorded, understudy's choice: a parameter asked by name and by ARN is answered once per spelling; " + kmsMessage + "; " + defaultKeyDecrypt + "; " + reservedText,
		"GetParametersByPath":     "no AWS public parameters or Secrets Manager references: paths under /aws/service/ or /aws/reference/secretsmanager/ answer 501; results come in name order across pages, where AWS's pages are ordered within but not across (switch ssm.list.order); " + kmsMessage + "; " + defaultKeyDecrypt,
		"DescribeParameters":      "AWS public parameters and Secrets Manager references are never listed: a Name or Path filter value under /aws/service/ or /aws/reference/secretsmanager/ answers 501; Shared lists nothing, since no parameter can be shared (resource policies are unbound); the deprecated Filters member alone answers 501 (use ParameterFilters); results come in name order across pages, where AWS's pages are ordered within but not across (switch ssm.list.order); unrecorded, understudy's choice: the values of one filter match any of them",
		"GetParameterHistory":     reserved + "; " + kmsMessage + "; " + defaultKeyDecrypt + "; " + reservedText,
		"DeleteParameter":         reserved + "; no Parameter Store Change events are sent; " + reservedText,
		"DeleteParameters":        reserved + "; no Parameter Store Change events are sent; " + reservedText,
		"LabelParameterVersion":   reserved + "; no Parameter Store Change events are sent; InvalidLabels lists the refused labels in request order, where AWS's order is its own; " + reservedText,
		"UnlabelParameterVersion": reserved + "; " + reservedText,
		"AddTagsToResource":       tagNote + "; " + reservedText,
		"RemoveTagsFromResource":  tagNote + "; " + reservedText,
		"ListTagsForResource":     tagNote + "; TagList lists tags in the order they were first set, where AWS's order is its own; " + reservedText,
	}
	run(t, func(h *harness) {
		for _, op := range ssmapi.Service().Operations {
			status := h.Kernel.Bindings().Status(op)
			note, listed := want[op.ID.Name]
			if !listed {
				if _, unbound := status.(kernel.Unbound); !unbound {
					h.Errorf("%s is %#v, want unbound", op.ID.Name, status)
				}
				continue
			}
			if bound, ok := status.(kernel.Bound); !ok || bound.Coverage != (registry.Partial{Note: note}) {
				h.Errorf("%s is %#v, want Partial with its coverage note", op.ID.Name, status)
			}
		}
		for _, op := range []string{"GetServiceSetting", "UpdateServiceSetting", "ResetServiceSetting", "PutResourcePolicy", "GetResourcePolicies", "DeleteResourcePolicy"} {
			if _, listed := want[op]; listed {
				h.Errorf("%s must stay unbound", op)
			}
		}
	})
}

// The five fidelity switches of the spec, each with its label, research
// ref, record, operations and the parity step that settles it: four
// recorded as understudy behaves, ssm.list.order contradicted and kept
// inferred (ssm-parameters.md 12.2; SSM-02, SSM-07, SSM-10, SSM-19,
// SSM-20, SSM-22).
func TestSwitches(t *testing.T) {
	type sw struct {
		value, slug, record string
		label               evidence.Label
		settles             fidelity.Settles
		ops                 []string
	}
	step := func(item, step string) fidelity.Settles {
		return fidelity.SettledBy{Step: fidelity.StepRef{Scenario: "ssm/" + item, Step: step}}
	}
	recorded := func(steps string) fidelity.Settles {
		return fidelity.NoScenario{Reason: "recorded as understudy behaves (" + steps + "); the step's replay differs only in JSON member order, which AWS sorts by name"}
	}
	want := map[string]sw{
		"ssm.name.root-slash-echo":              {"request-spelling", "32-root-parameters-and-the-leading-slash", "SSM-07", evidence.AWSRecorded, recorded("SSM-07 steps 01-07"), []string{"ssm.GetParameter", "ssm.GetParameters", "ssm.DescribeParameters", "ssm.GetParameterHistory"}},
		"ssm.error.parameter-not-found-message": {"empty-except-label", "22-errors", "SSM-02", evidence.AWSRecorded, step("SSM-02", "03-label"), []string{"ssm.DeleteParameter", "ssm.GetParameterHistory", "ssm.LabelParameterVersion", "ssm.UnlabelParameterVersion"}},
		"ssm.put.omitted-fields":                {"preserved", "71-putparameter", "SSM-10", evidence.AWSRecorded, recorded("SSM-10 steps 02-03"), []string{"ssm.PutParameter"}},
		"ssm.kms.key-id-echo":                   {"verbatim", "54-keyid-in-metadata", "SSM-22", evidence.AWSRecorded, recorded("SSM-22 steps 01-02"), []string{"ssm.PutParameter", "ssm.DescribeParameters", "ssm.GetParameterHistory"}},
		"ssm.list.order":                        {"name-ascending", "74-getparametersbypath", "SSM-19, SSM-20", evidence.Inferred, step("SSM-20", "01-default-page"), []string{"ssm.GetParametersByPath", "ssm.DescribeParameters"}},
	}
	run(t, func(h *harness) {
		got := map[string]fidelity.Entry{}
		for _, e := range h.svc.Switches() {
			got[e.Name] = e
		}
		if names := slices.Sorted(maps.Keys(got)); !slices.Equal(names, slices.Sorted(maps.Keys(want))) {
			h.Errorf("Switches() = %v, want exactly these five", names)
		}
		for name, w := range want {
			e, ok := got[name]
			if !ok {
				continue
			}
			if e.Value != w.value || e.Label != w.label || e.Record != w.record || !slices.Equal(e.Ops, w.ops) {
				h.Errorf("%s = %+v, want value %q, %s, record %s, ops %v", name, e, w.value, w.label, w.record, w.ops)
			}
			if e.Ref != "docs/research/ssm-parameters.md#"+w.slug {
				h.Errorf("%s Ref = %q, want docs/research/ssm-parameters.md#%s", name, e.Ref, w.slug)
			}
			if e.Settles != w.settles {
				h.Errorf("%s Settles = %#v, want %#v", name, e.Settles, w.settles)
			}
		}
	})
}

// Resources lists each parameter of the scope by ARN in byte order of
// names, summarized "<Type>, version <n>". A snapshot export,
// a Reset and an import restore the same answers to the reads; an import
// of another state version is refused (store.VersionMismatchError).
func TestResourcesAndSnapshotRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/snap/b", "one")
		h.overwrite("/snap/b", "two")
		h.putSecure("/snap/a", "sealed", nil)
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "root", Value: "x,y", Type: new(ssmapi.ParameterTypeStringList), Tags: []ssmapi.Tag{{Key: "k", Value: "v"}}})
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/snap/b", ParameterVersion: new(int64(1)), Labels: []string{"first"}})

		type res struct{ arn, summary string }
		var got []res
		for _, r := range h.svc.Resources(h.Scope) {
			got = append(got, res{r.ARN.String(), r.Summary})
		}
		want := []res{
			{arnPrefix + "root", "StringList, version 1"},
			{arnPrefix + "snap/a", "SecureString, version 1"},
			{arnPrefix + "snap/b", "String, version 2"},
		}
		slices.SortFunc(want, func(a, b res) int { return strings.Compare(a.arn, b.arn) })
		if !slices.Equal(got, want) {
			h.Errorf("Resources = %v, want %v", got, want)
		}

		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		if len(sections) != 1 {
			h.Errorf("Export wrote %d sections, want one for the one scope", len(sections))
		}
		for _, s := range sections {
			if s.Version != 1 {
				h.Errorf("section %s version %d, want 1", s.Path, s.Version)
			}
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.get("/snap/b"); err == nil {
			h.Error("a parameter survived Reset")
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		if got := h.mustGet("/snap/b", false); val(got.Value) != "two" || got.Version != 2 {
			h.Errorf("after import /snap/b = %q version %d", val(got.Value), got.Version)
		}
		if got := h.mustGet("/snap/b:first", false); val(got.Value) != "one" {
			h.Errorf("after import the label resolves to %q, want one", val(got.Value))
		}
		if got := h.mustGet("/snap/a", true); val(got.Value) != "sealed" {
			h.Errorf("after import the SecureString decrypts to %q", val(got.Value))
		}
		tags := must(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "root"}).TagList
		if len(tags) != 1 || tags[0].Key != "k" {
			h.Errorf("after import root tags = %+v", tags)
		}
		if got := versions(h.history("/snap/b")); !slices.Equal(got, []int64{1, 2}) {
			h.Errorf("after import history = %v, want [1 2]", got)
		}

		bumped := slices.Clone(sections)
		for i := range bumped {
			bumped[i].Version = 2
		}
		var mismatch *store.VersionMismatchError
		if err := h.svc.Import(bumped); !errors.As(err, &mismatch) {
			h.Errorf("Import of a version-2 section = %v, want *store.VersionMismatchError", err)
		}
	})
}

// No SecureString plaintext reaches the journal, the logs or the snapshot,
// on the success paths or on any refusal (ssm-parameters.md 12.6: "never
// keep it outside KMS sealing"), and no error message holds a Value of any
// type.
func TestPlaintextNeverLeaks(t *testing.T) {
	const marker = "PLAINTEXT-MARKER-7f3a9c"
	journalPath := filepath.Join(t.TempDir(), "journal.ndjson")
	var logs bytes.Buffer
	var exported []byte
	run(t, func(h *harness) {
		refuse := func(what string, in *ssmapi.PutParameterInput) {
			h.Helper()
			_, err := call(h, ssmapi.OpPutParameter, in)
			if err == nil {
				h.Errorf("%s was accepted", what)
				return
			}
			if strings.Contains(err.Message, marker) {
				h.Errorf("%s: message %q holds the value", what, err.Message)
			}
		}
		h.putSecure("/leak/ok", marker, nil)
		key := h.createKey()
		h.putSecure("/leak/cmk", marker+"-cmk", new(key))
		h.mustGet("/leak/ok", true)
		h.mustGet("/leak/cmk", true)
		must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/leak/ok"}, WithDecryption: new(true)})
		must(h, ssmapi.OpGetParametersByPath, &ssmapi.GetParametersByPathInput{Path: "/leak", WithDecryption: new(true)})
		must(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: "/leak/ok", WithDecryption: new(true)})

		secure := new(ssmapi.ParameterTypeSecureString)
		refuse("an oversize SecureString", &ssmapi.PutParameterInput{Name: "/leak/big", Value: marker + strings.Repeat("x", 5000), Type: secure, Tier: new(ssmapi.ParameterTierStandard)})
		refuse("a {{...}} SecureString", &ssmapi.PutParameterInput{Name: "/leak/braces", Value: marker + "{{ssm:other}}", Type: secure})
		refuse("a SecureString failing its pattern", &ssmapi.PutParameterInput{Name: "/leak/pattern", Value: marker, Type: secure, AllowedPattern: new(`\d+`)})
		refuse("a SecureString under a missing key", &ssmapi.PutParameterInput{Name: "/leak/kms", Value: marker, Type: secure, KeyId: new("alias/missing")})
		refuse("a String failing its pattern", &ssmapi.PutParameterInput{Name: "/leak/plain", Value: marker, Type: new(ssmapi.ParameterTypeString), AllowedPattern: new(`\d+`)})

		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		for _, s := range sections {
			exported = append(exported, s.Data...)
		}
	}, kerneltest.JournalFile(journalPath), kerneltest.Log(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))

	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) == 0 || len(exported) == 0 {
		t.Fatalf("journal %d bytes, export %d bytes; the test grepped nothing", len(journal), len(exported))
	}
	for what, b := range map[string][]byte{"journal": journal, "logs": logs.Bytes(), "snapshot": exported} {
		if bytes.Contains(b, []byte(marker)) {
			t.Errorf("the %s holds a SecureString plaintext", what)
		}
	}
}
