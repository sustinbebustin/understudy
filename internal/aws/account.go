package aws

import "fmt"

// AccountID is a 12-digit AWS account number. ParseAccountID is the only
// constructor; the zero value is the absent account.
type AccountID struct{ digits string }

// AccountIDError reports a string that is not a 12-digit account number.
type AccountIDError struct {
	Input string
}

func (e *AccountIDError) Error() string {
	return fmt.Sprintf("account id %q is not valid: an AWS account id is exactly 12 ASCII digits, such as 000000000000", e.Input)
}

// ParseAccountID accepts exactly 12 ASCII digits.
func ParseAccountID(s string) (AccountID, error) {
	if len(s) != 12 {
		return AccountID{}, &AccountIDError{Input: s}
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return AccountID{}, &AccountIDError{Input: s}
		}
	}
	return AccountID{digits: s}, nil
}

// DefaultAccount is 000000000000, the account of every access key understudy
// did not mint and that is not itself 12 digits (ADR 0012).
func DefaultAccount() AccountID {
	return AccountID{digits: "000000000000"}
}

// String returns the 12 digits, or the empty string for the zero value.
func (a AccountID) String() string { return a.digits }

// IsZero reports whether a is the absent account.
func (a AccountID) IsZero() bool { return a.digits == "" }

// MarshalText encodes the 12 digits so persisted state keeps the account.
func (a AccountID) MarshalText() ([]byte, error) { return []byte(a.digits), nil }

// UnmarshalText parses the 12 digits; the empty text decodes to the zero value.
func (a *AccountID) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*a = AccountID{}
		return nil
	}
	parsed, err := ParseAccountID(string(text))
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}
