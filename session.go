package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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

// AutoLogin performs a fresh headless login for the given client using the
// provided credentials, auto-resolving guard verification via MV_TOTP_SECRET.
// Used at boot so a clean deploy (no session file on disk) self-authenticates.
func (ss *SessionStore) AutoLogin(clientID, user, pass string) error {
	scraper := NewForumScraper(user, pass, clientID)
	if secret := os.Getenv("MV_TOTP_SECRET"); secret != "" {
		scraper.SetTOTPSecret(secret)
	}
	// Relogin clears any stale cookies, logs in fresh and auto-submits the
	// TOTP guard code when required, then persists the session to disk.
	if err := scraper.Relogin(); err != nil {
		return err
	}
	ss.Set(clientID, &Session{Scraper: scraper, Status: "authenticated"})
	return nil
}

// RestoreAllFromDisk restores every persisted session found on disk (including
// per-device app-login tokens, not just the configured API tokens). Returns the
// number of sessions restored. Call once at boot so logins survive restarts.
func (ss *SessionStore) RestoreAllFromDisk() int {
	base, err := os.UserConfigDir()
	if err != nil {
		return 0
	}
	dir := filepath.Join(base, "mediavida-mcp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		token := strings.TrimSuffix(strings.TrimPrefix(name, "session-"), ".json")
		if token == "" || ss.Get(token) != nil {
			continue
		}
		if ss.RestoreFromDisk(token) {
			n++
		}
	}
	return n
}

// ForEach calls fn for each stored session while holding a read lock.
func (ss *SessionStore) ForEach(fn func(clientID string, s *Session)) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	for id, s := range ss.sessions {
		fn(id, s)
	}
}
