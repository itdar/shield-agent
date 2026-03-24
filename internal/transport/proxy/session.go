package proxy

import (
	"sync"
)

// session represents a client-upstream session in the SSE proxy.
type session struct {
	mu             sync.RWMutex
	upstreamMsgURL string     // absolute URL of the upstream /messages endpoint
	events         chan []byte // SSE event bytes to relay to the client
}

func (s *session) setUpstreamMsgURL(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstreamMsgURL = u
}

func (s *session) getUpstreamMsgURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.upstreamMsgURL
}

// sessionStore manages a thread-safe localSessionID → *session mapping.
type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*session)}
}

func (s *sessionStore) add(id string, sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = sess
}

func (s *sessionStore) get(id string) (*session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

func (s *sessionStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}
