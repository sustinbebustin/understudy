package iam

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// holdings are the policies a role or user holds: inline policies and the
// ARNs of attached managed policies.
type holdings struct {
	inline   []InlinePolicy
	attached []string
}

// holder reaches the policies of one role or user of a State, so inline
// policies and attachments are written once for both kinds of entity.
type holder struct {
	// get returns the entity's policies and ARN, or the error a missing
	// entity answers.
	get func(st *State) (holdings, aws.ARN, *awserr.Error)
	// set stores the entity again with new policies; get succeeded first.
	set func(st *State, h holdings)
	// noSuchInline answers an inline policy name the entity does not have.
	noSuchInline func(name string) *awserr.Error
	// kind names the entity in quota messages: "role" or "user".
	kind string
	name string
	// inlineLimit is the quota on the entity's inline policies together.
	inlineLimit int
	// attachQuota and attachLimit are the quota on attached policies.
	attachQuota string
	attachLimit int
}

func roleHolder(account aws.AccountID, name string) holder {
	return holder{
		get: func(st *State) (holdings, aws.ARN, *awserr.Error) {
			r, err := st.role(name)
			if err != nil {
				return holdings{}, aws.ARN{}, err
			}
			return holdings{inline: r.Inline, attached: r.Attached}, roleARN(account, r.Path, r.Name), nil
		},
		set: func(st *State, h holdings) {
			r := st.Roles[key(name)]
			r.Inline, r.Attached = h.inline, h.attached
			st.Roles[key(name)] = r
		},
		noSuchInline: errNoSuchRolePolicy,
		kind:         "role",
		name:         name,
		inlineLimit:  maxRoleInline,
		attachQuota:  "PoliciesPerRole",
		attachLimit:  maxRoleAttached,
	}
}

func userHolder(account aws.AccountID, name string) holder {
	return holder{
		get: func(st *State) (holdings, aws.ARN, *awserr.Error) {
			u, err := st.user(name)
			if err != nil {
				return holdings{}, aws.ARN{}, err
			}
			return holdings{inline: u.Inline, attached: u.Attached}, userARN(account, u.Path, u.Name), nil
		},
		set: func(st *State, h holdings) {
			u := st.Users[key(name)]
			u.Inline, u.Attached = h.inline, h.attached
			st.Users[key(name)] = u
		},
		noSuchInline: errNoSuchUserPolicy,
		kind:         "user",
		name:         name,
		inlineLimit:  maxUserInline,
		attachQuota:  "PoliciesPerUser",
		attachLimit:  maxUserAttached,
	}
}
