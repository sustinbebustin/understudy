package lambda

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// A mapping's state machine: the control plane starts a transition, and
// the mapping settles in Enabled or Disabled, or is gone after Deleting.
// The SQS poller of an Enabled mapping is not part of this build; a
// mapping is stored, reported and never polled.

// transition starts state under TransitionsClock and arms its settle
// timer; with final transitions the mapping stays settled.
func (s *Service) transition(tx *kernel.Tx, scope aws.Scope, m *Mapping, state MappingState, now clock.Instant) {
	if !s.clockTransitions() {
		return
	}
	m.Transition = &Transition{State: state, Settles: now.Add(settleDelay)}
	tx.Arm(s.settle, mappingARN(scope, m.UUID), m.Transition.Settles)
}

// settleMapping ends a mapping's transition that was due by due: it
// settles in Enabled or Disabled, or is gone once Deleting ends.
func (st *State) settleMapping(uuid string, due clock.Instant) bool {
	m, ok := st.Mappings[uuid]
	if !ok || m.Transition == nil || due.Before(m.Transition.Settles) {
		return false
	}
	if m.Transition.State == MappingDeleting {
		delete(st.Mappings, uuid)
		return true
	}
	m.Transition = nil
	st.Mappings[uuid] = m
	return true
}
