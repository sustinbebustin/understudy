package fault

import (
	"errors"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Members is an error action's members: a JSON object of the error shape's
// members in the awsJson1_0 form, names as modeled and never jsonName,
// whatever the service's protocol. Add decodes them strictly through the
// shape's generated deserializer, which it reaches through the Catalogue.
type Members document.Map

// errMembersNotImplemented is what a rule with members fails with until
// error actions carry members.
var errMembersNotImplemented = errors.New("fault: action.members is not implemented yet; leave it out")

// MarshalJSON writes the members as a compact JSON object in the order
// given.
//
// It is not implemented yet: it returns an error.
func (Members) MarshalJSON() ([]byte, error) {
	return nil, errMembersNotImplemented
}

// UnmarshalJSON reads a JSON object of members, refusing any other JSON
// value and a duplicate member name.
//
// It is not implemented yet: it returns an error.
func (*Members) UnmarshalJSON([]byte) error {
	return errMembersNotImplemented
}
