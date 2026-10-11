package protocoltest_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/protocoltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

var (
	errFailed = errors.New("status: got 500, want 200")
	caseA     = protocoltest.Key{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, ID: "A"}
	caseB     = protocoltest.Key{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, ID: "B"}
	caseC     = protocoltest.Key{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, ID: "C"}
)

// recorded is a baseline in which A and B pass out of three cases.
func recorded() protocoltest.Baseline {
	return protocoltest.Baseline{Suites: []protocoltest.Suite{
		{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, Cases: 3, Passing: []string{"A", "B"}},
	}}
}

func skipC() protocoltest.Skips {
	return protocoltest.Skips{{Key: caseC, Category: protocoltest.CorpusBug{}, Reason: "the request has no target"}}
}

// The ratchet: a case the baseline records as passing that now fails is a
// finding, so the corpus test fails on any regression.
func TestCheckReportsASyntheticRegression(t *testing.T) {
	run := []protocoltest.Result{{Key: caseA}, {Key: caseB, Failure: errFailed}, {Key: caseC, Failure: errFailed}}
	got := recorded().Check(run, skipC())
	want := []protocoltest.Finding{protocoltest.Regressed{Key: caseB, Failure: errFailed}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Check = %v, want %v", got, want)
	}
}

// A run that matches the baseline, with the one failing case skipped, has
// no findings.
func TestCheckAcceptsAMatchingRun(t *testing.T) {
	run := []protocoltest.Result{{Key: caseA}, {Key: caseB}, {Key: caseC, Failure: errFailed}}
	if got := recorded().Check(run, skipC()); len(got) != 0 {
		t.Errorf("Check = %v, want no findings", got)
	}
}

// The baseline can only grow: a passing case it lacks, a failing case
// without a skip, a skip that passes, a name the corpus lost and a suite
// whose size changed are all findings.
func TestCheckReportsEveryDisagreement(t *testing.T) {
	caseD := protocoltest.Key{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, ID: "D"}
	stale := protocoltest.Key{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, ID: "Gone"}
	newSuite := protocoltest.Key{Protocol: ref.RESTXML, Kind: protocoltest.Response, ID: "E"}
	skips := append(skipC(), protocoltest.Skip{Key: stale, Category: protocoltest.ClientOnly{}, Reason: "host prefix"})
	run := []protocoltest.Result{
		{Key: caseA},
		{Key: caseB},
		{Key: caseC},
		{Key: caseD, Failure: errFailed},
		{Key: newSuite},
	}
	got := recorded().Check(run, skips)
	want := []protocoltest.Finding{
		protocoltest.Recounted{Protocol: ref.AWSJSON10, Kind: protocoltest.Request, Ran: 4, Recorded: 3},
		protocoltest.StaleSkip{Key: caseC},
		protocoltest.Unexplained{Key: caseD, Failure: errFailed},
		protocoltest.Unknown{Key: stale, In: "skips.json"},
		protocoltest.Recounted{Protocol: ref.RESTXML, Kind: protocoltest.Response, Ran: 1},
		protocoltest.Unrecorded{Key: newSuite},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Check =\n%v\nwant\n%v", got, want)
	}
}

// A run recorded as a baseline holds no findings against itself.
func TestFromRunRecordsTheRun(t *testing.T) {
	run := []protocoltest.Result{{Key: caseB}, {Key: caseA}, {Key: caseC, Failure: errFailed}}
	b := protocoltest.FromRun(run)
	if !reflect.DeepEqual(b, recorded()) {
		t.Errorf("FromRun = %+v, want %+v", b, recorded())
	}
	if got := b.Check(run, skipC()); len(got) != 0 {
		t.Errorf("Check against itself = %v", got)
	}
}

// Encode writes what ParseBaseline reads back, whatever the ids.
func TestBaselineEncodeRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var b protocoltest.Baseline
		for _, p := range rapid.SliceOfDistinct(rapid.SampledFrom([]ref.ProtocolID{ref.AWSJSON10, ref.AWSQuery, ref.RESTXML}), rapid.ID).Draw(t, "protocols") {
			for _, k := range rapid.SliceOfDistinct(rapid.SampledFrom(protocoltest.Kinds()), rapid.ID).Draw(t, "kinds") {
				passing := rapid.SliceOfDistinct(rapid.String(), rapid.ID).Draw(t, "passing")
				slices.Sort(passing)
				extra := rapid.IntRange(0, 5).Draw(t, "failing")
				b.Suites = append(b.Suites, protocoltest.Suite{Protocol: p, Kind: k, Cases: len(passing) + extra, Passing: passing})
			}
		}
		got, err := protocoltest.ParseBaseline(b.Encode())
		if err != nil {
			t.Fatalf("ParseBaseline(Encode()) = %v\n%s", err, b.Encode())
		}
		normalize(&got)
		normalize(&b)
		if !reflect.DeepEqual(got, b) {
			t.Fatalf("round trip = %+v, want %+v", got, b)
		}
	})
}

