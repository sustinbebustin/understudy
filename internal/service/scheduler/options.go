package scheduler

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/schedexpr"
)

// Options are the Scheduler switches that have another side; the zero value
// is every default. Tests build a Service with one field flipped.
type Options struct {
	// RateUnits is scheduler.rate.unit-agreement.
	RateUnits schedexpr.RateUnits
	// AtPastDue is scheduler.at.past-due.
	AtPastDue AtPastDue
	// FlexibleOffset is scheduler.flexible-window.offset.
	FlexibleOffset FlexibleOffset
	// PermanentErrors is scheduler.retry.permanent-errors.
	PermanentErrors delivery.PermanentErrors
	// KeyPolicies tells which KMS keys exist: a KmsKeyArn naming none is
	// accepted at create and update with no KMS action asked, as AWS did
	// (SCH-04 04-kms-missing); wire.go passes the KMS service. Nil holds no
	// key.
	KeyPolicies authz.PolicyProvider
}

// AtPastDue is what an at() schedule whose time has passed does when it is
// created, updated or enabled (scheduler.md 4.6, 5.1, SCH-09).
type AtPastDue uint8

// AtPastDue values; the zero value is PastDueFireAtOnce.
const (
	// PastDueFireAtOnce invokes the target once, at the write instant.
	PastDueFireAtOnce AtPastDue = iota
	// PastDueNever never invokes it; the schedule is completed at once.
	PastDueNever
)

// String returns the switch value: "fire-at-once" or "never".
func (a AtPastDue) String() string {
	switch a {
	case PastDueFireAtOnce:
		return "fire-at-once"
	case PastDueNever:
		return "never"
	}
	return "AtPastDue(" + strconv.Itoa(int(a)) + ")"
}

// FlexibleOffset is how far after its scheduled instant a FLEXIBLE
// invocation is due (scheduler.md 5.6, 14.3.7).
type FlexibleOffset uint8

// FlexibleOffset values; the zero value is OffsetSeededUniform.
const (
	// OffsetSeededUniform draws a whole-second offset in [0, window) from
	// the ids.KindFlexOffset stream when the fire is armed.
	OffsetSeededUniform FlexibleOffset = iota
	// OffsetWindowStart uses offset 0: the invocation is due at the
	// scheduled instant.
	OffsetWindowStart
)

// String returns the switch value: "seeded-uniform" or "window-start".
func (f FlexibleOffset) String() string {
	switch f {
	case OffsetSeededUniform:
		return "seeded-uniform"
	case OffsetWindowStart:
		return "window-start"
	}
	return "FlexibleOffset(" + strconv.Itoa(int(f)) + ")"
}
