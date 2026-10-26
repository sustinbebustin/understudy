package cli

import (
	"strings"
	"testing"
)

// A setting the file holds in a section is shown in the form the file
// accepts: {"iam": {"enforce": ...}}, since a top-level "iam.enforce" is
// refused.
func TestServeHelpNamesASectionSettingsFileForm(t *testing.T) {
	var help strings.Builder
	if err := serveFlags(&help); err != nil {
		t.Fatal(err)
	}
	if text := help.String(); !strings.Contains(text, `file key "enforce" in the "iam" section`) || strings.Contains(text, `file key "iam.enforce"`) {
		t.Errorf("serve --help names iam.enforce's file key as:\n%s\nwant file key \"enforce\" in the \"iam\" section", text)
	}
}
