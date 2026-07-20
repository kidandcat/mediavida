package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite")
)

// Store is the durable backing store: a single local SQLite file (pure-Go
// modernc.org/sqlite driver, CGO-free) holding the few pieces of state that
// must survive a restart: the per-device MV session bootstrap (token →
// user/pass + valid cookies), FCM push tokens, pending notifications, webhook
// URLs, mod-forum subscriptions and watch/app token aliases.
//
// It replaces the previous Colmena (distributed SQLite/Raft) store — the
// deployment is single-node, so a plain file DB is enough. The schema is
// byte-identical to what Colmena's OpenDB("mv") created, so an existing mv.db
// keeps working as-is (live sessions survive the swap).
//
// Everything churny or cheaply re-derivable stays in memory: the hot
// *ForumScraper (its live http.Client + cookiejar + uTLS transport), the
// csrfToken/threadID/… hot fields, and the last-bubble-count deltas. The
// cardinal rule: per-poll cookie refreshes must NOT be written here — only a
// materially-changed auth cookie set (re-login / guard resolution) is
// persisted, debounced by the caller.
type Store struct {
	db *sql.DB
}

// OpenStore opens (creating if needed) the SQLite database and applies the
// schema. The path comes from MV_DB (default ./data/mv.db). WAL + a busy
// timeout keep the single-node server responsive under concurrent
// reads/writes. On any hard open error it log.Fatals — fail loud, no fallback.
func OpenStore() *Store {
	path := envOr("MV_DB", "./data/mv.db")
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("[store] create data dir %s: %v", dir, err)
		}
	}
	// Same pragmas Colmena used for its writer handle, so an existing mv.db is
	// adopted in place: WAL journal, 5s busy timeout, synchronous=NORMAL.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatalf("[store] open sqlite %s: %v", path, err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("[store] ping sqlite %s: %v", path, err)
	}
	s := &Store{db: db}
	if merr := s.migrate(); merr != nil {
		log.Fatalf("[store] schema migrate: %v", merr)
	}
	log.Printf("[store] sqlite open (path %q, WAL)", path)
	return s
}

