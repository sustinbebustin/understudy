package ssm

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 1

// State is the Parameter Store state of one account and region
// (ssm-parameters.md 12.3). Records are immutable values replaced on
// change (ADR 0005), but a Parameter read out of a cell shares storage
// with it: its Versions, Labels and Tags, and the pointers and ciphertext
// bytes inside a Version. None of these is ever changed in place; a
// writer clones the slice or map it changes and stores the new Parameter
// back.
type State struct {
	// Parameters are by canonical name: a single-level name without its
	// leading slash, a hierarchical name as written (ssm-parameters.md 3.2).
	Parameters map[string]Parameter `json:"parameters"`
}

func newState(aws.Scope) State { return State{Parameters: map[string]Parameter{}} }

// IsZero reports a scope without parameters, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Parameters) == 0 }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// Parameter is one parameter: its retained versions, its labels and tags.
type Parameter struct {
	// Name is the canonical name, the key in State.Parameters.
	Name string `json:"name"`
	// Versions are the retained versions, oldest first, at most 100
	// (ssm-parameters.md 6.1).
	Versions []Version `json:"versions"`
	// LastVersion is the number the newest version was given. It only
	// grows: dropping the oldest of 100 versions does not reuse numbers.
	LastVersion int64 `json:"lastVersion"`
	// Labels maps each label to the one version it names, so a label on
	// two versions of a parameter cannot be represented
	// (ssm-parameters.md 6.2).
	Labels map[string]int64 `json:"labels"`
	// Tags are in the order they were first set; at most 50.
	Tags []Tag `json:"tags"`
}

// Version is one version of a parameter, as GetParameterHistory reports it.
type Version struct {
	Number int64     `json:"number"`
	Type   paramType `json:"type"`
	// Value is plainValue for String and StringList, sealedValue for
	// SecureString.
	Value          value         `json:"value"`
	Description    *string       `json:"description,omitempty"`
	AllowedPattern *string       `json:"allowedPattern,omitempty"`
	DataType       string        `json:"dataType"`
	Tier           tier          `json:"tier"`
	LastModified   clock.Instant `json:"lastModified"`
	// LastModifiedUser is the caller's principal ARN as SigV4 resolved it.
	LastModifiedUser string `json:"lastModifiedUser"`
}

// Tag is one parameter tag.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// paramType is a parameter's type (ssm-parameters.md 4.1), one of the
// three constants below, which are the wire spellings. A value enters only
// through parseType or a snapshot import, which UnmarshalText checks.
type paramType string

const (
	typeString       = paramType(ssmapi.ParameterTypeString)
	typeStringList   = paramType(ssmapi.ParameterTypeStringList)
	typeSecureString = paramType(ssmapi.ParameterTypeSecureString)
)

// parseType is the closed paramType of a request's Type; false for a value
// outside the model's enum, which the generated constraint check refuses
// before a handler runs.
func parseType(t ssmapi.ParameterType) (paramType, bool) {
	switch p := paramType(t); p {
	case typeString, typeStringList, typeSecureString:
		return p, true
	}
	return "", false
}

// UnmarshalText refuses a snapshot whose stored type is not one of the
// three.
func (p *paramType) UnmarshalText(b []byte) error {
	t, ok := parseType(ssmapi.ParameterType(b))
	if !ok {
		return fmt.Errorf("ssm: stored parameter type %q is not String, StringList or SecureString", b)
	}
	*p = t
	return nil
}

// tier is a stored tier, tierStandard or tierAdvanced. Intelligent-Tiering
// is resolved at write time and never stored; a parameter's tier only goes
// from Standard to Advanced (ssm-parameters.md 4.2).
type tier string

// UnmarshalText refuses a snapshot whose stored tier is neither Standard
// nor Advanced.
func (t *tier) UnmarshalText(b []byte) error {
	switch v := tier(b); v {
	case tierStandard, tierAdvanced:
		*t = v
		return nil
	}
	return fmt.Errorf("ssm: stored tier %q is neither Standard nor Advanced", b)
}

// value is a version's value: the text of a String or StringList, or the
// KMS ciphertext of a SecureString.
//
//sumtype:decl
type value interface{ isValue() }

// plainValue is a String or StringList value, stored verbatim: a
// StringList is neither split nor trimmed. UNVERIFIED (SSM-28),
// ssm-parameters.md 4.1.
type plainValue struct {
	Text string `json:"text"`
}

// sealedValue is a Standard SecureString value: the CiphertextBlob KMS
// Encrypt returned under the encryption context PARAMETER_ARN, and the
// KeyId as the request gave it ("alias/aws/ssm" when none was given).
type sealedValue struct {
	Ciphertext []byte `json:"ciphertext"`
	KeyRef     string `json:"keyRef"`
}

func (plainValue) isValue()  {}
func (sealedValue) isValue() {}

// stateCodec is the snapshot codec of State, with the value sum in the
// tagged-union form. A table jsonx refuses is a bug in this file; the codec
// then fails every Export and Import with that error.
func stateCodec() store.Codec {
	u, err := jsonx.NewUnion("ssm.value",
		jsonx.Variant[value, plainValue]("plain"),
		jsonx.Variant[value, sealedValue]("sealed"),
	)
	if err != nil {
		return store.Codec{
			Marshal:   func(any) ([]byte, error) { return nil, err },
			Unmarshal: func([]byte, any) error { return err },
		}
	}
	c := jsonx.NewCodec(u)
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}
