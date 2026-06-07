package main

import (
	"log"
	"sync"
	"time"
)

const keepAliveInterval = 4 * time.Hour

// SessionKeepAlive periodically refreshes all authenticated sessions by visiting
// mediavida.com, preventing cookies from expiring due to inactivity.
// This runs independently of the BubblesPoller so that session refresh is never
// skipped even if bubbles polling fails.
type SessionKeepAlive struct {
	sessions *SessionStore

	mu     sync.Mutex
	stopCh chan struct{}
}

func NewSessionKeepAlive(sessions *SessionStore) *SessionKeepAlive {
	return &SessionKeepAlive{sessions: sessions}
}

func (ka *SessionKeepAlive) Start() {
	ka.mu.Lock()
	defer ka.mu.Unlock()

	if ka.stopCh != nil {
		return
	}
	ka.stopCh = make(chan struct{})
	go ka.loop() // goroutine-ok: long-lived background poller, stopped via stopCh for the process lifetime
}

func (ka *SessionKeepAlive) Stop() {
	ka.mu.Lock()
	defer ka.mu.Unlock()

	if ka.stopCh != nil {
		close(ka.stopCh)
		ka.stopCh = nil
	}
}

func (ka *SessionKeepAlive) loop() {
	log.Printf("[keepalive] started (every %s)", keepAliveInterval)

	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ka.stopCh:
			log.Printf("[keepalive] stopped")
			return
		case <-ticker.C:
			ka.refreshAll()
		}
	}
}

func (ka *SessionKeepAlive) refreshAll() {
	ka.sessions.ForEach(func(clientID string, s *Session) {
		if s.Status != "authenticated" || s.Scraper == nil {
			return
		}
		username := s.Scraper.Username()

		// Visit the homepage to get fresh Set-Cookie headers
		if err := s.Scraper.RefreshSession(); err != nil {
			log.Printf("[keepalive] refresh failed for %s: %v", username, err)

			// Fallback: try validating the session to at least touch the server
			if s.Scraper.ValidateSession() {
				log.Printf("[keepalive] session still valid for %s (validate succeeded)", username)
				s.Scraper.SaveSession()
			} else {
				log.Printf("[keepalive] session appears expired for %s", username)
			}
			return
		}

		s.Scraper.SaveSession()
		log.Printf("[keepalive] session refreshed and saved for %s", username)
	})
}
