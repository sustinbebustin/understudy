package ssm

import (
	"strings"
	"testing"
)

// A snapshot is a boundary too: a stored type or tier outside the closed
// sets (ssm-parameters.md 4.1, 4.2) fails the import with an error naming
// the value, instead of becoming a parameter no handler can serve.
func TestSnapshotRefusesAnUnknownTypeOrTier(t *testing.T) {
	codec := stateCodec()
	good, err := codec.Marshal(&State{Parameters: map[string]Parameter{"/a": {
		Name:        "/a",
		Versions:    []Version{{Number: 1, Type: typeString, Value: plainValue{Text: "v"}, DataType: defaultDataType, Tier: tierStandard}},
		LastVersion: 1,
		Labels:      map[string]int64{},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var ok State
	if err := codec.Unmarshal(good, &ok); err != nil {
		t.Fatalf("a valid state does not import: %v", err)
	}
	for _, c := range []struct{ from, to string }{
		{`"type":"String"`, "Bogus"},
		{`"tier":"Standard"`, "Intelligent-Tiering"},
	} {
		key, _, _ := strings.Cut(c.from, ":")
		bad := strings.Replace(string(good), c.from, key+`:"`+c.to+`"`, 1)
		if bad == string(good) {
			t.Fatalf("the marshaled state %s holds no %s", good, c.from)
		}
		var st State
		if err := codec.Unmarshal([]byte(bad), &st); err == nil || !strings.Contains(err.Error(), c.to) {
			t.Errorf("import with %s %q = %v, want an error naming the stored value", key, c.to, err)
		}
	}
}
