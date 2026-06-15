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

		// Touch the homepage so MV refreshes any sliding-expiry cookies.
		if err := s.Scraper.RefreshSession(); err != nil {
			log.Printf("[keepalive] homepage touch failed for %s: %v", username, err)
		}

		// The homepage is public, so a 200 there does NOT prove the session is
		// still authenticated. Verify against an authenticated endpoint; if the
		// session has expired, re-login silently now (stored credentials +
		// remember cookie) — well before the user opens the app — so MV's short
		// session-cookie lifetime never surfaces as a logout in the app.
		if s.Scraper.ValidateSession() {
			s.Scraper.SaveSession()
			return
		}

		log.Printf("[keepalive] session expired for %s, re-logging in", username)
		if err := s.Scraper.Relogin(); err != nil {
			log.Printf("[keepalive] silent re-login failed for %s (user must re-auth): %v", username, err)
			return
		}
		log.Printf("[keepalive] silently re-logged in %s", username)
	})
}
