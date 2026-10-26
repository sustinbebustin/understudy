package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

func TestRunNeedsExactlyOneMode(t *testing.T) {
	for _, args := range [][]string{nil, {"-fetch", "-verify"}, {"-verify", "extra"}} {
		var stderr strings.Builder
		if err := run(t.Context(), args, &stderr); !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want errUsage", args, err)
		}
	}
}

// -verify on a directory the generator wrote prints its one review line.
func TestRunVerifyPrintsTheSummary(t *testing.T) {
	dir, cat, _ := written(t)
	var stderr strings.Builder
	if err := run(t.Context(), []string{"-verify", "-dir", dir}, &stderr); err != nil {
		t.Fatalf("run -verify = %v", err)
	}
	if want := "managedpolicies: " + summary(cat) + "\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestRunVerifyRefusesAnIncompleteDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, awsmanagedpolicies.PoliciesFile), []byte("{\n  \"policies\": []\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	err := run(t.Context(), []string{"-verify", "-dir", dir}, &stderr)
	if _, ok := errors.AsType[*VerifyError](err); !ok {
		t.Errorf("run -verify = %v, want a *VerifyError", err)
	}
}

// The summary is the line the maintainer checks against the counts of
// iam-evaluation.md 10.4: the total, one count per Type, and the highest
// default version by its number.
func TestSummary(t *testing.T) {
	if got, want := summary(fixtureCatalogue(t)), "4 policies (1 AWS managed policy, 1 Service-linked role policy, 1 Service role policy, 1 Job function policy), highest default version v190"; got != want {
		t.Errorf("summary(fixture) = %q, want %q", got, want)
	}
	if got, want := summary(nil), "0 policies (0 AWS managed policy, 0 Service-linked role policy, 0 Service role policy, 0 Job function policy), highest default version none"; got != want {
		t.Errorf("summary(empty) = %q, want %q", got, want)
	}
	ps := fixturePolicies()
	ps[0].VersionID, ps[1].VersionID, ps[2].VersionID, ps[3].VersionID = "v9", "v20.1", "v3", "v19"
	ps[3].Type, ps[3].Path, ps[3].ARN = policycatalogue.ServiceRole, "/service-role/", "arn:aws:iam::aws:policy/service-role/"+ps[3].Name
	cat, err := policycatalogue.New(ps)
	if err != nil {
		t.Fatalf("policycatalogue.New = %v", err)
	}
	if got, want := summary(cat), "4 policies (1 AWS managed policy, 0 Service-linked role policy, 2 Service role policy, 1 Job function policy), highest default version v20"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// The real fetcher reads docs.aws.amazon.com with a bounded client, waits
// on the real clock (here the bubble's) until the wait ends or its context
// does, and dates the run in UTC. No request is made.
func TestDocsFetcher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := docsFetcher()
		if f.base != docsBase {
			t.Errorf("base = %q, want %q", f.base, docsBase)
		}
		if f.client == nil || f.client.Timeout != 2*time.Minute {
			t.Errorf("client = %+v, want a 2-minute timeout", f.client)
		}
		if f.sleep == nil || f.today == nil {
			t.Fatal("docsFetcher has no sleep or no today")
		}
		start := time.Now()
		if err := f.sleep(t.Context(), 3*time.Second); err != nil {
			t.Errorf("sleep(3s) = %v", err)
		}
		if waited := time.Since(start); waited != 3*time.Second {
			t.Errorf("sleep(3s) waited %v", waited)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := f.sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
			t.Errorf("sleep with a cancelled context = %v, want context.Canceled", err)
		}
		if waited := time.Since(start); waited != 3*time.Second {
			t.Errorf("a cancelled sleep waited %v more", waited-3*time.Second)
		}
		// A bubble's clock starts at midnight UTC on 2000-01-01
		// (testing/synctest).
		if got := f.today(); got != "2000-01-01" {
			t.Errorf("today = %q, want 2000-01-01", got)
		}
	})
}