// normalize orders suites and makes an empty passing list nil, as the
// document does not tell nil from empty.
func normalize(b *protocoltest.Baseline) {
	for i := range b.Suites {
		if len(b.Suites[i].Passing) == 0 {
			b.Suites[i].Passing = nil
		}
	}
	slices.SortFunc(b.Suites, func(x, y protocoltest.Suite) int {
		return strings.Compare(string(x.Protocol)+" "+string(x.Kind), string(y.Protocol)+" "+string(y.Kind))
	})
}

// An entry without a reason, with an unknown category, an aws-differs
// entry without evidence, a label on another category and a case listed
// twice are each refused, naming the entry.
func TestParseSkipsRefusesBadEntries(t *testing.T) {
	entry := func(fields string) string {
		return `{"protocol": "awsjson1_0", "kind": "request", "id": "A", ` + fields + `}`
	}
	for name, doc := range map[string]string{
		"no reason":          `{"skips": [` + entry(`"category": "corpus-bug"`) + `]}`,
		"unknown category":   `{"skips": [` + entry(`"category": "flaky", "reason": "r"`) + `]}`,
		"no label":           `{"skips": [` + entry(`"category": "aws-differs", "source": "s", "reason": "r"`) + `]}`,
		"no source":          `{"skips": [` + entry(`"category": "aws-differs", "label": "aws-doc", "reason": "r"`) + `]}`,
		"label on corpus":    `{"skips": [` + entry(`"category": "corpus-bug", "label": "aws-doc", "reason": "r"`) + `]}`,
		"unknown kind":       `{"skips": [{"protocol": "awsjson1_0", "kind": "event", "id": "A", "category": "gap", "reason": "r"}]}`,
		"listed twice":       `{"skips": [` + entry(`"category": "gap", "reason": "r"`) + `, ` + entry(`"category": "gap", "reason": "s"`) + `]}`,
		"unknown member":     `{"skips": [` + entry(`"category": "gap", "reason": "r", "note": "x"`) + `]}`,
		"not a skip list":    `[]`,
		"label not in vocab": `{"skips": [` + entry(`"category": "aws-differs", "label": "recorded", "source": "s", "reason": "r"`) + `]}`,
	} {
		var target *protocoltest.SkipsError
		if _, err := protocoltest.ParseSkips([]byte(doc)); !errors.As(err, &target) {
			t.Errorf("%s: ParseSkips = %v, want *SkipsError", name, err)
		}
	}
}

// Each category reads back as its variant; aws-differs keeps its evidence.
func TestParseSkipsReadsEveryCategory(t *testing.T) {
	doc := `{"skips": [
		{"protocol": "restxml", "kind": "response", "id": "D", "category": "aws-differs", "label": "aws-doc", "source": "https://docs.aws.amazon.com/x", "reason": "r"},
		{"protocol": "awsjson1_0", "kind": "request", "id": "A", "category": "corpus-bug", "reason": "r"},
		{"protocol": "awsjson1_0", "kind": "request", "id": "B", "category": "client-only", "reason": "r"},
		{"protocol": "awsjson1_0", "kind": "malformed", "id": "C", "category": "gap", "reason": "r"}
	]}`
	got, err := protocoltest.ParseSkips([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := []protocoltest.Category{
		protocoltest.CorpusBug{},
		protocoltest.ClientOnly{},
		protocoltest.Gap{},
		protocoltest.AWSDiffers{Label: evidence.AWSDoc, Source: "https://docs.aws.amazon.com/x"},
	}
	if len(got) != len(want) {
		t.Fatalf("%d skips, want %d", len(got), len(want))
	}
	for i, s := range got {
		if s.Category != want[i] {
			t.Errorf("entry %d (%s) category = %#v, want %#v", i, s.Key, s.Category, want[i])
		}
	}
	if s, ok := got.Lookup(protocoltest.Key{Protocol: ref.AWSJSON10, Kind: protocoltest.Malformed, ID: "C"}); !ok || s.Category != (protocoltest.Gap{}) {
		t.Errorf("Lookup(C) = %+v, %v", s, ok)
	}
}

// The checked-in files parse, and every skip says why.
func TestCheckedInFilesParse(t *testing.T) {
	if _, err := protocoltest.CheckedInBaseline(); err != nil {
		t.Error(err)
	}
	skips, err := protocoltest.CheckedInSkips()
	if err != nil {
		t.Fatal(err)
	}
	if len(skips) == 0 {
		t.Error("skips.json is empty")
	}
}
