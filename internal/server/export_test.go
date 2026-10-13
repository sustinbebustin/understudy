package server

import "github.com/sustinbebustin/understudy/internal/journal"

// Journal is the emulator's journal, for the external tests that assert
// what a run recorded.
func (s *Server) Journal() *journal.Journal { return s.wired.journal }
