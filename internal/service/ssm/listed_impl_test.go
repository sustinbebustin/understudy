package ssm

import "testing"

// With a Label filter GetParametersByPath carries the labeled version, not
// the latest (ssm-parameters.md 7.4, labels guide); when the filter's
// labels sit on several versions the newest of them answers, and a
// parameter without the label is left out. UNVERIFIED (SSM-19),
// ssm-parameters.md 7.4.
func TestListedVersionUnderALabelFilter(t *testing.T) {
	p := Parameter{
		Name:     "/l/p",
		Versions: []Version{{Number: 1}, {Number: 2}, {Number: 3}},
		Labels:   map[string]int64{"old": 1, "mid": 2},
	}
	filter := func(labels ...string) []predicate {
		f, err := labelFilter(optEquals, labels)
		if err != nil {
			t.Fatal(err)
		}
		return []predicate{f}
	}
	for _, c := range []struct {
		labels []string
		want   int64
		ok     bool
	}{
		{[]string{"old"}, 1, true},
		{[]string{"old", "mid"}, 2, true},
		{[]string{"none"}, 0, false},
	} {
		v, ok := p.listed(filter(c.labels...), true)
		if ok != c.ok || v.Number != c.want {
			t.Errorf("labels %v = version %d, %v; want %d, %v", c.labels, v.Number, ok, c.want, c.ok)
		}
	}
	if v, ok := p.listed(nil, false); !ok || v.Number != 3 {
		t.Errorf("no Label filter = version %d, %v; want the latest, 3", v.Number, ok)
	}
}
