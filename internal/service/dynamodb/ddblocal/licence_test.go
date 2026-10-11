package ddblocal_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

func openRoot(t *testing.T) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	return root
}

// TestLicenceIsAwaitedUntilAccepted: without the flag or a recorded
// acceptance nothing may be pulled (ADR 0014).
func TestLicenceIsAwaitedUntilAccepted(t *testing.T) {
	for name, dir := range map[string]*os.Root{"no data dir": nil, "empty data dir": openRoot(t)} {
		got, err := ddblocal.LoadLicence(dir, false)
		if err != nil {
			t.Fatalf("%s: LoadLicence = %v", name, err)
		}
		if _, ok := got.(ddblocal.AwaitingLicence); !ok {
			t.Errorf("%s: LoadLicence = %#v, want AwaitingLicence", name, got)
		}
	}
}

// TestAcceptanceIsRecordedPerDigest: accepting once with a data directory
// covers later runs without the flag, for the pinned digest.
func TestAcceptanceIsRecordedPerDigest(t *testing.T) {
	dir := openRoot(t)
	got, err := ddblocal.LoadLicence(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	want := ddblocal.Accepted{Digest: ddblocal.Digest}
	if got != want {
		t.Fatalf("LoadLicence(accept) = %#v, want %#v", got, want)
	}
	again, err := ddblocal.LoadLicence(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if again != want {
		t.Errorf("a later LoadLicence without the flag = %#v, want the recorded %#v", again, want)
	}
	record, err := dir.ReadFile("dynamodb-local/licence-accepted/" + strings.ReplaceAll(ddblocal.Digest, ":", "-"))
	if err != nil {
		t.Fatalf("the acceptance record is missing: %v", err)
	}
	if got := string(record); got != ddblocal.Reference()+"\n" {
		t.Errorf("acceptance record = %q, want the pinned reference", got)
	}
}

// TestAcceptanceOfAnotherDigestDoesNotCount: a bumped pin needs a fresh
// acceptance, so an older digest's record is ignored.
func TestAcceptanceOfAnotherDigestDoesNotCount(t *testing.T) {
	dir := openRoot(t)
	if err := dir.MkdirAll("dynamodb-local/licence-accepted", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := dir.WriteFile("dynamodb-local/licence-accepted/sha256-0000", []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ddblocal.LoadLicence(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.(ddblocal.AwaitingLicence); !ok {
		t.Errorf("LoadLicence = %#v, want AwaitingLicence", got)
	}
}

// TestAcceptanceThatCannotBeRecorded: an acceptance given now that cannot
// be saved says it was given and which directory to make writable; a
// recorded one that cannot be read says how to accept again.
func TestAcceptanceThatCannotBeRecorded(t *testing.T) {
	tests := []struct {
		accept bool
		op     ddblocal.AcceptanceOp
		want   []string
		unwant string
	}{
		{
			accept: true, op: ddblocal.AcceptanceRecord{},
			want:   []string{"the DynamoDB Local licence was accepted, but the acceptance could not be saved", "dynamodb-local/licence-accepted writable", "nothing has been pulled"},
			unwant: ddblocal.AcceptFlag,
		},
		{
			accept: false, op: ddblocal.AcceptanceRead{},
			want: []string{"cannot read the DynamoDB Local licence acceptance", "pass " + ddblocal.AcceptFlag + " to accept again", "nothing has been pulled"},
		},
	}
	for _, tt := range tests {
		dir := openRoot(t)
		if err := dir.WriteFile("dynamodb-local", []byte("a file where the directory goes"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ddblocal.LoadLicence(dir, tt.accept)
		ae, ok := errors.AsType[*ddblocal.AcceptanceError](err)
		if !ok || got != nil {
			t.Fatalf("LoadLicence(accept %t) = %#v, %v; want only an *AcceptanceError", tt.accept, got, err)
		}
		if ae.Op != tt.op || ae.Dir != dir.Name() {
			t.Errorf("LoadLicence(accept %t): Op, Dir = %T, %q; want %T, %q", tt.accept, ae.Op, ae.Dir, tt.op, dir.Name())
		}
		for _, want := range tt.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("LoadLicence(accept %t): error %q lacks %q", tt.accept, err, want)
			}
		}
		if tt.unwant != "" && strings.Contains(err.Error(), tt.unwant) {
			t.Errorf("LoadLicence(accept %t): error %q tells the user to pass %s, which they did", tt.accept, err, tt.unwant)
		}
	}
}

// TestNoticeNamesImageLicenceAndHowToAccept: the 501 and the banner name
// the exact flag and environment variable (engineering m15, product M4).
func TestNoticeNamesImageLicenceAndHowToAccept(t *testing.T) {
	notice := ddblocal.Notice()
	for _, want := range []string{
		"amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab",
		"Amazon DynamoDB Local License Agreement",
		`"You may
not use the Software if you do not have an AWS account in good standing."`,
		"telemetry turned off",
		"--accept-dynamodb-local-license",
		"UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("Notice lacks %q:\n%s", want, notice)
		}
	}
	if strings.Contains(notice, "[y/N]") {
		t.Error("Notice prompts; understudy never prompts")
	}
}

func TestBannerLine(t *testing.T) {
	tests := []struct {
		licence ddblocal.Licence
		want    string
	}{
		{ddblocal.AwaitingLicence{}, "DynamoDB: disabled until you accept the DynamoDB Local licence: start with --accept-dynamodb-local-license or set UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1"},
		{ddblocal.Accepted{Digest: "sha256:0000"}, "DynamoDB: disabled until you accept the DynamoDB Local licence: start with --accept-dynamodb-local-license or set UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1"},
		{ddblocal.Accepted{Digest: ddblocal.Digest}, "DynamoDB: DynamoDB Local licence accepted for " + ddblocal.Reference()},
		{ddblocal.Disabled{}, "DynamoDB: disabled by configuration"},
	}
	for _, tt := range tests {
		if got := ddblocal.BannerLine(tt.licence); got != tt.want {
			t.Errorf("BannerLine(%#v) = %q, want %q", tt.licence, got, tt.want)
		}
	}
}
