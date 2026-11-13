package s3_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// checksumRequiredNote is the sentence s3-ssec-object-lock.md 11.1 gives
// every bound operation whose model carries requestChecksumRequired.
const checksumRequiredNote = "a request with neither Content-MD5 nor an x-amz-checksum-* header is accepted, and so is one with a wrong checksum; what AWS answers is unrecorded"

// coverageNote is the note of a coverage mark, empty for Full.
func coverageNote(c registry.BindCoverage) string {
	switch c := c.(type) {
	case registry.Partial:
		return c.Note
	case registry.StoredOnly:
		return c.Note
	case registry.Full:
		return ""
	}
	return ""
}

// TestChecksumRequiredCoverageNotes: each bound S3 operation whose model
// carries aws.protocols#httpChecksum{requestChecksumRequired: true} says in
// its coverage note that understudy does not enforce the requirement, and no
// other operation says it (s3-ssec-object-lock.md 8, 11.1; s3.md 5.11).
func TestChecksumRequiredCoverageNotes(t *testing.T) {
	raw, err := os.ReadFile("../../../third_party/api-models-aws/models/s3/service/2006-03-01/s3-2006-03-01.json")
	if err != nil {
		t.Fatalf("read the vendored S3 model: %v", err)
	}
	var model struct {
		Shapes map[string]struct {
			Type   string                     `json:"type"`
			Traits map[string]json.RawMessage `json:"traits"`
		} `json:"shapes"`
	}
	if err := json.Unmarshal(raw, &model); err != nil {
		t.Fatalf("parse the vendored S3 model: %v", err)
	}
	required := map[string]bool{}
	for id, shape := range model.Shapes {
		if shape.Type != "operation" {
			continue
		}
		var trait struct {
			Required bool `json:"requestChecksumRequired"`
		}
		if raw, ok := shape.Traits["aws.protocols#httpChecksum"]; ok {
			if err := json.Unmarshal(raw, &trait); err != nil {
				t.Fatalf("%s: parse httpChecksum: %v", id, err)
			}
		}
		required[id] = trait.Required
	}
	if n := countTrue(required); n != 25 {
		t.Fatalf("the model has %d operations with requestChecksumRequired, want 25 (s3-ssec-object-lock.md 8)", n)
	}
	run(t, s3.Options{}, func(h *harness) {
		t := h.T
		svc, ok := h.Kernel.Registry().Service("s3")
		if !ok {
			t.Fatal("no service s3")
		}
		for _, op := range svc.Operations {
			bound, isBound := h.Kernel.Bindings().Status(op).(kernel.Bound)
			if !isBound {
				continue
			}
			has := strings.Contains(coverageNote(bound.Coverage), checksumRequiredNote)
			if want := required[string(op.Shape)]; has != want {
				t.Errorf("s3.%s: note has the checksum sentence = %t, want %t (requestChecksumRequired in the model); coverage = %#v", op.ID.Name, has, want, bound.Coverage)
			}
		}
	})
}

func countTrue(m map[string]bool) int {
	n := 0
	for _, v := range m {
		if v {
			n++
		}
	}
	return n
}
