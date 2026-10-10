package evidence

import "fmt"

// Label says what backs a behavior: the one vocabulary for overlay entries,
// fidelity switches, protocol-test skip entries and parity fixtures.
type Label string

// The five labels, strongest evidence first.
const (
	AWSRecorded        Label = "aws-recorded"         // captured from real AWS by the project
	ThirdPartyRecorded Label = "third-party-recorded" // captured from real AWS by another project (LocalStack snapshots)
	AWSDoc             Label = "aws-doc"              // stated in AWS documentation
	SDKSource          Label = "sdk-source"           // read from an AWS SDK's source
	Inferred           Label = "inferred"             // reasoned from the above without direct evidence
)

// Labels returns every label, strongest evidence first.
func Labels() []Label {
	return []Label{AWSRecorded, ThirdPartyRecorded, AWSDoc, SDKSource, Inferred}
}

// LabelError reports text that names no label.
type LabelError struct {
	Text string
}

func (e *LabelError) Error() string {
	return fmt.Sprintf("evidence label %q is unknown; use one of %q", e.Text, Labels())
}

// ParseLabel returns the label spelled s.
func ParseLabel(s string) (Label, error) {
	for _, l := range Labels() {
		if string(l) == s {
			return l, nil
		}
	}
	return "", &LabelError{Text: s}
}
