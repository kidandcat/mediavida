package main

import (
	"errors"
	"fmt"
	"log"
	"os"
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
	s := ss.sessions[clientID]
	ss.mu.RUnlock()
	if s != nil {
		return s
	}
	// Multi-node cluster: the session may have been created on another node and
	// only live in Raft (this node's in-memory map is per-process). Lazily
	// rehydrate from Colmena so any node can serve any device — without this, a
	// login on node A followed by a request routed to node B looks unauthenticated.
	if ss.RestoreSession(clientID) {
		ss.mu.RLock()
		s = ss.sessions[clientID]
		ss.mu.RUnlock()
	}
	return s
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
		// No usable cookies left (all expired by date, or the record is
		// corrupt). There is nothing to trust, so a fresh login is the only
		// way to recover — do it lazily here when we have credentials.
		if scraper.Username() == "" || scraper.pass == "" {
			log.Printf("No usable cookies and no credentials for re-login (client %s)", rec.ClientID)
			return false
		}
		if err := scraper.Relogin(); err != nil {
			log.Printf("Re-login failed for client %s: %v", rec.ClientID, err)
			return false
		}
	}

	// IMPORTANT: do NOT probe MV here (no ValidateSession). The cookies in
	// Colmena are the freshest copy in the cluster, so we trust them and mark
	// the session authenticated immediately. Probing MV on every cross-node
	// rehydration was the cause of spurious logouts: any transient failure of
	// that HTTP call (anti-bot challenge, rate-limit, 5xx, network) made us
	// discard a perfectly valid session and kick the user to the login screen.
	// If the cookies really are dead, the first real data operation detects it
	// via ErrSessionExpired and recovers through withRelogin (see api.go) —
	// reactive recovery, no false logout.
	session := &Session{
		Scraper: scraper,
		Status:  "authenticated",
	}
	ss.mu.Lock()
	ss.sessions[rec.ClientID] = session
	ss.mu.Unlock()
	log.Printf("Session restored from Colmena for client %s", rec.ClientID)
	return true
}

// CreateFromCredentials logs in with user/pass and stores the session for the given client.
// Returns ErrGuardRequired if guard verification is needed.
func (ss *SessionStore) CreateFromCredentials(clientID, user, pass string) error {
	scraper := NewForumScraper(user, pass, clientID, ss.cs)
	if secret := os.Getenv("MV_TOTP_SECRET"); secret != "" {
		scraper.SetTOTPSecret(secret)
	}

	// Try loading a saved session first. Only reuse it if it belongs to the
	// account the caller is logging in as: the stored record is keyed by
	// client token, so it may hold a different user's session (e.g. the boot
	// auto-login from MV_USERNAME). Restoring it blindly would silently hand
	// the caller someone else's identity.
	if scraper.LoadSession() {
		if strings.EqualFold(scraper.Username(), user) {
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
		log.Printf("Stored session for client %s belongs to %q, not %q — discarding it and logging in fresh", clientID, scraper.Username(), user)
		scraper = NewForumScraper(user, pass, clientID, ss.cs)
		if secret := os.Getenv("MV_TOTP_SECRET"); secret != "" {
			scraper.SetTOTPSecret(secret)
		}
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

// CreateWatchAlias mints a new bearer token for a paired watch. It does NOT
// duplicate the MV session: the token aliases the owner's session and resolves
// to it at request time (see APITokenMiddleware), so there is only ever one
// Mediavida session per account — duplicating it spawned a second concurrent
// login and triggered guard/login storms. Pairing still requires the owner to be
// logged in. Returns the new token; the caller records the token→owner mapping.
func (ss *SessionStore) CreateWatchAlias(ownerClientID string) (string, error) {
	if ss.Get(ownerClientID) == nil {
		return "", errors.New("no active session to pair; log in first")
	}
	return "wf_" + generateFlowID(), nil
}

// ForEach calls fn for each stored session while holding a read lock.
func (ss *SessionStore) ForEach(fn func(clientID string, s *Session)) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	for id, s := range ss.sessions {
		fn(id, s)
	}
}
