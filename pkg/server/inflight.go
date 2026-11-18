package server

import "time"

// slotsBusy reports whether all inference slots are taken. nvidia-smi
// utilization is not an admission check: it is a trailing window and stays
// high for about a second after this process's own forward.
func (s *Server) slotsBusy() bool {
	snap := s.admit().Snapshot()
	return snap.Inflight >= snap.Limit
}

// defaultSessionQueueLimit is how many returning turns may wait for a slot
// when the config does not say.
const defaultSessionQueueLimit = 32

func (s *Server) queueLimit() int {
	if s.sessionQueueLimit > 0 {
		return s.sessionQueueLimit
	}
	return defaultSessionQueueLimit
}

// sessionMemory is how long a conversation still counts as "served here".
const sessionMemory = 15 * time.Minute

func (s *Server) isActiveSession(key string) bool {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	seen, ok := s.sessions[key]
	return ok && time.Since(seen) <= sessionMemory
}

// setActiveSession remembers this conversation. Empty keys are ignored:
// a request without a session does not erase the others still in flight.
func (s *Server) setActiveSession(key string) {
	if key == "" {
		return
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessions == nil {
		s.sessions = map[string]time.Time{}
	}
	now := time.Now()
	s.sessions[key] = now
	cutoff := now.Add(-sessionMemory)
	for k, seen := range s.sessions {
		if seen.Before(cutoff) {
			delete(s.sessions, k)
		}
	}
}

// ClaimIdleSlot reserves an inference slot for the client's own work, such as
// a capability probe. It succeeds only when nothing is in flight and no
// benchmark has the engine. The slot counts as in flight: the platform sees
// it, and a benchmark that starts while it is held waits for it.
func (s *Server) ClaimIdleSlot() (release func(), ok bool) {
	slot, ok := s.admit().ClaimIdle()
	if !ok {
		return nil, false
	}
	return slot.Release, true
}

// notifyCapacityChange asks the capacity sender to push. Non-blocking;
// the sender coalesces bursts into one in-flight POST.
func (s *Server) notifyCapacityChange() {
	if s.onCapacity != nil {
		s.onCapacity()
	}
}
