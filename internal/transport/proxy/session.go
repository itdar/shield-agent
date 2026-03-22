package proxy

import (
	"sync"
)

// session은 SSE 프록시의 클라이언트-업스트림 간 세션을 나타낸다.
type session struct {
	mu             sync.RWMutex
	upstreamMsgURL string     // 업스트림 /messages 엔드포인트 절대 URL
	events         chan []byte // 클라이언트로 relay할 SSE 이벤트 바이트
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

// sessionStore는 localSessionID → *session 매핑을 스레드 안전하게 관리한다.
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
