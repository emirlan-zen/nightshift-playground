package machine

import (
	"sync"
	"time"
)

type Reader func() Vitals

type Service struct {
	read Reader
	now  func() time.Time
	ttl  time.Duration

	mu       sync.Mutex
	cached   Vitals
	cachedAt time.Time
}

func NewService(read Reader) *Service {
	return &Service{read: read, now: time.Now, ttl: 4 * time.Second}
}

func (s *Service) Current() Vitals {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.cachedAt.IsZero() || now.Sub(s.cachedAt) > s.ttl {
		s.cached = s.read()
		s.cachedAt = now
	}
	return s.cached
}
