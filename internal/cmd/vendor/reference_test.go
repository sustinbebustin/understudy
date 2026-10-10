package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sustinbebustin/understudy/internal/codegen"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// referenceDocFixture is a Service Reference document for prefix: SQS's
// SendMessageBatch authorizes sqs:SendMessage, CreateQueue also
// sqs:TagQueue (smithy-models.md 8), listed out of name order as the service
// may.
func referenceDocFixture(prefix string) []byte {
	return fmt.Appendf(nil, `{
  "Name": %q,
  "Version": "v1.4",
  "Operations": [
    {"Name": "SendMessageBatch", "AuthorizedActions": [{"Name": "SendMessage", "Service": %[1]q}]},
    {"Name": "CreateQueue", "AuthorizedActions": [{"Name": "CreateQueue", "Service": %[1]q}, {"Name": "TagQueue", "Service": %[1]q}]},
    {"Name": "GetCallerIdentity", "AuthorizedActions": []}
  ],
  "Actions": [
    {"Name": "TagQueue", "Annotations": {"Properties": {"IsTaggingOnly": true, "IsWrite": true}}},
    {"Name": "CreateQueue", "Annotations": {"Properties": {"IsWrite": true}}}
  ],
  "Resources": [{"Name": "queue"}]
}`, prefix)
}

func TestDeriveServiceKeepsAuthorizedActionsAndAnnotations(t *testing.T) {
	body := referenceDocFixture("sqs")
	e := indexEntry{Service: "sqs", URL: referenceIndex + "v1/sqs/sqs.json", Modified: 1791496902}
	svc, err := deriveService("sqs", e, body)
	if err != nil {
		t.Fatal(err)
	}
	want := ServiceActions{
		Prefix: "sqs", URL: e.URL, Version: "v1.4", Modified: 1791496902, SHA256: sha256Hex(body),
		Operations: []OperationAction{
			{Name: "CreateQueue", Authorized: []string{"sqs:CreateQueue", "sqs:TagQueue"}},
			{Name: "GetCallerIdentity", Authorized: []string{}},
			{Name: "SendMessageBatch", Authorized: []string{"sqs:SendMessage"}},
		},
		Actions: []ActionInfo{
			{Name: "CreateQueue", IsWrite: true},
			{Name: "TagQueue", IsTaggingOnly: true, IsWrite: true},
		},
	}
	if !reflect.DeepEqual(svc, want) {
		t.Fatalf("deriveService =\n%+v\nwant\n%+v", svc, want)
	}
}

func TestDeriveServiceRefusesAnotherServicesDocument(t *testing.T) {
	_, err := deriveService("sqs", indexEntry{URL: "u"}, referenceDocFixture("sns"))
	if err == nil || !strings.Contains(err.Error(), `names service "sns", want "sqs"`) {
		t.Fatalf("deriveService = %v, want a name mismatch", err)
	}
}

// fakeReference serves the index and one document per pinned prefix.
func fakeReference(t *testing.T) referenceFetcher {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /index", func(w http.ResponseWriter, _ *http.Request) {
		prefixes := slices.Sorted(maps.Values(referencePrefixes()))
		index := make([]indexEntry, 0, len(prefixes))
		for _, p := range prefixes {
			index = append(index, indexEntry{Service: p, URL: srv.URL + "/v1/" + p + "/" + p + ".json", Modified: 1791496902})
		}
		if err := json.NewEncoder(w).Encode(index); err != nil {
			t.Error(err)
		}
	})
	mux.HandleFunc("GET /v1/{prefix}/{file}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(referenceDocFixture(r.PathValue("prefix"))); err != nil {
			t.Error(err)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return referenceFetcher{
		client: srv.Client(), today: func() string { return "2026-10-10" },
		indexURL: srv.URL + "/index",
	}
}

func TestFetchActionsWritesOneEntryPerPinnedPrefix(t *testing.T) {
	dir := t.TempDir()
	if err := fakeReference(t).fetchActions(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dir + "/" + actionsName)
	if err != nil {
		t.Fatal(err)
	}
	var a Actions
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	prefixes := make([]string, 0, len(a.Services))
	for _, s := range a.Services {
		prefixes = append(prefixes, s.Prefix)
	}
	if want := slices.Sorted(maps.Values(referencePrefixes())); !slices.Equal(prefixes, want) || a.Retrieved != "2026-10-10" {
		t.Fatalf("actions.json lists %v retrieved %s, want %v", prefixes, a.Retrieved, want)
	}
	cw := a.Services[0]
	if cw.Prefix != "cloudwatch" || cw.Operations[2].Authorized[0] != "cloudwatch:SendMessage" {
		t.Fatalf("cloudwatch entry = %+v; actions are prefixed by the document's own service", cw)
	}
}

// actionsFixture is an actions table (root) that covers every operation of
// twelve one-operation models (models).
func actionsFixture(t *testing.T) (root, models fstest.MapFS) {
	t.Helper()
	models = fstest.MapFS{}
	a := Actions{Source: referenceIndex, Retrieved: "2026-10-10"}
	for _, prefix := range slices.Sorted(maps.Values(referencePrefixes())) {
		a.Services = append(a.Services, ServiceActions{
			Prefix: prefix, URL: referenceIndex + "v1/" + prefix + "/" + prefix + ".json", Version: "v1.4", Modified: 1, SHA256: strings.Repeat("d", 64),
			Operations: []OperationAction{{Name: "Ping", Authorized: []string{prefix + ":Ping"}}},
		})
	}
	for _, m := range pinnedModels() {
		models[m.Path] = &fstest.MapFile{Data: []byte(`{"smithy":"2.0","shapes":{"x#Ping":{"type":"operation"},"x#S":{"type":"string"}}}`)}
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{actionsName: &fstest.MapFile{Data: raw}}, models
}

func TestVerifyActionsAcceptsACompleteTable(t *testing.T) {
	root, models := actionsFixture(t)
	if err := verifyActions(root, models, "fixture"); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyActionsNamesAModelOperationWithoutAnEntry: a model bump that adds
// an operation fails -verify before the generator does.
func TestVerifyActionsNamesAModelOperationWithoutAnEntry(t *testing.T) {
	root, models := actionsFixture(t)
	sqs := pinnedModels()[10]
	models[sqs.Path] = &fstest.MapFile{Data: []byte(`{"smithy":"2.0","shapes":{"x#Ping":{"type":"operation"},"x#Purge":{"type":"operation"}}}`)}
	err := verifyActions(root, models, "fixture")
	want := "service sqs has no entry for operation Purge of " + sqs.Path
	if ve, ok := errors.AsType[*VerifyError](err); !ok || len(ve.Problems) != 1 || !strings.Contains(err.Error(), want) {
		t.Fatalf("verifyActions = %v, want one problem naming %q", err, want)
	}
}

// TestReferencePrefixesMatchTheServiceConfigs: the prefixes this tool
// fetches are the iamPrefix values the generator looks up, model by model,
// so the two tables cannot drift apart.
func TestReferencePrefixesMatchTheServiceConfigs(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "codegen", "services")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := codegen.ParseServiceConfig(bytes.NewReader(raw), ref.ServiceID(id))
		if err != nil {
			t.Fatal(err)
		}
		got[cfg.Model] = cfg.IAMPrefix
	}
	if want := referencePrefixes(); !maps.Equal(got, want) {
		t.Fatalf("codegen/services model -> iamPrefix = %v\nreferencePrefixes() = %v", got, want)
	}
}
