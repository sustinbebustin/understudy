//go:build rulerdiff

package ruler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"testing"
)

// TestJavaRulerDifferential gives every corpus case to understudy, compiled
// for EventBridge, and to AWS's Java Event Ruler (the version pom.xml pins,
// run by testdata/rulerdiff/run.sh), and reports each case where the two
// answer differently and the difference is not a known one. A known
// difference that no longer occurs is reported too, so the list stays true.
func TestJavaRulerDifferential(t *testing.T) {
	cases := diffCases()
	java := runJavaRuler(t, cases)
	known := knownDiffs()
	disagreements := 0
	for i, c := range cases {
		got, want := understudyAnswer(c), java[i]
		k, isKnown := known[c]
		switch {
		case isKnown && k.ruler == rulerVaries:
			t.Logf("pattern %s event %s: understudy %s, Java Ruler %s this run (its answer varies; %s)",
				c.pattern, c.event, got, want, k.reason)
		case got != want && !isKnown:
			disagreements++
			t.Errorf("pattern %s event %s: understudy %s, Java Ruler %s", c.pattern, c.event, got, want)
		case got == want && isKnown:
			t.Errorf("pattern %s event %s: both answer %s, but it is listed as a known difference (%s); remove it from knownDiffs",
				c.pattern, c.event, got, k.reason)
		}
	}
	t.Logf("%d cases, %d known differences, %d new disagreements", len(cases), len(known), disagreements)
}

// knownDiff is a case where understudy answers differently from Java Ruler
// on purpose: why, and whether Ruler's answer is stable.
type knownDiff struct {
	reason string
	ruler  rulerAnswer
}

// knownDiffs are the known differences by case.
func knownDiffs() map[diffCase]knownDiff {
	known := map[diffCase]knownDiff{}
	for _, d := range rulerDefects {
		if d.ruler != rulerAgrees {
			known[diffCase{d.pattern, d.event}] = knownDiff{"Ruler defect not copied (doc.go): " + d.name, d.ruler}
		}
	}
	for _, d := range []struct {
		pattern, event, reason string
	}{
		{
			`{"a":["x"]}`, `{"a":"x"} {}`,
			"rulesForJSONEvent reads the first JSON value and ignores the rest; understudy refuses content after the " +
				"event (event.go); AWS's answer is not recorded (DEFERRED.md, Ruler differential harness)",
		},
		{
			`{"a":[{"exactly":"x"}]}`, `{"a":"x"}`,
			"the EventBridge profile refuses \"exactly\" (doc.go); AWS accepts it (DEFERRED.md, Recorded differences from " +
				"AWS, events/R14/H14-*.json)",
		},
	} {
		known[diffCase{d.pattern, d.event}] = knownDiff{d.reason, rulerDiffers}
	}
	return known
}

// understudyAnswer renders understudy's outcome in the probe's vocabulary.
func understudyAnswer(c diffCase) string {
	p, cerr := Compile([]byte(c.pattern), EventBridge{})
	if cerr != nil {
		return "reject: " + cerr.Detail
	}
	ok, err := Match(p, []byte(c.event))
	var evErr *EventError
	switch {
	case errors.As(err, &evErr):
		return "event error: " + evErr.Detail
	case err != nil:
		return "error: " + err.Error()
	}
	return "match: " + strconv.FormatBool(ok)
}

// javaAnswer is one line of Probe.java's output; exactly one field is set.
type javaAnswer struct {
	Reject     *string `json:"reject"`
	Match      *bool   `json:"match"`
	EventError *string `json:"eventError"`
	Crash      *string `json:"crash"`
}

func (a javaAnswer) String() string {
	switch {
	case a.Reject != nil:
		return "reject: " + *a.Reject
	case a.Match != nil:
		return "match: " + strconv.FormatBool(*a.Match)
	case a.EventError != nil:
		return "event error: " + *a.EventError
	case a.Crash != nil:
		return "crash: " + *a.Crash
	}
	return "no answer"
}

// runJavaRuler runs the probe once over all cases and returns its answers in
// case order.
func runJavaRuler(t *testing.T, cases []diffCase) []string {
	t.Helper()
	var in bytes.Buffer
	enc := json.NewEncoder(&in)
	for _, c := range cases {
		if err := enc.Encode(map[string]string{"pattern": c.pattern, "event": c.event}); err != nil {
			t.Fatalf("encode case %+v: %v", c, err)
		}
	}
	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(t.Context(), "testdata/rulerdiff/run.sh")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = &in, &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Java Ruler probe (testdata/rulerdiff/run.sh) failed: %v; it needs Docker or a JDK 21 with Maven, "+
			"and Maven Central the first time. Its stderr:\n%s", err, stderr.String())
	}
	answers := make([]string, 0, len(cases))
	sc := bufio.NewScanner(&out)
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var a javaAnswer
		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			t.Fatalf("probe answer %d is not JSON: %v: %s", len(answers), err, sc.Bytes())
		}
		answers = append(answers, a.String())
	}
	if len(answers) != len(cases) {
		t.Fatalf("probe answered %d of %d cases; its stderr:\n%s", len(answers), len(cases), stderr.String())
	}
	return answers
}
