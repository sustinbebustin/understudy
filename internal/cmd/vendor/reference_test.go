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
		prefixes := referencePrefixes()
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
	if want := referencePrefixes(); !slices.Equal(prefixes, want) || a.Retrieved != "2026-10-10" {
		t.Fatalf("actions.json lists %v retrieved %s, want %v", prefixes, a.Retrieved, want)
	}
	cw := a.Services[0]
	if cw.Prefix != "cloudwatch" || cw.Operations[2].Authorized[0] != "cloudwatch:SendMessage" {
		t.Fatalf("cloudwatch entry = %+v; actions are prefixed by the document's own service", cw)
	}
}

// actionsFixture is an actions table (root) that covers every operation of
// the pinned models, each a one-operation model (models).
func actionsFixture(t *testing.T) (root, models fstest.MapFS) {
	t.Helper()
	models = fstest.MapFS{}
	a := Actions{Source: referenceIndex, Retrieved: "2026-10-10"}
	for _, prefix := range referencePrefixes() {
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
	sqs := pinnedModel(t, "sqs")
	models[sqs.Path] = &fstest.MapFile{Data: []byte(`{"smithy":"2.0","shapes":{"x#Ping":{"type":"operation"},"x#Purge":{"type":"operation"}}}`)}
	err := verifyActions(root, models, "fixture")
	want := "service sqs has no entry for operation Purge of " + sqs.Path
	if ve, ok := errors.AsType[*VerifyError](err); !ok || len(ve.Problems) != 1 || !strings.Contains(err.Error(), want) {
		t.Fatalf("verifyActions = %v, want one problem naming %q", err, want)
	}
}

// pinnedModel is the pinned model of directory dir.
func pinnedModel(t *testing.T, dir string) model {
	t.Helper()
	i := slices.IndexFunc(pinnedModels(), func(m model) bool { return m.Dir == dir })
	if i < 0 {
		t.Fatalf("no pinned model in directory %s", dir)
	}
	return pinnedModels()[i]
}

// sharedDocFixture is a Service Reference document whose CreateDeployment is
// an operation of two Boto3 clients with different authorized actions, as a
// prefix two models share may list it.
func sharedDocFixture() []byte {
	return []byte(`{
  "Name": "apigateway",
  "Version": "v1.4",
  "Operations": [
    {"Name": "CreateDeployment", "AuthorizedActions": [{"Name": "POST", "Service": "apigateway"}],
     "SDK": [{"Name": "apigatewayv2", "Method": "create_deployment", "Package": "Boto3"}]},
    {"Name": "CreateDeployment", "AuthorizedActions": [{"Name": "POST", "Service": "apigateway"}, {"Name": "PATCH", "Service": "apigateway"}],
     "SDK": [{"Name": "apigateway", "Method": "create_deployment", "Package": "Boto3"}, {"Name": "apigateway", "Method": "createDeployment", "Package": "SomeOtherSDK"}]}
  ],
  "Actions": []
}`)
}

func TestDeriveServiceKeepsEachSDKClientsOperation(t *testing.T) {
	svc, err := deriveService("apigateway", indexEntry{URL: "u"}, sharedDocFixture())
	if err != nil {
		t.Fatal(err)
	}
	want := []OperationAction{
		{Name: "CreateDeployment", SDK: []string{"apigateway"}, Authorized: []string{"apigateway:POST", "apigateway:PATCH"}},
		{Name: "CreateDeployment", SDK: []string{"apigatewayv2"}, Authorized: []string{"apigateway:POST"}},
	}
	if !reflect.DeepEqual(svc.Operations, want) {
		t.Fatalf("operations =\n%+v\nwant\n%+v", svc.Operations, want)
	}
	var problems []string
	checkService(svc, func(_, format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) })
	if slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "appears twice") }) {
		t.Fatalf("checkService = %v; one name for two SDK clients is not a duplicate", problems)
	}
}

func TestCheckServiceRefusesAnOperationListedTwiceForOneClient(t *testing.T) {
	svc := ServiceActions{Prefix: "sqs", Operations: []OperationAction{
		{Name: "SendMessage", Authorized: []string{"sqs:SendMessage"}},
		{Name: "SendMessage", SDK: []string{"sqs"}, Authorized: []string{"sqs:SendMessage"}},
	}}
	var problems []string
	checkService(svc, func(_, format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) })
	if !slices.Contains(problems, "operation SendMessage appears twice for one SDK client") {
		t.Fatalf("checkService = %v, want the duplicate named", problems)
	}
}

// TestReferenceServicesMatchTheServiceConfigs: the prefixes this tool
// fetches and the SDK clients it keeps apart are the iamPrefix and
// sdkService values the generator looks up, model by model, so the two
// tables cannot drift apart.
func TestReferenceServicesMatchTheServiceConfigs(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "codegen", "services")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]referenceService{}
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
		got[cfg.Model] = referenceService{Prefix: cfg.IAMPrefix, SDK: cfg.SDKService}
	}
	if want := referenceServices(); !maps.Equal(got, want) {
		t.Fatalf("codegen/services model -> {iamPrefix, sdkService} = %v\nreferenceServices() = %v", got, want)
	}
}
