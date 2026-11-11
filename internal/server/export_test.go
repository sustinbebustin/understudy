package server

import (
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Journal is the emulator's journal, for the external tests that assert
// what a run recorded.
func (s *Server) Journal() *journal.Journal { return s.wired.journal }

// ServiceIDs are the wired services' registry ids in wiring order, for the
// external tests that count what a run serves instead of hard-coding it.
func ServiceIDs() []ref.ServiceID {
	all := descriptors()
	out := make([]ref.ServiceID, len(all))
	for i, d := range all {
		out[i] = d.ID
	}
	return out
}
