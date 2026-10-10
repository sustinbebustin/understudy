package server

import (
	"encoding/json"
	"os"
	"testing"
)

// The health endpoint's model pin is the vendored commit.
func TestModelPinIsTheVendoredCommit(t *testing.T) {
	data, err := os.ReadFile("../../third_party/api-models-aws/UPSTREAM.json")
	if err != nil {
		t.Fatal(err)
	}
	var upstream struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal(data, &upstream); err != nil {
		t.Fatal(err)
	}
	if want := "api-models-aws@" + upstream.Commit; modelPin != want {
		t.Errorf("modelPin = %q, want %q", modelPin, want)
	}
}
