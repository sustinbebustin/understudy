package identity

import "fmt"

// MintProblem says why the store refused to mint.
type MintProblem uint8

// MintProblem values.
const (
	// NoAccount: a service principal or an anonymous caller has no account
	// to encode in a key.
	NoAccount MintProblem = iota + 1
	// RoleNeedsTemporary: AWS issues role sessions only temporary
	// credentials.
	RoleNeedsTemporary
	// KeyCollision: the drawn key is already minted, which a seeded id source
	// whose stream positions were not restored with the snapshot can cause.
	KeyCollision
	// InvalidSessionSource: an assumed role whose Source is nil, or whose
	// execution environment Function is not an ARN that formats and parses
	// back to itself. A snapshot could not restore such a session.
	InvalidSessionSource
	// InvalidExpiry: an expiry that has no RFC 3339 form in UTC, the zone the
	// store holds it in: one whose UTC year is outside 0 to 9999. A snapshot
	// could not save it.
	InvalidExpiry
	// NoExpiry: a zero expiry, which is no STS Expiration.
	NoExpiry
	// MissingName: a User with no Name, or an AssumedRole with no role name
	// or session name, which its ARN shows.
	MissingName
	// MalformedPath: a User or role Path that is neither empty nor an IAM
	// path ("/" or printable ASCII between slashes, at most 512 characters).
	// A user's ARN shows its path.
	MalformedPath
	// MalformedName: a user or role name that is not 1 to 64 of letters,
	// digits and _+=,.@-, or a session name that is not 2 to 64 of them, the
	// IAM and STS rules. A "/" in a name would change the path its ARN reads
	// back as.
	MalformedName
)

// MintError reports credentials the store refused to mint. No key was
// minted and the store is unchanged.
type MintError struct {
	// Principal is the rendered principal.
	Principal string
	Problem   MintProblem
}

func (e *MintError) Error() string {
	return fmt.Sprintf("identity: cannot mint credentials for %s: %s; no key was minted", e.Principal, e.Problem.reason())
}

// reason explains the problem and how to avoid it, for MintError and for
// Import, which refuses the same records.
func (p MintProblem) reason() string {
	switch p {
	case NoAccount:
		return "it belongs to no account, and only Root, User and AssumedRole principals hold keys"
	case RoleNeedsTemporary:
		return "an assumed role takes only temporary credentials, as STS issues them; use MintTemporary"
	case KeyCollision:
		return "the id source drew a key the store already holds; restore the id stream positions saved with the snapshot"
	case InvalidSessionSource:
		return "the assumed role has no session source, or its execution environment function is not a well-formed ARN"
	case InvalidExpiry:
		return "the expiry has no RFC 3339 form in UTC; use a time whose UTC year is between 0 and 9999"
	case NoExpiry:
		return "the expiry is the zero time; pass the instant the session ends"
	case MissingName:
		return "a user needs a name, and an assumed role a role name and a session name, or its ARN is malformed"
	case MalformedPath:
		return `a user's or role's path must be empty, "/", or printable ASCII other than space between a leading and a trailing "/", at most 512 characters, such as "/service-role/"`
	case MalformedName:
		return "a user or role name must be 1 to 64, and a session name 2 to 64, of letters, digits and _+=,.@-, as IAM and STS require"
	}
	return fmt.Sprintf("unknown problem %d", uint8(p))
}
