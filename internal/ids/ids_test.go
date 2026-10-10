package ids

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

var (
	uuidV4        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	receiptHandle = regexp.MustCompile(`^AQEB[A-Za-z0-9+/]{32}$`)
	uploadID      = regexp.MustCompile(`^[A-Za-z0-9_-]{64}$`)
	versionID     = regexp.MustCompile(`^[A-Za-z0-9._]{32}$`)
	etag          = regexp.MustCompile(`^[0-9a-f]{32}$`)
	secretKey     = regexp.MustCompile(`^[A-Za-z0-9+/]{40}$`)
	sessionToken  = regexp.MustCompile(`^[A-Za-z0-9+/]+$`)
	base32Body    = regexp.MustCompile(`^[A-Z2-7]+$`)
)

func allKinds() []Kind {
	kinds := make([]Kind, 0, kindCount)
	for k := range Kind(kindCount) {
		kinds = append(kinds, k)
	}
	return kinds
}

// draw is one step of a generated draw sequence.
type draw struct {
	kind Kind
	n    uint
}

func drawsGen() *rapid.Generator[[]draw] {
	return rapid.SliceOf(rapid.Custom(func(t *rapid.T) draw {
		return draw{
			kind: rapid.SampledFrom(allKinds()).Draw(t, "kind"),
			n:    rapid.UintRange(0, 40).Draw(t, "n"),
		}
	}))
}

func TestSeededSourcesAgreePerKindAndKindsAreIndependent(t *testing.T) {
	// Same seed and same calls give the same ids, and the draws of one kind do
	// not depend on how draws of other kinds are interleaved with them.
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Uint64().Draw(t, "seed")
		draws := drawsGen().Draw(t, "draws")

		interleaved := NewSeeded(seed)
		again := NewSeeded(seed)
		got := map[Kind][]byte{}
		for i, d := range draws {
			b := interleaved.Draw(d.kind, d.n)
			if b2 := again.Draw(d.kind, d.n); !bytes.Equal(b, b2) {
				t.Fatalf("draw %d of %s: two sources with seed %d drew %x and %x", i, d.kind, seed, b, b2)
			}
			got[d.kind] = append(got[d.kind], b...)
		}

		for _, k := range allKinds() {
			alone := NewSeeded(seed)
			var want []byte
			for _, d := range draws {
				if d.kind == k {
					want = append(want, alone.Draw(k, d.n)...)
				}
			}
			if !bytes.Equal(want, got[k]) {
				t.Fatalf("%s drew %x interleaved with other kinds but %x alone", k, got[k], want)
			}
		}
	})
}

func TestSeekReproducesNextDraws(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Uint64().Draw(t, "seed")
		src := NewSeeded(seed)
		for _, d := range drawsGen().Draw(t, "before") {
			src.Draw(d.kind, d.n)
		}
		positions := mustPositions(t, src)
		after := drawsGen().Draw(t, "after")
		want := make([][]byte, len(after))
		for i, d := range after {
			want[i] = src.Draw(d.kind, d.n)
		}

		restored := NewSeeded(seed)
		restored.Draw(KindJitter, 64) // a stream not in sync with the recording
		if err := restored.Seek(positions); err != nil {
			t.Fatalf("Seek: %v", err)
		}
		for i, d := range after {
			if got := restored.Draw(d.kind, d.n); !bytes.Equal(got, want[i]) {
				t.Fatalf("draw %d of %s after Seek = %x, want %x", i, d.kind, got, want[i])
			}
		}
	})
}

func TestSeedsDiffer(t *testing.T) {
	if NewSeeded(1).RequestID() == NewSeeded(2).RequestID() {
		t.Error("seeds 1 and 2 drew the same first request id")
	}
}

func TestKindsDrawDifferentStreams(t *testing.T) {
	// Each kind's stream is derived from its own name, so with one seed no two
	// kinds draw the same bytes.
	rapid.Check(t, func(t *rapid.T) {
		src := NewSeeded(rapid.Uint64().Draw(t, "seed"))
		first := map[string]Kind{}
		for _, k := range allKinds() {
			b := string(src.Draw(k, 32))
			if prev, dup := first[b]; dup {
				t.Fatalf("kinds %s and %s drew the same first 32 bytes %x", prev, k, b)
			}
			first[b] = k
		}
	})
}

