package main

import (
	"errors"
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
// The in-memory map is the hot path; durable persistence is delegated entirely
// to the Raft-replicated Colmena store (cs) — there is no JSON-on-disk fallback.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session // clientID → session
	cs       *ColmenaStore       // durable, Raft-replicated persistence
}

func NewSessionStore(cs *ColmenaStore) *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
		cs:       cs,
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

// RestoreSession tries to load a durable session from Colmena for the given
// client ID. Validates the session is still active; if expired, attempts
// re-login. Returns true if a session was successfully restored.
func (ss *SessionStore) RestoreSession(clientID string) bool {
	if ss.cs == nil {
		return false
	}
	rec, ok, err := ss.cs.GetSession(clientID)
	if err != nil {
		log.Printf("Failed to read session for client %s: %v", clientID, err)
		return false
	}
	if !ok {
		return false
	}
	return ss.restoreFromRecord(rec)
}

// restoreFromRecord rehydrates a hot *ForumScraper from a durable SessionRecord,
// validates/re-logs in, and installs it into the in-memory map. Returns true on
// success.
func (ss *SessionStore) restoreFromRecord(rec SessionRecord) bool {
	scraper := NewForumScraper("", "", rec.ClientID, ss.cs)
	if secret := os.Getenv("MV_TOTP_SECRET"); secret != "" {
		scraper.SetTOTPSecret(secret)
	}
	if !scraper.loadFromRecord(rec) {
		return false
	}

	// Validate the session is still valid server-side
	if !scraper.ValidateSession() {
		log.Printf("Session for client %s is expired, attempting re-login", rec.ClientID)
		if scraper.Username() == "" || scraper.pass == "" {
			log.Printf("No credentials available for re-login (client %s)", rec.ClientID)
			return false
		}
		if err := scraper.Relogin(); err != nil {
			log.Printf("Re-login failed for client %s: %v", rec.ClientID, err)
			return false
		}
	}

	session := &Session{
		Scraper: scraper,
		Status:  "authenticated",
	}
	ss.mu.Lock()
	ss.sessions[rec.ClientID] = session
	ss.mu.Unlock()
	log.Printf("Session restored from Colmena for client %s (validated)", rec.ClientID)
	return true
}

// CreateFromCredentials logs in with user/pass and stores the session for the given client.
// Returns ErrGuardRequired if guard verification is needed.
func (ss *SessionStore) CreateFromCredentials(clientID, user, pass string) error {
	scraper := NewForumScraper(user, pass, clientID, ss.cs)
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
		var guardErr *ErrGuardRequired
		if errors.As(err, &guardErr) {
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
	scraper := NewForumScraper(user, pass, clientID, ss.cs)
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

// RestoreAll rehydrates every durable session from the Colmena store (including
// per-device app-login tokens, not just the configured API tokens). Returns the
// number of sessions restored. Call once at boot so logins survive restarts.
func (ss *SessionStore) RestoreAll() int {
	if ss.cs == nil {
		return 0
	}
	records, err := ss.cs.AllSessions()
	if err != nil {
		log.Printf("Failed to list sessions from Colmena: %v", err)
		return 0
	}
	n := 0
	for _, rec := range records {
		if rec.ClientID == "" || ss.Get(rec.ClientID) != nil {
			continue
		}
		if ss.restoreFromRecord(rec) {
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
