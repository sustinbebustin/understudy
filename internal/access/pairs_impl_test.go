package access_test

import (
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Entry.Pairs beyond the blind tests: a pair Record cannot render is
// refused with nothing recorded, and an empty, non-nil Pairs records no use
// even when Actions lists some (iam-enforcement-surfaces.md 12.6).
func TestRecordRefusesAPairItCannotRender(t *testing.T) {
	list := act("sqs", "ListQueues")
	for name, c := range map[string]struct {
		pair access.Pair
		want access.EntryProblem
	}{
		"no resource":    {access.Pair{Action: list}, access.EntryNoPairScope},
		"invalid action": {access.Pair{Action: registry.IAMAction{Prefix: "sqs"}, Resource: access.AnyResource{}}, access.EntryInvalidAction},
	} {
		t.Run(name, func(t *testing.T) {
			var r access.Recorder
			err := r.Record(access.Entry{
				Principal: app, Mode: access.Direct{}, Service: "sqs", Actions: []registry.IAMAction{list},
				Pairs: []access.Pair{{Action: list, Resource: access.AnyResource{}}, c.pair}, Outcome: access.Succeeded{}, At: t0,
			})
			var refused *access.EntryError
			if !errors.As(err, &refused) || refused.Problem != c.want {
				t.Fatalf("Record = %v, want problem %d", err, c.want)
			}
			if uses := r.Usage(access.Filter{}); len(uses) != 0 {
				t.Errorf("a refused entry recorded %+v", uses)
			}
		})
	}
}

func TestEmptyPairsRecordNoUse(t *testing.T) {
	var r access.Recorder
	record(t, &r, access.Entry{
		Principal: app, Mode: access.Direct{}, Service: "iam", Actions: []registry.IAMAction{act("iam", "PassRole")},
		Pairs: []access.Pair{}, Outcome: access.Succeeded{}, At: t0,
	})
	if uses := r.Usage(access.Filter{}); len(uses) != 0 {
		t.Errorf("Usage = %+v, want none: the call needed no pair", uses)
	}
}
