package codegen

import (
	"errors"
	"fmt"
)

// Stage names the generator stage that failed (ARCHITECTURE.md 5.4).
type Stage uint8

// Stage values, in pipeline order.
const (
	StageLoad Stage = iota + 1
	StageOverlay
	StageClosure
	StageClassify
	StageEffective
	StageRegex
	StageRoles
	StageIR
	StageEmit
	StageWrite
)

// String returns the stage name used in error messages.
func (s Stage) String() string {
	switch s {
	case StageLoad:
		return "load"
	case StageOverlay:
		return "overlay"
	case StageClosure:
		return "closure"
	case StageClassify:
		return "classify"
	case StageEffective:
		return "effective"
	case StageRegex:
		return "regex"
	case StageRoles:
		return "roles"
	case StageIR:
		return "ir"
	case StageEmit:
		return "emit"
	case StageWrite:
		return "write"
	default:
		return fmt.Sprintf("Stage(%d)", uint8(s))
	}
}

// Error is a generation failure: the stage, what it was looking at (a shape
// id, member, trait or file) and what is wrong with it. Generation stops at
// the first Error; nothing is written.
type Error struct {
	Stage   Stage
	Subject string
	Detail  string
	// Err is the cause Detail wraps, such as the fs error of a failed read,
	// so callers can test it with errors.Is; nil when there is none.
	Err error
}

func (e *Error) Error() string {
	return fmt.Sprintf("understudy-gen %s: %s: %s", e.Stage, e.Subject, e.Detail)
}

// Unwrap returns the cause, if any.
func (e *Error) Unwrap() error { return e.Err }

// failf builds an *Error whose Detail is formatted as fmt.Errorf does; a %w
// verb renders like %v and also keeps that error as the cause.
func failf(stage Stage, subject, format string, args ...any) *Error {
	detail := fmt.Errorf(format, args...)
	return &Error{Stage: stage, Subject: subject, Detail: detail.Error(), Err: errors.Unwrap(detail)}
}
