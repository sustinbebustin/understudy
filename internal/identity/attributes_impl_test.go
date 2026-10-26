package identity_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// Only an assumed role has session attributes: Import refuses a root or user
// record that carries them, since a later Export would drop them, and keeps
// the keys it held.
func TestImportRefusesAttributesOnARootOrUser(t *testing.T) {
	for _, p := range []identity.Principal{
		identity.Root{Account: account(t, "123456789012")},
		identity.User{Account: account(t, "123456789012"), Path: "/", Name: "ana", ID: "AIDAANAEXAMPLE000001"},
	} {
		src := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
		mint(t, src, p, identity.LongTerm{})
		out, err := src.Export()
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Version int `json:"version"`
			Keys    []struct {
				AccessKeyID     string          `json:"accessKeyId"`
				SecretAccessKey string          `json:"secretAccessKey"`
				Lifetime        json.RawMessage `json:"lifetime"`
				Principal       map[string]any  `json:"principal"`
			} `json:"keys"`
		}
		if err := json.Unmarshal(out, &doc); err != nil || len(doc.Keys) != 1 {
			t.Fatalf("Export = %s (%v), want one key", out, err)
		}
		doc.Keys[0].Principal["attributes"] = map[string]any{"sourceIdentity": "ana"}
		edited, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}

		dst := identity.New(ids.NewSeeded(2), aws.DefaultAccount())
		held := mint(t, dst, identity.Root{Account: aws.DefaultAccount()}, identity.LongTerm{})
		err = dst.Import(edited)
		if _, ok := errors.AsType[*identity.ImportError](err); !ok || !strings.Contains(err.Error(), "has no attributes") {
			t.Errorf("Import of a %s record with attributes = %v, want an *ImportError naming the stray attributes", identity.Render(p), err)
		}
		if got, _ := dst.Resolve(string(held.AccessKeyID)); got != (identity.Root{Account: aws.DefaultAccount()}) {
			t.Errorf("after the refused Import, the held key resolves to %#v, want the root it was minted for", got)
		}
	}
}

// An issue time RFC 3339 cannot hold would make every later Export fail, so
// MintTemporary refuses it and mints nothing.
func TestMintTemporaryRefusesAnIssueTimeASnapshotCannotSave(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	role := identity.AssumedRole{
		Account: account(t, "123456789012"), Role: identity.RoleRef{Name: "app", Path: "/", ID: "AROA1"},
		Session: "s1", Source: identity.FromAssumeRole{},
		Attributes: &identity.SessionAttributes{Issued: clock.FromWire(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))},
	}
	_, err := s.MintTemporary(role, time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC))
	if me, ok := errors.AsType[*identity.MintError](err); !ok || me.Problem != identity.InvalidIssueTime {
		t.Fatalf("MintTemporary with issue time in year 10000 = %v, want a *MintError with InvalidIssueTime", err)
	}
	out, err := s.Export()
	if err != nil {
		t.Fatalf("Export after the refused mint = %v", err)
	}
	if !bytes.Contains(out, []byte(`"keys":[]`)) {
		t.Errorf("Export after the refused mint = %s, want no keys", out)
	}
}
