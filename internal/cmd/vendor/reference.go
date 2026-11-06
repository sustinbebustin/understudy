package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The AWS Service Reference (smithy-models.md 8): one JSON document per IAM
// service prefix, listed with its modification time at the index. Only the
// derived table is vendored; the raw documents carry no stated licence
// (ADR 0016).
const (
	referenceIndex = "https://servicereference.us-east-1.amazonaws.com/"
	actionsName    = "actions.json"
	// boto3 is the SDK package whose client names referenceServices uses.
	boto3 = "Boto3"
)

// referenceService is where one pinned model's operations sit in the
// Service Reference: Prefix is the document (the IAM service prefix) and SDK
// the Boto3 client name its Operations[].SDK entries carry. Two models can
// share a prefix and an operation name (API Gateway v1 and v2 are both
// apigateway and both have CreateDeployment); SDK tells such operations
// apart, and is empty for a model whose operations need no telling apart.
type referenceService struct {
	Prefix string
	SDK    string
}

// referenceServices maps each pinned model directory to its place in the
// Service Reference: CloudWatch is cloudwatch, not the model's arnNamespace
// monitoring (smithy-models.md 3.1, 8). It mirrors the iamPrefix and
// sdkService of codegen/services, which a test checks.
func referenceServices() map[string]referenceService {
	return map[string]referenceService{
		"api-gateway":      {Prefix: "apigateway", SDK: "apigateway"},
		"apigatewayv2":     {Prefix: "apigateway", SDK: "apigatewayv2"},
		"cloudwatch":       {Prefix: "cloudwatch"},
		"cloudwatch-logs":  {Prefix: "logs"},
		"dynamodb":         {Prefix: "dynamodb"},
		"dynamodb-streams": {Prefix: "dynamodb"},
		"eventbridge":      {Prefix: "events"},
		"iam":              {Prefix: "iam"},
		"kinesis":          {Prefix: "kinesis"},
		"kms":              {Prefix: "kms"},
		"lambda":           {Prefix: "lambda"},
		"s3":               {Prefix: "s3"},
		"scheduler":        {Prefix: "scheduler"},
		"secrets-manager":  {Prefix: "secretsmanager"},
		"ses":              {Prefix: "ses", SDK: "ses"},
		"sesv2":            {Prefix: "ses", SDK: "sesv2"},
		"sfn":              {Prefix: "states"},
		"sns":              {Prefix: "sns"},
		"sqs":              {Prefix: "sqs"},
		"ssm":              {Prefix: "ssm"},
		"sts":              {Prefix: "sts"},
	}
}

