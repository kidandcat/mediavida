package main

import (
	"fmt"
	"log"
	"os"
	"sync"
)

// Session maps a client to a ForumScraper with active MV cookies.
type Session struct {
	Scraper  *ForumScraper
	Status   string // "pending", "guard_required", "authenticated"
	GuardURL string
}

// SessionStore manages per-client sessions keyed by client ID (API token).
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session // clientID → session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
	}
}

// Get returns a session by client ID.
func (ss *SessionStore) Get(clientID string) *Session {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.sessions[clientID]
}

// Set stores a session for a client ID.
func (ss *SessionStore) Set(clientID string, s *Session) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.sessions[clientID] = s
}

// RestoreFromDisk tries to load a session from disk for the given client ID.
// Validates the session is still active; if expired, attempts re-login.
// Returns true if a session was successfully restored.
func (ss *SessionStore) RestoreFromDisk(clientID string) bool {
	scraper := NewForumScraper("", "", clientID)
	if secret := os.Getenv("MV_TOTP_SECRET"); secret != "" {
		scraper.SetTOTPSecret(secret)
	}
	if !scraper.LoadSession() {
		return false
	}

	// Validate the session is still valid server-side
	if !scraper.ValidateSession() {
		log.Printf("Session on disk for client %s is expired, attempting re-login", clientID)
		if scraper.Username() == "" || scraper.pass == "" {
			log.Printf("No credentials available for re-login (client %s)", clientID)
			return false
		}
		if err := scraper.Relogin(); err != nil {
			log.Printf("Re-login failed for client %s: %v", clientID, err)
			return false
		}
	}

	session := &Session{
		Scraper: scraper,
		Status:  "authenticated",
	}
	ss.mu.Lock()
	ss.sessions[clientID] = session
	ss.mu.Unlock()
	log.Printf("Session restored from disk for client %s (validated)", clientID)
	return true
}

// CreateFromCredentials logs in with user/pass and stores the session for the given client.
// Returns ErrGuardRequired if guard verification is needed.
func (ss *SessionStore) CreateFromCredentials(clientID, user, pass string) error {
	scraper := NewForumScraper(user, pass, clientID)
	if secret := os.Getenv("MV_TOTP_SECRET"); secret != "" {
		scraper.SetTOTPSecret(secret)
	}

	// Try loading a saved session first
	if scraper.LoadSession() {
		session := &Session{
			Scraper: scraper,
			Status:  "authenticated",
		}
		ss.mu.Lock()
		ss.sessions[clientID] = session
		ss.mu.Unlock()
		log.Printf("Session restored from disk for client %s", clientID)
		return nil
	}

	err := scraper.Login()
	if err != nil {
		if guardErr, ok := err.(*ErrGuardRequired); ok {
			session := &Session{
				Scraper:  scraper,
				Status:   "guard_required",
				GuardURL: guardErr.GuardURL,
			}
			ss.mu.Lock()
			ss.sessions[clientID] = session
			ss.mu.Unlock()
			return err
		}
		return fmt.Errorf("login failed: %w", err)
	}

	scraper.SaveSession()
	session := &Session{
		Scraper: scraper,
		Status:  "authenticated",
	}
	ss.mu.Lock()
	ss.sessions[clientID] = session
	ss.mu.Unlock()
	return nil
}

// ForEach calls fn for each stored session while holding a read lock.
func (ss *SessionStore) ForEach(fn func(clientID string, s *Session)) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	for id, s := range ss.sessions {
		fn(id, s)
	}
}
