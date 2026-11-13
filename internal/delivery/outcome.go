package delivery

// Outcome is how a delivery ended, as the outbox reports it back to the
// producer that asks (Request.AlarmAction). A delivery that is retried has
// no outcome until its last attempt.
//
//sumtype:decl
type Outcome interface{ isOutcome() }

// Delivered is a delivery its target accepted.
type Delivered struct{}

// Denied is a delivery its target refused on its policy.
type Denied struct{}

// TargetFailed is a delivery whose last target call answered an error
// other than a denial, Message being the error's text.
type TargetFailed struct{ Message string }

// Undelivered is a delivery that ended with no answer from its target,
// such as one a fault rule dropped; Reason says why, as the journal does.
type Undelivered struct{ Reason string }

func (Delivered) isOutcome()    {}
func (Denied) isOutcome()       {}
func (TargetFailed) isOutcome() {}
func (Undelivered) isOutcome()  {}
