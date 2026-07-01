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

// errSessionStoreUnavailable marks a TRANSIENT failure to read the durable
// session store (Colmena leader election, quorum loss, network blip), as opposed
// to the session genuinely not existing. Auth gates must map this to a retryable
// 503 — never a 401 — so a momentary store hiccup doesn't kick the user to the
// login screen even though their session is perfectly valid.
var errSessionStoreUnavailable = errors.New("session store temporarily unavailable") // global-ok: sentinel error value

// Get returns a session by client ID, or nil when none is active. It does NOT
// distinguish "no session" from a transient store failure — callers gating
// authentication should use [Lookup] instead so they can return 503 (retry)
// rather than 401 (logout) on a transient Colmena read error.
func (ss *SessionStore) Get(clientID string) *Session {
	s, _ := ss.Lookup(clientID) // safe-ignore: Get intentionally collapses "no session" and "store unavailable" to nil; callers needing the distinction use Lookup
	return s
}

// lookupSession is a nil-safe wrapper around [SessionStore.Lookup]: a nil store
// (some test/tooling wiring passes none) resolves to "no session" without a
// transient error, matching the historical `sessions == nil` short-circuit.
func lookupSession(ss *SessionStore, clientID string) (*Session, error) {
	if ss == nil {
		return nil, nil
	}
	return ss.Lookup(clientID)
}

// Lookup is like [Get] but surfaces whether a nil result is definitive (no
// session: nil, nil) or transient (store unavailable: nil, errSessionStoreUnavailable).
func (ss *SessionStore) Lookup(clientID string) (*Session, error) {
	ss.mu.RLock()
	s := ss.sessions[clientID]
	ss.mu.RUnlock()
	if s != nil {
		return s, nil
	}
	// Multi-node cluster: the session may have been created on another node and
	// only live in Raft (this node's in-memory map is per-process). Lazily
	// rehydrate from Colmena so any node can serve any device — without this, a
	// login on node A followed by a request routed to node B looks unauthenticated.
	ok, err := ss.restoreSessionE(clientID)
	if err != nil {
		return nil, err
	}
	if ok {
		ss.mu.RLock()
		s = ss.sessions[clientID]
		ss.mu.RUnlock()
	}
	return s, nil
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
	ok, _ := ss.restoreSessionE(clientID) // safe-ignore: bool-only callers don't distinguish a transient store error from a missing session; Lookup surfaces it
	return ok
}

// restoreSessionE is [RestoreSession] that also reports a TRANSIENT store-read
// failure (errSessionStoreUnavailable) distinctly from "no session on record"
// ((false, nil)). A failed read of Colmena must NOT be mistaken for a logged-out
// device — that was the cause of spurious logouts on every Colmena blip.
func (ss *SessionStore) restoreSessionE(clientID string) (bool, error) {
	if ss.cs == nil {
		return false, nil
	}
	rec, ok, err := ss.cs.GetSession(clientID)
	if err != nil {
		log.Printf("Failed to read session for client %s: %v", clientID, err)
		return false, errSessionStoreUnavailable
	}
	if !ok {
		return false, nil
	}
	return ss.restoreFromRecord(rec), nil
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
	// Fresh scraper (empty jar), so Relogin performs a clean login and
	// auto-submits the TOTP guard code when a secret is configured, then
	// persists the session.
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

// DropSession removes a session from the in-memory map and its durable record,
// so it is neither served nor kept alive/re-logged-in. Used to retire a
// duplicate account session once its token has been re-pointed as an alias.
func (ss *SessionStore) DropSession(clientID string) {
	ss.mu.Lock()
	delete(ss.sessions, clientID)
	ss.mu.Unlock()
	if ss.cs != nil {
		if err := ss.cs.DeleteSession(clientID); err != nil {
			log.Printf("Failed to delete duplicate session %s: %v", clientID, err)
		}
	}
}

// CanonicalForUser returns the clientID of the live session that should own the
// single MV session for the given account, preferring a configured API token
// (its session is re-established at boot) over an app-device session. Returns
// false when no authenticated session for that account exists yet.
func (ss *SessionStore) CanonicalForUser(user string, isAPIToken func(string) bool) (string, bool) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	canonical, found := "", false
	for id, s := range ss.sessions {
		if s == nil || s.Status != "authenticated" || s.Scraper == nil {
			continue
		}
		if !strings.EqualFold(s.Scraper.Username(), user) {
			continue
		}
		switch {
		case !found:
			canonical, found = id, true
		case isAPIToken != nil && isAPIToken(id) && !isAPIToken(canonical):
			// Prefer an API-token clientID over an app-device one.
			canonical = id
		case (isAPIToken == nil || isAPIToken(id) == isAPIToken(canonical)) && id < canonical:
			// Stable tie-break so reconciliation is deterministic across nodes.
			canonical = id
		}
	}
	return canonical, found
}

// ReconcileAccountSessions enforces the "one MV session per account" invariant
// over the DURABLE session records. Older app builds gave each device its own MV
// session (a second concurrent login for the same account) that fought the boot
// session for the single session MV allows and left the app stuck re-logging in.
// For every durable session whose account also has a canonical API-token session
// — i.e. the server's own MV_USERNAME account — it re-points that device token as
// an app alias and retires the duplicate MV login, so existing installs recover
// without the user logging in again. Returns the number of duplicates collapsed.
// Safe with multiple accounts: a device logged into a DIFFERENT account than any
// API token has no canonical to merge onto and keeps its own session untouched.
func (ss *SessionStore) ReconcileAccountSessions(isAPIToken func(string) bool, aliases *AppAliasStore) int {
	if ss.cs == nil {
		return 0
	}
	// Canonical owner per account = the live API-token session serving it (API
	// tokens are always (re)established at boot via AutoLogin).
	canonicalByUser := map[string]string{}
	ss.mu.RLock()
	for id, s := range ss.sessions {
		if s == nil || s.Scraper == nil || !isAPIToken(id) {
			continue
		}
		u := strings.ToLower(s.Scraper.Username())
		if u == "" {
			continue
		}
		if cur, ok := canonicalByUser[u]; !ok || id < cur {
			canonicalByUser[u] = id // stable pick if several API tokens share an account
		}
	}
	ss.mu.RUnlock()

	records, err := ss.cs.AllSessions()
	if err != nil {
		log.Printf("[alias] reconcile: failed to list sessions: %v", err)
		return 0
	}
	collapsed := 0
	for _, r := range records {
		if isAPIToken(r.ClientID) {
			continue // never retire a configured API token's own session
		}
		owner, ok := canonicalByUser[strings.ToLower(r.User)]
		if !ok || owner == r.ClientID {
			continue // no canonical account session, or this already is it
		}
		if aliases != nil {
			aliases.Add(r.ClientID, owner)
		}
		ss.DropSession(r.ClientID)
		collapsed++
		log.Printf("[alias] collapsed duplicate session %s… for %q onto canonical owner", r.ClientID[:min(8, len(r.ClientID))], r.User)
	}
	return collapsed
}
