package iam

import (
	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 1

// State is the IAM state of one account. Entities are keyed by their name
// in lower case, since IAM names are unique regardless of case while each
// record keeps the case it was created with. Records are values, replaced
// on change and never mutated in place, so a value read out of a cell never
// aliases the cell.
type State struct {
	Roles            map[string]Role            `json:"roles"`
	Users            map[string]User            `json:"users"`
	Policies         map[string]Policy          `json:"policies"`
	InstanceProfiles map[string]InstanceProfile `json:"instanceProfiles"`
}

func newState(aws.AccountID) State {
	return State{
		Roles:            map[string]Role{},
		Users:            map[string]User{},
		Policies:         map[string]Policy{},
		InstanceProfiles: map[string]InstanceProfile{},
	}
}

// IsZero reports an account without IAM entities, which snapshots skip.
func (s *State) IsZero() bool {
	return len(s.Roles) == 0 && len(s.Users) == 0 && len(s.Policies) == 0 && len(s.InstanceProfiles) == 0
}

// Tag is one tag of an IAM entity, in the order it was first added.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// InlinePolicy is a policy embedded in a role or user. Document is the text
// the caller sent, kept byte for byte.
type InlinePolicy struct {
	Name     string `json:"name"`
	Document string `json:"document"`
}

// Role is an IAM role.
type Role struct {
	Name string    `json:"name"`
	Path string    `json:"path"`
	ID   ids.IAMID `json:"id"`
	// TrustPolicy is the assume-role policy as the caller sent it.
	TrustPolicy        string `json:"trustPolicy"`
	Description        string `json:"description,omitempty"`
	MaxSessionDuration int32  `json:"maxSessionDuration"`
	// Boundary is the permissions boundary's policy ARN, empty for none.
	Boundary string         `json:"boundary,omitempty"`
	Tags     []Tag          `json:"tags,omitempty"`
	Created  clock.Instant  `json:"created"`
	Inline   []InlinePolicy `json:"inline,omitempty"`
	// Attached are the managed policy ARNs attached to the role.
	Attached []string `json:"attached,omitempty"`
}

// User is an IAM user.
type User struct {
	Name       string         `json:"name"`
	Path       string         `json:"path"`
	ID         ids.IAMID      `json:"id"`
	Boundary   string         `json:"boundary,omitempty"`
	Tags       []Tag          `json:"tags,omitempty"`
	Created    clock.Instant  `json:"created"`
	Inline     []InlinePolicy `json:"inline,omitempty"`
	Attached   []string       `json:"attached,omitempty"`
	AccessKeys []AccessKey    `json:"accessKeys,omitempty"`
}

// AccessKey is a long-term key of a user. The identity store holds its
// secret and resolves it to the user; IAM keeps what ListAccessKeys shows.
type AccessKey struct {
	ID      ids.AccessKeyID   `json:"accessKeyId"`
	Status  iamapi.StatusType `json:"status"`
	Created clock.Instant     `json:"created"`
}

// Policy is a customer managed policy of the account.
type Policy struct {
	Name        string        `json:"name"`
	Path        string        `json:"path"`
	ID          ids.IAMID     `json:"id"`
	Description string        `json:"description,omitempty"`
	Tags        []Tag         `json:"tags,omitempty"`
	Created     clock.Instant `json:"created"`
	// Updated is when the default version last changed.
	Updated clock.Instant `json:"updated"`
	// Versions are in creation order; at most maxPolicyVersions.
	Versions []PolicyVersion `json:"versions"`
	// Default is the id of the default version, one of Versions.
	Default string `json:"default"`
	// Next is the number of the next version id: IAM never reuses one.
	Next int `json:"next"`
}

// PolicyVersion is one version of a managed policy. Document is the text
// the caller sent, kept byte for byte.
type PolicyVersion struct {
	ID       string        `json:"id"`
	Document string        `json:"document"`
	Created  clock.Instant `json:"created"`
}

// InstanceProfile is an IAM instance profile.
type InstanceProfile struct {
	Name    string        `json:"name"`
	Path    string        `json:"path"`
	ID      ids.IAMID     `json:"id"`
	Tags    []Tag         `json:"tags,omitempty"`
	Created clock.Instant `json:"created"`
	// Role is the key of the profile's one role, empty for none: an
	// instance profile holds at most one role.
	Role string `json:"role,omitempty"`
}