func TestSeededGoldenVectors(t *testing.T) {
	// Snapshots record stream positions and replay them on load, so the
	// stream for (seed, kind) and the formatting of every id must not change
	// between versions. A failure here means old snapshots would replay to
	// different ids: change the derivation only with a new domain-separation
	// prefix and a snapshot format version.
	src := NewSeeded(1)
	key, err := src.AccessKeyID("123456789012")
	if err != nil {
		t.Fatalf("AccessKeyID: %v", err)
	}
	for _, tc := range []struct {
		what, got, want string
	}{
		{"request id", string(src.RequestID()), "a4bc63ce-a60c-4eb8-9e89-83aac447bdc2"},
		{"receipt handle", string(src.ReceiptHandle()), "AQEBMyV9AEH2mkDuT7fJyaw38vwOEpOK5R65"},
		{"upload id", string(src.UploadID()), "nutefeWQyeJdlUWvAdier4mfn-mgRe-_aLTOIDt5-AhIIiuEho6si0QHQcX8vah-"},
		{"version id", string(src.VersionID()), "3fJG7OXqS9x43dBPONK7g2DecvkHcPZQ"},
		{"ETag", string(src.ETag()), "3af1cc4edc99bf6624417cd3b2cbf453"},
		{"role id", string(src.RoleID()), "AROAV35GFBG7AT2QBVERC"},
		{"access key id", string(key), "AKIARZPUZDIKPK3NTIES"},
		{"fault stream word", strconv.FormatUint(src.Uint64(KindFaultStream), 10), "8568185004654249808"},
	} {
		if tc.got != tc.want {
			t.Errorf("NewSeeded(1) %s = %q, want %q", tc.what, tc.got, tc.want)
		}
	}
}

func TestSeekRefusals(t *testing.T) {
	var notSeeded *NotSeededError
	if err := NewRandom().Seek(map[Kind]uint64{KindRequestID: 1}); !errors.As(err, &notSeeded) {
		t.Errorf("Seek on a random source = %v, want *NotSeededError", err)
	}
	if p, err := NewRandom().Positions(); p != nil || !errors.As(err, &notSeeded) {
		t.Errorf("Positions on a random source = %v, %v; want *NotSeededError", p, err)
	}

	src := NewSeeded(7)
	src.Draw(KindETag, 16)
	before := mustPositions(t, src)
	var unknown *UnknownKindError
	if err := src.Seek(map[Kind]uint64{Kind(200): 1}); !errors.As(err, &unknown) {
		t.Errorf("Seek with an unknown kind = %v, want *UnknownKindError", err)
	}
	if now := mustPositions(t, src); !maps.Equal(now, before) {
		t.Errorf("a refused Seek moved the streams: %v, was %v", now, before)
	}

	// A corrupt manifest must not make snapshot load replay for hours.
	var position *PositionError
	err := src.Seek(map[Kind]uint64{KindRequestID: 3, KindETag: MaxPosition + 1})
	if !errors.As(err, &position) || position.Kind != KindETag || position.Pos != MaxPosition+1 {
		t.Errorf("Seek past MaxPosition = %v, want *PositionError for %s at %d", err, KindETag, uint64(MaxPosition+1))
	}
	if now := mustPositions(t, src); !maps.Equal(now, before) {
		t.Errorf("a refused Seek moved the streams: %v, was %v", now, before)
	}
}

func TestPositionsRefusePastMaxPosition(t *testing.T) {
	// Save and load agree on the limit: a stream drawn past MaxPosition is
	// refused when its position is read for a snapshot, not when the
	// snapshot is loaded. Drawing or replaying 2^30 words takes many seconds
	// under -race, so the test sets the word count directly.
	src := NewSeeded(7)
	src.seeded.streams[KindUploadID].pos = MaxPosition
	if p := mustPositions(t, src); p[KindUploadID] != MaxPosition {
		t.Fatalf("position at the limit = %d, want %d", p[KindUploadID], uint64(MaxPosition))
	}

	src.UploadID()
	var limit *PositionLimitError
	if _, err := src.Positions(); !errors.As(err, &limit) || limit.Kind != KindUploadID || limit.Pos <= MaxPosition {
		t.Fatalf("Positions past the limit = %v, want *PositionLimitError for %s", err, KindUploadID)
	}
	if src.UploadID() == "" {
		t.Error("drawing stopped past MaxPosition")
	}
}

// mustPositions returns the source's positions, failing the test when it
// refuses them.
func mustPositions(t interface {
	Helper()
	Fatalf(format string, args ...any)
}, src *Source,
) map[Kind]uint64 {
	t.Helper()
	p, err := src.Positions()
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	return p
}

