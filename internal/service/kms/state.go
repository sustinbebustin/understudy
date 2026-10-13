package kms

import (
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 1

// State is the KMS state of one account and region. Records are values,
// replaced on change and never mutated in place, so a value read out of a
// cell never aliases the cell.
type State struct {
	// Keys are by key id.
	Keys map[string]Key `json:"keys"`
	// Aliases are by name, "alias/" included.
	Aliases map[string]Alias `json:"aliases"`
}

func newState(aws.Scope) State {
	return State{Keys: map[string]Key{}, Aliases: map[string]Alias{}}
}

// IsZero reports a scope without keys or aliases, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Keys) == 0 && len(s.Aliases) == 0 }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// Key is one symmetric encryption key with AWS_KMS origin, the only kind
// understudy creates.
type Key struct {
	ID          string        `json:"id"`
	Created     clock.Instant `json:"created"`
	Description string        `json:"description"`
	// Manager is CUSTOMER for keys CreateKey made and AWS for the AWS
	// managed keys behind the alias/aws/ aliases.
	Manager   kmsapi.KeyManagerType `json:"manager"`
	Lifecycle lifecycle             `json:"lifecycle"`
	// Policy is the key policy as the caller sent it, or the default one.
	Policy string `json:"policy"`
	// Rotation is nil while automatic rotation is off.
	Rotation *Rotation         `json:"rotation,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	// Material is the AES-256 key, always from crypto/rand.
	Material   []byte `json:"material"`
	MaterialID string `json:"materialId"`
}

// Rotation is automatic rotation turned on: the period and when it was
// turned on, which dates the next rotation.
type Rotation struct {
	PeriodDays int32         `json:"periodDays"`
	Since      clock.Instant `json:"since"`
}

// Alias names one key.
type Alias struct {
	Name    string        `json:"name"`
	KeyID   string        `json:"keyId"`
	Created clock.Instant `json:"created"`
	Updated clock.Instant `json:"updated"`
}

// lifecycle is a key's state: enabled, disabled or pending deletion. The
// deletion date exists only on the pending state.
//
//sumtype:decl
type lifecycle interface {
	isLifecycle()
	// keyState is the KeyState AWS reports.
	keyState() kmsapi.KeyState
}

type enabled struct{}

type disabled struct{}

type pendingDeletion struct {
	// DeletionDate is when the key and its aliases are deleted.
	DeletionDate clock.Instant `json:"deletionDate"`
	// WindowDays is the waiting period ScheduleKeyDeletion was given.
	WindowDays int32 `json:"windowDays"`
}

func (enabled) isLifecycle()         {}
func (disabled) isLifecycle()        {}
func (pendingDeletion) isLifecycle() {}

func (enabled) keyState() kmsapi.KeyState         { return kmsapi.KeyStateEnabled }
func (disabled) keyState() kmsapi.KeyState        { return kmsapi.KeyStateDisabled }
func (pendingDeletion) keyState() kmsapi.KeyState { return kmsapi.KeyStatePendingDeletion }

// stateCodec is the snapshot codec of State, with the lifecycle sum in the
// tagged-union form. A table jsonx refuses is a bug in this file; the codec
// then fails every Export and Import with that error.
func stateCodec() store.Codec {
	u, err := jsonx.NewUnion("kms.lifecycle",
		jsonx.Variant[lifecycle, enabled]("enabled"),
		jsonx.Variant[lifecycle, disabled]("disabled"),
		jsonx.Variant[lifecycle, pendingDeletion]("pending-deletion"),
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
