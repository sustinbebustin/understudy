package docker

import "strconv"

// The label keys on every object understudy creates (containers.md 9.1).
// Image labels are inherited into containers and events, so selectors use
// only these keys.
const (
	// LabelManaged ("true") is the sweep selector.
	LabelManaged = "understudy.dev/managed"
	// LabelInstance is random per serve process; it tells concurrent
	// emulators on one daemon apart.
	LabelInstance = "understudy.dev/instance"
	// LabelOwner is the identity that outlives a serve process (its data
	// directory, or its run without one); a later process sweeps an earlier
	// instance's leftovers by it.
	LabelOwner = "understudy.dev/owner"
	// LabelFunction, LabelAccount, LabelRegion and LabelGeneration identify
	// the function environment a container serves.
	LabelFunction   = "understudy.dev/function"
	LabelAccount    = "understudy.dev/account"
	LabelRegion     = "understudy.dev/region"
	LabelGeneration = "understudy.dev/generation"
	// LabelTest ("1") marks containers started by test harnesses; serve
	// never sets it.
	LabelTest = "understudy.dev/test"
)

// Ownership is the serve process an object belongs to. Both fields must be
// set: Sweep and Labels.Map refuse an incomplete one through Validate.
type Ownership struct {
	Instance string
	Owner    string
}

// Validate returns an *OwnershipError when the instance or the owner is
// empty: an object labeled so is invisible to its owner's sweep, and a sweep
// by it would treat every instance of the owner as a leftover.
func (o Ownership) Validate() error {
	if o.Instance == "" || o.Owner == "" {
		return &OwnershipError{Ownership: o}
	}
	return nil
}

// FunctionLabels identify the function environment a container serves.
type FunctionLabels struct {
	Function   string
	Account    string
	Region     string
	Generation uint64
}

// Labels is the label set of one object.
type Labels struct {
	Ownership Ownership
	// Function is nil for objects that serve no single function, such as
	// networks and the DynamoDB Local container.
	Function *FunctionLabels
	// Test marks an object created by a test harness.
	Test bool
}

// Map renders the label set for a create call. It returns the
// *OwnershipError from Ownership.Validate for an incomplete ownership, since
// an object labeled so would escape its owner's sweep.
func (l Labels) Map() (map[string]string, error) {
	if err := l.Ownership.Validate(); err != nil {
		return nil, err
	}
	m := map[string]string{
		LabelManaged:  "true",
		LabelInstance: l.Ownership.Instance,
		LabelOwner:    l.Ownership.Owner,
	}
	if f := l.Function; f != nil {
		m[LabelFunction] = f.Function
		m[LabelAccount] = f.Account
		m[LabelRegion] = f.Region
		m[LabelGeneration] = strconv.FormatUint(f.Generation, 10)
	}
	if l.Test {
		m[LabelTest] = "1"
	}
	return m, nil
}

// ownerSelector selects every managed object of an owner.
func ownerSelector(owner string) map[string]string {
	return map[string]string{LabelManaged: "true", LabelOwner: owner}
}