func TestKindNamesRoundTripThroughJSON(t *testing.T) {
	src := NewSeeded(42)
	for _, k := range allKinds() {
		src.Uint64(k)
	}
	positions := mustPositions(t, src)
	b, err := json.Marshal(positions)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back map[Kind]uint64
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal %s: %v", b, err)
	}
	if !maps.Equal(back, positions) {
		t.Errorf("positions round trip gave %v, want %v", back, positions)
	}
	names := map[string]Kind{}
	for _, k := range allKinds() {
		if prev, dup := names[k.String()]; dup {
			t.Errorf("kinds %d and %d share the name %q", prev, k, k)
		}
		names[k.String()] = k
	}
	var k Kind
	var unknown *UnknownKindError
	if err := k.UnmarshalText([]byte("sqs-message-ids")); !errors.As(err, &unknown) {
		t.Errorf("UnmarshalText of an unknown name = %v, want *UnknownKindError", err)
	}
	if _, err := json.Marshal(map[Kind]uint64{Kind(200): 1}); !errors.As(err, &unknown) || unknown.Name != "kind(200)" {
		t.Errorf("marshaling an out-of-range kind = %v, want *UnknownKindError naming kind(200)", err)
	}
}

func TestFormats(t *testing.T) {
	for name, src := range map[string]*Source{"random": NewRandom(), "seeded": NewSeeded(99)} {
		t.Run(name, func(t *testing.T) {
			checks := []struct {
				what string
				got  string
				re   *regexp.Regexp
			}{
				{"request id", string(src.RequestID()), uuidV4},
				{"SQS message id", string(src.MessageID()), uuidV4},
				{"SNS message id", string(src.SNSMessageID()), uuidV4},
				{"Lambda request id", string(src.LambdaRequestID()), uuidV4},
				{"ESM UUID", string(src.ESMID()), uuidV4},
				{"KMS key id", string(src.KMSKeyID()), uuidV4},
				{"delivery id", string(src.DeliveryID()), uuidV4},
				{"receipt handle", string(src.ReceiptHandle()), receiptHandle},
				{"upload id", string(src.UploadID()), uploadID},
				{"version id", string(src.VersionID()), versionID},
				{"SSE-KMS ETag", string(src.ETag()), etag},
				{"role id", string(src.RoleID()), regexp.MustCompile(`^AROA[A-Z2-7]{17}$`)},
				{"policy id", string(src.PolicyID()), regexp.MustCompile(`^ANPA[A-Z2-7]{17}$`)},
				{"user id", string(src.UserID()), regexp.MustCompile(`^AIDA[A-Z2-7]{17}$`)},
				{"secret access key", string(src.SecretAccessKey()), secretKey},
				{"session token", string(src.SessionToken()), sessionToken},
			}
			for _, c := range checks {
				if !c.re.MatchString(c.got) {
					t.Errorf("%s %q does not match %s", c.what, c.got, c.re)
				}
			}
		})
	}
}

// ParseReceiptHandle accepts exactly the receipt handle format: every
// minted handle, and any string only when it matches the format.
func TestParseReceiptHandle(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		src := NewSeeded(rapid.Uint64().Draw(t, "seed"))
		minted := src.ReceiptHandle()
		if got, ok := ParseReceiptHandle(string(minted)); !ok || got != minted {
			t.Fatalf("ParseReceiptHandle(%q) = %q, %v; want the handle back", minted, got, ok)
		}
		// Strings near the format: the minted handle with one byte changed,
		// cut or extended, or an arbitrary string.
		s := rapid.OneOf(
			rapid.Custom(func(t *rapid.T) string {
				b := []byte(minted)
				b[rapid.IntRange(0, len(b)-1).Draw(t, "at")] = rapid.Byte().Draw(t, "byte")
				return string(b)
			}),
			rapid.Custom(func(t *rapid.T) string {
				return string(minted)[:rapid.IntRange(0, len(minted)-1).Draw(t, "cut")]
			}),
			rapid.Custom(func(t *rapid.T) string { return string(minted) + rapid.String().Draw(t, "tail") }),
			rapid.String(),
		).Draw(t, "s")
		got, ok := ParseReceiptHandle(s)
		if want := receiptHandle.MatchString(s); ok != want || (ok && string(got) != s) {
			t.Fatalf("ParseReceiptHandle(%q) = %q, %v; want ok=%v", s, got, ok, want)
		}
	})
}

