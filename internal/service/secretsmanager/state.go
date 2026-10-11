package secretsmanager

import (
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 1

// State is the Secrets Manager state of one account and region. Records are
// values: a handler copies a Secret out, changes the copy and stores it
// back, and clones the maps it changes, so nothing read out of a cell
// aliases it.
type State struct {
	// Secrets are by name; a name is unique in an account and region,
	// including the names of secrets scheduled for deletion.
	Secrets map[string]Secret `json:"secrets"`
}

func newState(aws.Scope) State { return State{Secrets: map[string]Secret{}} }

// IsZero reports a scope without secrets, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Secrets) == 0 }

// Secret is one secret: its metadata, its versions and which version holds
// each staging label.
type Secret struct {
	Name string `json:"name"`
	// ARN ends in "-" and six random characters, so a secret recreated with
	// the same name has another ARN.
	ARN string `json:"arn"`
	// CreateToken is the ClientRequestToken CreateSecret was given, which a
	// retried CreateSecret repeats.
	CreateToken string  `json:"createToken"`
	Description *string `json:"description,omitempty"`
	// KMSKeyID is the KmsKeyId as the caller sent it; nil is the AWS
	// managed key aws/secretsmanager, which DescribeSecret does not show.
	KMSKeyID *string       `json:"kmsKeyId,omitempty"`
	Type     *string       `json:"type,omitempty"`
	Created  clock.Instant `json:"created"`
	Changed  clock.Instant `json:"changed"`
	// Accessed is the day of the last GetSecretValue, nil before the first.
	Accessed *clock.Instant `json:"accessed,omitempty"`
	// Tags are in the order they were first set. Nil means the secret was
	// never tagged, which DescribeSecret shows as no Tags member; empty means
	// every tag was removed, which it shows as [].
	Tags      []Tag     `json:"tags"`
	Policy    *string   `json:"policy,omitempty"`
	Lifecycle lifecycle `json:"lifecycle"`
	// Versions are by version id, which is the ClientRequestToken that made
	// the version.
	Versions map[string]Version `json:"versions"`
	// Stages maps each staging label to the one version that holds it, so a
	// label on two versions cannot be represented.
	Stages map[string]string `json:"stages"`
}

// Tag is one secret tag.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Version is one secret value, sealed under a data key KMS generated.
type Version struct {
	ID      string        `json:"id"`
	Created clock.Instant `json:"created"`
	// Accessed is the day of the last GetSecretValue of this version.
	Accessed *clock.Instant `json:"accessed,omitempty"`
	// KMSKeyID is the secret's KMSKeyID when the version was made, which
	// ListSecretVersionIds reports.
	KMSKeyID *string `json:"kmsKeyId,omitempty"`
	Value    sealed  `json:"value"`
}

// labels returns the staging labels on version id, sorted, as AWS lists
// them; nil when the version has none (deprecated).
func (s *Secret) labels(id string) []string {
	var out []string
	for _, label := range slices.Sorted(maps.Keys(s.Stages)) {
		if s.Stages[label] == id {
			out = append(out, label)
		}
	}
	return out
}

// versionsToStages maps every labeled version to its labels, nil when no
// version has a label.
func (s *Secret) versionsToStages() map[string][]string {
	var out map[string][]string
	for _, id := range slices.Sorted(maps.Keys(s.Versions)) {
		if labels := s.labels(id); labels != nil {
			if out == nil {
				out = map[string][]string{}
			}
			out[id] = labels
		}
	}
	return out
}

// ownMaps gives s its own copies of the version and stage maps, so a copy
// of a stored secret can change them without the change showing through
// the stored value.
func (s *Secret) ownMaps() {
	s.Versions = maps.Clone(s.Versions)
	if s.Versions == nil {
		s.Versions = map[string]Version{}
	}
	s.Stages = maps.Clone(s.Stages)
	if s.Stages == nil {
		s.Stages = map[string]string{}
	}
}

// lifecycle says whether a secret is usable or scheduled for deletion; the
// deletion date exists only on the scheduled state.
//
//sumtype:decl
type lifecycle interface{ isLifecycle() }

type active struct{}

type scheduled struct {
	// DeletionDate is when the secret is deleted for good.
	DeletionDate clock.Instant `json:"deletionDate"`
}

func (active) isLifecycle()    {}
func (scheduled) isLifecycle() {}

// stateCodec is the snapshot codec of State, with the lifecycle sum in the
// tagged-union form. A table jsonx refuses is a bug in this file; the codec
// then fails every Export and Import with that error.
func stateCodec() store.Codec {
	u, err := jsonx.NewUnion("secretsmanager.lifecycle",
		jsonx.Variant[lifecycle, active]("active"),
		jsonx.Variant[lifecycle, scheduled]("scheduled"),
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