func (cs *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS mv_sessions (
			client_id  TEXT PRIMARY KEY,
			mv_user    TEXT NOT NULL,
			mv_pass    TEXT NOT NULL,
			cookies    TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS fcm_tokens (
			client_id TEXT NOT NULL,
			token     TEXT NOT NULL,
			PRIMARY KEY (client_id, token)
		)`,
		// pending_notifs was a mirror of unseen avisos for the in-app badge when
		// the poller used to mark them seen while enriching pushes. The poller
		// no longer does that; the table remains for GET /notifications.
		`CREATE TABLE IF NOT EXISTS pending_notifs (
			client_id  TEXT NOT NULL,
			notif_id   TEXT NOT NULL,
			url        TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			PRIMARY KEY (client_id, notif_id)
		)`,
		`CREATE TABLE IF NOT EXISTS webhooks (
			username TEXT PRIMARY KEY,
			url      TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mod_forums (
			username TEXT NOT NULL,
			slug     TEXT NOT NULL,
			PRIMARY KEY (username, slug)
		)`,
		`CREATE TABLE IF NOT EXISTS watch_tokens (
			token      TEXT PRIMARY KEY,
			owner      TEXT NOT NULL,
			label      TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		)`,
		// app_aliases maps a phone's per-install device token to the single MV
		// session for its account (the owner clientID), mirroring watch_tokens.
		// A device token used to get its OWN MV session (a full second login for
		// the same account), which fought the server's boot session for the one
		// Mediavida session MV allows per account — a perpetual mutual-logout
		// storm that left the app stuck on 503. It now aliases the shared session
		// instead: one MV login per account, shared by boot/phone/watch.
		`CREATE TABLE IF NOT EXISTS app_aliases (
			token      TEXT PRIMARY KEY,
			owner      TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := cs.db.Exec(s); err != nil {
			return err
		}
	}
	// Idempotent cleanup: earlier versions minted watch tokens by COPYING the
	// owner's MV session under a `wf_` client_id, creating a second concurrent
	// Mediavida login per account (guard/login storms). Watch tokens now alias
	// the owner's session at request time, so drop any leftover copied watch
	// sessions. The watch_tokens mapping rows are kept.
	if _, err := cs.db.Exec(`DELETE FROM mv_sessions WHERE client_id LIKE 'wf_%'`); err != nil {
		return err
	}
	return nil
}

// Healthy reports whether the store is ready to serve — wired into the /health
// endpoint. A plain DB ping: the single-node SQLite file is healthy as long as
// it answers queries.
func (cs *Store) Healthy() bool {
	if cs == nil {
		return false
	}
	return cs.db.Ping() == nil
}

// Close closes the database (called on SIGTERM after the HTTP server stops).
func (cs *Store) Close() {
	if cs == nil {
		return
	}
	if err := cs.db.Close(); err != nil {
		log.Printf("[store] close: %v", err)
	}
}

// --- durable MV session ---

// SessionRecord is the durable bootstrap for one device's MV session. The
// live scraper is rehydrated from this on demand; cookies is the serialized
// savedSession cookie set.
type SessionRecord struct {
	ClientID string
	User     string
	Pass     string
	Cookies  string
}

// PutSession upserts a device's durable MV session. Call ONLY when the auth
// cookie set materially changes (login / guard / re-login) — never on every
// per-poll refresh, so the write load stays negligible.
func (cs *Store) PutSession(r SessionRecord) error {
	_, err := cs.db.Exec(
		`INSERT INTO mv_sessions (client_id, mv_user, mv_pass, cookies, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(client_id) DO UPDATE SET mv_user=excluded.mv_user,
		   mv_pass=excluded.mv_pass, cookies=excluded.cookies, updated_at=excluded.updated_at`,
		r.ClientID, r.User, r.Pass, r.Cookies, time.Now().Unix(),
	)
	return err
}

// GetSession reads one device's durable session (ok=false if absent).
func (cs *Store) GetSession(clientID string) (SessionRecord, bool, error) {
	var r SessionRecord
	err := cs.db.QueryRow(
		`SELECT client_id, mv_user, mv_pass, cookies FROM mv_sessions WHERE client_id = ?`, clientID,
	).Scan(&r.ClientID, &r.User, &r.Pass, &r.Cookies)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// AllSessions returns every durable session (used to rehydrate hot scrapers at
// boot, replacing RestoreAllFromDisk).
func (cs *Store) AllSessions() ([]SessionRecord, error) {
	rows, err := cs.db.Query(`SELECT client_id, mv_user, mv_pass, cookies FROM mv_sessions`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // safe-ignore: best-effort cursor close
	var out []SessionRecord
	for rows.Next() {
		var r SessionRecord
		if serr := rows.Scan(&r.ClientID, &r.User, &r.Pass, &r.Cookies); serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (cs *Store) DeleteSession(clientID string) error {
	_, err := cs.db.Exec(`DELETE FROM mv_sessions WHERE client_id = ?`, clientID)
	return err
}

// --- durable FCM tokens ---

func (cs *Store) AddFCMToken(clientID, token string) error {
	_, err := cs.db.Exec(
		`INSERT OR IGNORE INTO fcm_tokens (client_id, token) VALUES (?, ?)`, clientID, token)
	return err
}

func (cs *Store) RemoveFCMToken(clientID, token string) error {
	_, err := cs.db.Exec(`DELETE FROM fcm_tokens WHERE client_id = ? AND token = ?`, clientID, token)
	return err
}

func (cs *Store) GetFCMTokens(clientID string) ([]string, error) {
	rows, err := cs.db.Query(`SELECT token FROM fcm_tokens WHERE client_id = ?`, clientID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // safe-ignore: best-effort cursor close
	var out []string
	for rows.Next() {
		var t string
		if serr := rows.Scan(&t); serr != nil {
			return nil, serr
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- pending notifications ---

// AddPendingNotif mirrors one unseen MV notification into the pending set
// (deduped by its MV id). created_at is supplied by the caller so the row is
// deterministic.
func (cs *Store) AddPendingNotif(clientID, notifID, url string, createdAt int64) error {
	_, err := cs.db.Exec(
		`INSERT OR IGNORE INTO pending_notifs (client_id, notif_id, url, created_at) VALUES (?, ?, ?, ?)`,
		clientID, notifID, url, createdAt)
	return err
}

// CountPendingNotifs returns how many unseen notifications are mirrored for a client.
func (cs *Store) CountPendingNotifs(clientID string) (int, error) {
	var n int
	err := cs.db.QueryRow(`SELECT COUNT(*) FROM pending_notifs WHERE client_id = ?`, clientID).Scan(&n)
	return n, err
}

// PendingNotifURLs returns the post URLs of every mirrored unseen notification
// (used to group the per-thread push count).
func (cs *Store) PendingNotifURLs(clientID string) ([]string, error) {
	rows, err := cs.db.Query(`SELECT url FROM pending_notifs WHERE client_id = ?`, clientID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // safe-ignore: best-effort cursor close
	var out []string
	for rows.Next() {
		var u string
		if serr := rows.Scan(&u); serr != nil {
			return nil, serr
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ClearPendingNotifs drops every mirrored notification for a client (called when
// the app opens the notifications feed, which also marks them seen on MV).
func (cs *Store) ClearPendingNotifs(clientID string) error {
	_, err := cs.db.Exec(`DELETE FROM pending_notifs WHERE client_id = ?`, clientID)
	return err
}

// --- durable webhooks ---

func (cs *Store) SetWebhook(username, url string) error {
	_, err := cs.db.Exec(
		`INSERT INTO webhooks (username, url) VALUES (?, ?)
		 ON CONFLICT(username) DO UPDATE SET url=excluded.url`, username, url)
	return err
}

func (cs *Store) RemoveWebhook(username string) error {
	_, err := cs.db.Exec(`DELETE FROM webhooks WHERE username = ?`, username)
	return err
}

func (cs *Store) AllWebhooks() (map[string]string, error) {
	rows, err := cs.db.Query(`SELECT username, url FROM webhooks`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // safe-ignore: best-effort cursor close
	out := make(map[string]string)
	for rows.Next() {
		var u, url string
		if serr := rows.Scan(&u, &url); serr != nil {
			return nil, serr
		}
		out[u] = url
	}
	return out, rows.Err()
}

// --- durable mod-forum subscriptions ---

// AddModForum subscribes a moderator to a forum slug (idempotent).
func (cs *Store) AddModForum(username, slug string) error {
	_, err := cs.db.Exec(
		`INSERT OR IGNORE INTO mod_forums (username, slug) VALUES (?, ?)`, username, slug)
	return err
}

// RemoveModForum drops one moderator's subscription to a forum slug.
func (cs *Store) RemoveModForum(username, slug string) error {
	_, err := cs.db.Exec(`DELETE FROM mod_forums WHERE username = ? AND slug = ?`, username, slug)
	return err
}

// GetModForums returns the forum slugs a moderator is subscribed to.
func (cs *Store) GetModForums(username string) ([]string, error) {
	rows, err := cs.db.Query(`SELECT slug FROM mod_forums WHERE username = ?`, username)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // safe-ignore: best-effort cursor close
	var out []string
	for rows.Next() {
		var slug string
		if serr := rows.Scan(&slug); serr != nil {
			return nil, serr
		}
		out = append(out, slug)
	}
	return out, rows.Err()
}

// AllModForums returns every mod-forum subscription, keyed by moderator
// username → forum slugs.
func (cs *Store) AllModForums() (map[string][]string, error) {
	rows, err := cs.db.Query(`SELECT username, slug FROM mod_forums`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // safe-ignore: best-effort cursor close
	out := make(map[string][]string)
	for rows.Next() {
		var u, slug string
		if serr := rows.Scan(&u, &slug); serr != nil {
			return nil, serr
		}
		out[u] = append(out[u], slug)
	}
	return out, rows.Err()
}

// --- durable watch pairing tokens ---

// WatchTokenRecord is one paired watch: a bearer token that aliases an owner's
// MV session so the watch can fetch /bubbles autonomously after a one-time pair.
type WatchTokenRecord struct {
	Token     string
	Owner     string
	Label     string
	CreatedAt int64
}

// AddWatchToken registers (or relabels) a paired-watch token for an owner.
func (cs *Store) AddWatchToken(token, owner, label string) error {
	_, err := cs.db.Exec(
		`INSERT INTO watch_tokens (token, owner, label, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(token) DO UPDATE SET owner=excluded.owner, label=excluded.label`,
		token, owner, label, time.Now().Unix(),
	)
	return err
}

// ListWatchTokens returns an owner's paired watches, newest first.
func (cs *Store) ListWatchTokens(owner string) ([]WatchTokenRecord, error) {
	rows, err := cs.db.Query(
		`SELECT token, owner, label, created_at FROM watch_tokens WHERE owner = ? ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }() // safe-ignore: best-effort cursor close
	var out []WatchTokenRecord
	for rows.Next() {
		var r WatchTokenRecord
		if serr := rows.Scan(&r.Token, &r.Owner, &r.Label, &r.CreatedAt); serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetWatchTokenOwner resolves a watch token to its owner (ok=false if absent).
func (cs *Store) GetWatchTokenOwner(token string) (string, bool, error) {
	var owner string
	err := cs.db.QueryRow(`SELECT owner FROM watch_tokens WHERE token = ?`, token).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return owner, err == nil, err
}

// DeleteWatchToken removes a paired-watch token (revocation).
func (cs *Store) DeleteWatchToken(token string) error {
	_, err := cs.db.Exec(`DELETE FROM watch_tokens WHERE token = ?`, token)
	return err
}

// AddAppAlias records that a phone device token aliases the given owner's MV
// session (idempotent; re-pointing an existing token updates the owner).
func (cs *Store) AddAppAlias(token, owner string) error {
	_, err := cs.db.Exec(
		`INSERT INTO app_aliases (token, owner, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(token) DO UPDATE SET owner=excluded.owner`,
		token, owner, time.Now().Unix(),
	)
	return err
}

// GetAppAliasOwner resolves a device token to its owner (ok=false if absent).
func (cs *Store) GetAppAliasOwner(token string) (string, bool, error) {
	var owner string
	err := cs.db.QueryRow(`SELECT owner FROM app_aliases WHERE token = ?`, token).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return owner, err == nil, err
}

// DeleteAppAlias removes a device-token alias.
func (cs *Store) DeleteAppAlias(token string) error {
	_, err := cs.db.Exec(`DELETE FROM app_aliases WHERE token = ?`, token)
	return err
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