func TestRandomSourceDoesNotRepeat(t *testing.T) {
	src := NewRandom()
	seen := make(map[MessageID]struct{}, 10_000)
	for i := range 10_000 {
		id := src.MessageID()
		if _, dup := seen[id]; dup {
			t.Fatalf("draw %d repeated %s", i, id)
		}
		if !uuidV4.MatchString(string(id)) {
			t.Fatalf("draw %d %q is not a v4 UUID", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestMintedKeysDecodeToTheirAccount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		account := fmt.Sprintf("%012d", rapid.Uint64Range(0, maxAccount).Draw(t, "account"))
		src := NewSeeded(rapid.Uint64().Draw(t, "seed"))
		for prefix, mint := range map[string]func(string) (AccessKeyID, error){
			"AKIA": src.AccessKeyID,
			"ASIA": src.TemporaryAccessKeyID,
		} {
			key, err := mint(account)
			if err != nil {
				t.Fatalf("minting a %s key for %s: %v", prefix, account, err)
			}
			if len(key) != 20 || !strings.HasPrefix(string(key), prefix) || !base32Body.MatchString(string(key[4:])) {
				t.Fatalf("minted %q, want %s and 16 base32 characters", key, prefix)
			}
			if !strings.ContainsRune("QRSTUVWXYZ234567", rune(key[4])) {
				t.Fatalf("minted %q without the new-generation bit", key)
			}
			if got, ok := DecodeAccount(string(key)); !ok || got != account {
				t.Fatalf("DecodeAccount(%q) = %q, %v; want %s", key, got, ok, account)
			}
		}
	})
}

func TestDecodeAccount(t *testing.T) {
	// Vectors from iam-sts-kms.md 2.2: two real-format keys and two built
	// with the documented layout.
	for _, tc := range []struct {
		key     string
		account string
		ok      bool
	}{
		{"AKIAIOSFODNN7EXAMPLE", "581039954779", true},
		{"ASIAQNZGKIQY56JQ7WML", "029608264753", true},
		{"AKIARZPUZDIKNGW42DFU", "123456789012", true},
		{"ASIAQAAAAAAAFMLBRZQW", "000000000000", true},
		{"AKIAI44QH8DHBEXAMPLE", "", false}, // '8' is not base32
		{"AROAQAAAAAAAFMLBRZQW", "", false}, // not an access key prefix
		{"AKIAQAAAAAAAFMLBRZQ", "", false},  // too short
		{"test", "", false},
		{"112233445566", "", false},
		{"AKIA7777777777777777", "", false}, // encodes more than 12 digits
	} {
		got, ok := DecodeAccount(tc.key)
		if got != tc.account || ok != tc.ok {
			t.Errorf("DecodeAccount(%q) = %q, %v; want %q, %v", tc.key, got, ok, tc.account, tc.ok)
		}
	}
}

// Keys minted from one seed under two accounts sort in the same mint order
// under CompareAccessKeys, although the account's lowest bit shares a base32
// character with random bits and can reorder the raw ids.
func TestCompareAccessKeysIgnoresTheAccount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Uint64().Draw(t, "seed")
		n := rapid.IntRange(2, 8).Draw(t, "n")
		accounts := rapid.SliceOfNDistinct(rapid.StringMatching(`[0-9]{12}`), 2, 2, rapid.ID[string]).Draw(t, "accounts")
		order := func(account string) []int {
			src := NewSeeded(seed)
			keys := make([]AccessKeyID, n)
			for i := range keys {
				mint := src.AccessKeyID
				if i%2 == 1 {
					mint = src.TemporaryAccessKeyID
				}
				k, err := mint(account)
				if err != nil {
					t.Fatalf("mint under %s: %v", account, err)
				}
				keys[i] = k
			}
			idx := make([]int, n)
			for i := range idx {
				idx[i] = i
			}
			slices.SortFunc(idx, func(a, b int) int { return CompareAccessKeys(keys[a], keys[b]) })
			return idx
		}
		if a, b := order(accounts[0]), order(accounts[1]); !slices.Equal(a, b) {
			t.Fatalf("mint order of sorted keys under %s = %v, under %s = %v", accounts[0], a, accounts[1], b)
		}
	})
}

func TestMintingRefusesInvalidAccounts(t *testing.T) {
	src := NewSeeded(1)
	for _, account := range []string{"", "12345678901", "1234567890123", "12345678901a", "+12345678901", "١٢٣٤٥٦٧٨٩٠١٢"} {
		var invalid *InvalidAccountError
		if key, err := src.AccessKeyID(account); !errors.As(err, &invalid) {
			t.Errorf("AccessKeyID(%q) = %q, %v; want *InvalidAccountError", account, key, err)
		}
	}
}

func TestSecretsAreRedactedInLogs(t *testing.T) {
	src := NewSeeded(1)
	secret, token := src.SecretAccessKey(), src.SessionToken()
	var out bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&out, nil))
	log.Info("minted credentials", slog.Any("secret", secret), slog.Any("token", token))
	for what, raw := range map[string]string{"secret access key": string(secret), "session token": string(token)} {
		if strings.Contains(out.String(), raw) {
			t.Errorf("the %s %q reached the log: %s", what, raw, out.String())
		}
	}
	if n := strings.Count(out.String(), `"[REDACTED]"`); n != 2 {
		t.Errorf("log has %d redacted values, want 2: %s", n, out.String())
	}
}
