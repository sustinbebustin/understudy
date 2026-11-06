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
	all := apis()
	out := make([]ref.ServiceID, len(all))
	for i, a := range all {
		out[i] = a.desc.ID
	}
	return out
}
