package ids

import "fmt"

// UnknownKindError reports a generator kind name or value this binary does
// not know, such as one read from a snapshot manifest written by another
// version, or a Kind value outside the declared constants.
type UnknownKindError struct {
	Name string
}

func (e *UnknownKindError) Error() string {
	return fmt.Sprintf("ids: unknown generator kind %q; known kinds are %s", e.Name, knownKinds())
}

// PositionError reports a stream position above MaxPosition, such as one read
// from a corrupt snapshot manifest. No stream moved.
type PositionError struct {
	Kind Kind
	Pos  uint64
}

func (e *PositionError) Error() string {
	return fmt.Sprintf("ids: stream position %d for kind %s is above the maximum %d; no stream moved (the snapshot manifest is corrupt or was not written by understudy)",
		e.Pos, e.Kind, MaxPosition)
}

// PositionLimitError reports a stream drawn past MaxPosition when its
// position is read for a snapshot. Seek would refuse the position, so it is
// not reported. The source keeps drawing normally.
type PositionLimitError struct {
	Kind Kind
	Pos  uint64
}

func (e *PositionLimitError) Error() string {
	return fmt.Sprintf("ids: the %s stream has drawn %d words, above the %d a snapshot can restore, so the stream positions cannot be saved; id generation continues normally (start a new seeded session to save snapshots again)",
		e.Kind, e.Pos, MaxPosition)
}

// NotSeededError reports Positions or Seek on a source that draws from
// crypto/rand, which has no stream positions to record or restore. The source
// is unchanged.
type NotSeededError struct{}

func (e *NotSeededError) Error() string {
	return "ids: a random id source has no stream positions to record or seek; only a source started with a seed has them"
}

// InvalidAccountError reports an account that is not exactly 12 ASCII digits
// when minting an access key. No key was drawn.
type InvalidAccountError struct {
	Account string
}

func (e *InvalidAccountError) Error() string {
	return fmt.Sprintf("ids: cannot mint an access key for account %q: an account ID is exactly 12 digits", e.Account)
}