// referencePrefixes are the Service Reference documents to fetch, each
// once, sorted.
func referencePrefixes() []string {
	services := referenceServices()
	out := make([]string, 0, len(services))
	for _, s := range services {
		out = append(out, s.Prefix)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Actions is actions.json: per service, the IAM actions each operation
// authorizes and the annotations of the service's own actions, with the
// provenance of the document they were derived from.
type Actions struct {
	Source    string           `json:"source"`
	Retrieved string           `json:"retrieved"`
	Services  []ServiceActions `json:"services"`
}

// ServiceActions is one service's part of the table.
type ServiceActions struct {
	Prefix string `json:"prefix"`
	URL    string `json:"url"`
	// Version is the document's Version field, such as v1.4.
	Version string `json:"version"`
	// Modified is the index's modification time of the document, in Unix
	// seconds.
	Modified int64 `json:"modified"`
	// SHA256 is the hash of the raw document body the table was derived
	// from.
	SHA256     string            `json:"sha256"`
	Operations []OperationAction `json:"operations"`
	Actions    []ActionInfo      `json:"actions"`
}

// OperationAction is Operations[].AuthorizedActions of one operation, each
// action as prefix:Name in the document's order. An empty list is the
// document's own: the operation authorizes no action. SDK is the sorted Boto3
// client names of the operation's Operations[].SDK entries, which say which
// model an operation of a shared document belongs to; it is empty when the
// document names none.
type OperationAction struct {
	Name       string   `json:"name"`
	SDK        []string `json:"sdk,omitempty"`
	Authorized []string `json:"authorized"`
}

// appliesTo reports whether the entry is an operation of the model whose
// Boto3 client is sdk: an entry naming no SDK applies to every model of its
// document, and a model with no sdk takes every entry.
func (o OperationAction) appliesTo(sdk string) bool {
	return sdk == "" || len(o.SDK) == 0 || slices.Contains(o.SDK, sdk)
}

// compareOperations orders entries by name, then by SDK names, so a shared
// document's operations of one name sit together.
func compareOperations(a, b OperationAction) int {
	if c := strings.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	return slices.Compare(a.SDK, b.SDK)
}

// ActionInfo is Actions[].Annotations.Properties of one action.
type ActionInfo struct {
	Name                   string `json:"name"`
	IsList                 bool   `json:"isList"`
	IsPermissionManagement bool   `json:"isPermissionManagement"`
	IsTaggingOnly          bool   `json:"isTaggingOnly"`
	IsWrite                bool   `json:"isWrite"`
}

// referenceDoc is the part of a Service Reference document the table keeps.
type referenceDoc struct {
	Name       string `json:"Name"`
	Version    string `json:"Version"`
	Operations []struct {
		Name              string `json:"Name"`
		AuthorizedActions []struct {
			Name    string `json:"Name"`
			Service string `json:"Service"`
		} `json:"AuthorizedActions"`
		SDK []struct {
			Name    string `json:"Name"`
			Package string `json:"Package"`
		} `json:"SDK"`
	} `json:"Operations"`
	Actions []struct {
		Name        string `json:"Name"`
		Annotations struct {
			Properties ActionInfo `json:"Properties"`
		} `json:"Annotations"`
	} `json:"Actions"`
}

// indexEntry is one row of the Service Reference index.
type indexEntry struct {
	Service  string `json:"service"`
	URL      string `json:"url"`
	Modified int64  `json:"modified"`
}

// referenceFetcher downloads the index and the documents; the index URL is a
// field so tests can serve their own.
type referenceFetcher struct {
	fetcher
	indexURL string
}

func serviceReferenceFetcher() referenceFetcher {
	return referenceFetcher{fetcher: githubFetcher(), indexURL: referenceIndex}
}

// fetchActions downloads one document per pinned prefix and writes the
// derived table to dir. Nothing is written unless every document downloads
// and parses.
func (f referenceFetcher) fetchActions(ctx context.Context, dir string) error {
	var index []indexEntry
	if err := f.getJSON(ctx, f.indexURL, &index); err != nil {
		return fmt.Errorf("read the Service Reference index %s: %w", f.indexURL, err)
	}
	byPrefix := make(map[string]indexEntry, len(index))
	for _, e := range index {
		byPrefix[e.Service] = e
	}
	out := Actions{Source: f.indexURL, Retrieved: f.today()}
	for _, prefix := range referencePrefixes() {
		e, ok := byPrefix[prefix]
		if !ok {
			return fmt.Errorf("the Service Reference index lists no %q document; the prefix table in reference.go is wrong", prefix)
		}
		body, err := f.get(ctx, e.URL)
		if err != nil {
			return fmt.Errorf("download %s: %w", e.URL, err)
		}
		svc, err := deriveService(prefix, e, body)
		if err != nil {
			return err
		}
		out.Services = append(out.Services, svc)
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", actionsName, err)
	}
	return writeFileAtomic(filepath.Join(dir, actionsName), append(raw, '\n'))
}

// deriveService keeps a document's operations, each authorized action as
// prefix:Name with the Boto3 clients the operation belongs to, and the
// annotations of the service's own actions, sorted by name.
func deriveService(prefix string, e indexEntry, body []byte) (ServiceActions, error) {
	var doc referenceDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return ServiceActions{}, fmt.Errorf("parse %s: %w", e.URL, err)
	}
	if doc.Name != prefix {
		return ServiceActions{}, fmt.Errorf("%s names service %q, want %q", e.URL, doc.Name, prefix)
	}
	svc := ServiceActions{Prefix: prefix, URL: e.URL, Version: doc.Version, Modified: e.Modified, SHA256: sha256Hex(body)}
	for _, op := range doc.Operations {
		oa := OperationAction{Name: op.Name, Authorized: []string{}}
		for _, a := range op.AuthorizedActions {
			oa.Authorized = append(oa.Authorized, a.Service+":"+a.Name)
		}
		for _, sdk := range op.SDK {
			if sdk.Package == boto3 {
				oa.SDK = append(oa.SDK, sdk.Name)
			}
		}
		slices.Sort(oa.SDK)
		oa.SDK = slices.Compact(oa.SDK)
		svc.Operations = append(svc.Operations, oa)
	}
	for _, a := range doc.Actions {
		info := a.Annotations.Properties
		info.Name = a.Name
		svc.Actions = append(svc.Actions, info)
	}
	slices.SortFunc(svc.Operations, compareOperations)
	slices.SortFunc(svc.Actions, func(a, b ActionInfo) int { return strings.Compare(a.Name, b.Name) })
	return svc, nil
}

var (
	actionPattern = regexp.MustCompile(`^[a-z0-9-]+:[A-Za-z0-9]+$`)
	hexPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sha1Pattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// verifyActions checks actions.json offline: one entry per pinned prefix with
// its provenance, operations and actions sorted by name, no operation listed
// twice for one model, well-formed action names, and an entry for every
// operation shape of every pinned model in models (the api-models-aws tree),
// so a model bump that adds an operation fails here before the generator
// does.
func verifyActions(root, models fs.FS, dir string) error {
	var problems []Problem
	add := func(p, format string, args ...any) {
		problems = append(problems, Problem{Path: p, Detail: fmt.Sprintf(format, args...)})
	}
	raw, err := fs.ReadFile(root, actionsName)
	if err != nil {
		return fmt.Errorf("verify %s: read %s: %w; run 'go run ./internal/cmd/vendor -service-reference' to create it", dir, actionsName, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var a Actions
	if err := dec.Decode(&a); err != nil {
		return fmt.Errorf("verify %s: parse %s: %w", dir, actionsName, err)
	}
	if a.Source != referenceIndex {
		add(actionsName, "records source %q, want %q", a.Source, referenceIndex)
	}
	if _, err := time.Parse(time.DateOnly, a.Retrieved); err != nil {
		add(actionsName, "retrieved %q is not a YYYY-MM-DD date", a.Retrieved)
	}
	want := referencePrefixes()
	got := make([]string, len(a.Services))
	for i, s := range a.Services {
		got[i] = s.Prefix
	}
	if !slices.Equal(got, want) {
		add(actionsName, "lists services %v, want %v in that order", got, want)
	}
	byPrefix := map[string]ServiceActions{}
	for _, s := range a.Services {
		byPrefix[s.Prefix] = s
		checkService(s, add)
	}
	for _, m := range pinnedModels() {
		checkModelCovered(models, m, byPrefix, add)
	}
	if len(problems) > 0 {
		return &VerifyError{Dir: dir, Problems: problems}
	}
	return nil
}

func checkService(s ServiceActions, add func(p, format string, args ...any)) {
	where := actionsName + " " + s.Prefix
	if s.URL != referenceIndex+"v1/"+s.Prefix+"/"+s.Prefix+".json" {
		add(where, "url %q is not the Service Reference document of %s", s.URL, s.Prefix)
	}
	if s.Version == "" || s.Modified <= 0 {
		add(where, "provenance needs the document Version and the index modification time; have %q and %d", s.Version, s.Modified)
	}
	if !hexPattern.MatchString(s.SHA256) {
		add(where, "sha256 %q is not 64 lowercase hex digits", s.SHA256)
	}
	if !slices.IsSortedFunc(s.Operations, compareOperations) {
		add(where, "operations are not sorted by name, then SDK")
	}
	if !slices.IsSortedFunc(s.Actions, func(a, b ActionInfo) int { return strings.Compare(a.Name, b.Name) }) {
		add(where, "actions are not sorted by name")
	}
	for i, op := range s.Operations {
		for _, prev := range s.Operations[:i] {
			if prev.Name == op.Name && sharesModel(prev, op) {
				add(where, "operation %s appears twice for one SDK client", op.Name)
				break
			}
		}
		for _, act := range op.Authorized {
			if !actionPattern.MatchString(act) {
				add(where, "operation %s authorizes %q, which is not prefix:Action", op.Name, act)
			}
		}
	}
}

// checkModelCovered reports each operation shape of model m that has no
// entry for it in its Service Reference document.
func checkModelCovered(models fs.FS, m model, byPrefix map[string]ServiceActions, add func(p, format string, args ...any)) {
	ref := referenceServices()[m.Dir]
	ops, err := modelOperations(models, m.Path)
	if err != nil {
		add(m.Path, "%v", err)
		return
	}
	have := map[string]bool{}
	for _, op := range byPrefix[ref.Prefix].Operations {
		if op.appliesTo(ref.SDK) {
			have[op.Name] = true
		}
	}
	client := ""
	if ref.SDK != "" {
		client = " for the Boto3 client " + ref.SDK
	}
	for _, op := range ops {
		if !have[op] {
			add(actionsName, "service %s has no entry for operation %s of %s%s; re-run -service-reference", ref.Prefix, op, m.Path, client)
		}
	}
}

// sharesModel reports whether two entries could both apply to one model:
// either names no SDK, or their SDK lists overlap.
func sharesModel(a, b OperationAction) bool {
	if len(a.SDK) == 0 || len(b.SDK) == 0 {
		return true
	}
	return slices.ContainsFunc(a.SDK, func(s string) bool { return slices.Contains(b.SDK, s) })
}

// modelOperations lists the names of the operation shapes of one model file.
func modelOperations(models fs.FS, p string) ([]string, error) {
	raw, err := fs.ReadFile(models, p)
	if err != nil {
		return nil, err
	}
	var m struct {
		Shapes map[string]struct {
			Type string `json:"type"`
		} `json:"shapes"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var out []string
	for id, s := range m.Shapes {
		if s.Type == "operation" {
			_, name, _ := strings.Cut(id, "#")
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}
