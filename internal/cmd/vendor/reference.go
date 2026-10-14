package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
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
)

// referencePrefixes maps each pinned model directory to its IAM service
// prefix, the Service Reference document name: CloudWatch is cloudwatch, not
// the model's arnNamespace monitoring (smithy-models.md 3.1, 8).
func referencePrefixes() map[string]string {
	return map[string]string{
		"cloudwatch":      "cloudwatch",
		"cloudwatch-logs": "logs",
		"dynamodb":        "dynamodb",
		"eventbridge":     "events",
		"iam":             "iam",
		"kms":             "kms",
		"lambda":          "lambda",
		"s3":              "s3",
		"secrets-manager": "secretsmanager",
		"sns":             "sns",
		"sqs":             "sqs",
		"sts":             "sts",
	}
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
// document's own: the operation authorizes no action.
type OperationAction struct {
	Name       string   `json:"name"`
	Authorized []string `json:"authorized"`
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

// fetchActions downloads the twelve documents and writes the derived table to
// dir. Nothing is written unless every document downloads and parses.
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
	for _, prefix := range slices.Sorted(maps.Values(referencePrefixes())) {
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
// prefix:Name, and the annotations of the service's own actions, sorted by
// name.
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
		svc.Operations = append(svc.Operations, oa)
	}
	for _, a := range doc.Actions {
		info := a.Annotations.Properties
		info.Name = a.Name
		svc.Actions = append(svc.Actions, info)
	}
	slices.SortFunc(svc.Operations, func(a, b OperationAction) int { return strings.Compare(a.Name, b.Name) })
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
// twice, well-formed action names, and an entry for every operation shape of every pinned model in
// models (the api-models-aws tree), so a model bump that adds an operation
// fails here before the generator does.
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
	want := slices.Sorted(maps.Values(referencePrefixes()))
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
		prefix := referencePrefixes()[m.Dir]
		ops, err := modelOperations(models, m.Path)
		if err != nil {
			add(m.Path, "%v", err)
			continue
		}
		have := map[string]bool{}
		for _, op := range byPrefix[prefix].Operations {
			have[op.Name] = true
		}
		for _, op := range ops {
			if !have[op] {
				add(actionsName, "service %s has no entry for operation %s of %s; re-run -service-reference", prefix, op, m.Path)
			}
		}
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
	if !slices.IsSortedFunc(s.Operations, func(a, b OperationAction) int { return strings.Compare(a.Name, b.Name) }) {
		add(where, "operations are not sorted by name")
	}
	if !slices.IsSortedFunc(s.Actions, func(a, b ActionInfo) int { return strings.Compare(a.Name, b.Name) }) {
		add(where, "actions are not sorted by name")
	}
	for i, op := range s.Operations {
		if i > 0 && s.Operations[i-1].Name == op.Name {
			add(where, "operation %s appears twice", op.Name)
		}
		for _, act := range op.Authorized {
			if !actionPattern.MatchString(act) {
				add(where, "operation %s authorizes %q, which is not prefix:Action", op.Name, act)
			}
		}
	}
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
