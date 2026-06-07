package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"time"

	"github.com/mentasystems/colmena"
	"github.com/mentasystems/colmena/fly"
)

// ColmenaStore is the durable, Raft-replicated backing store for the few pieces
// of state that must survive a node dying and be served by any node (PLAN_SCALE
// Phase 2): the per-device MV session bootstrap (token → user/pass + valid
// cookies), FCM push tokens, webhook URLs and mod-forum subscriptions.
//
// Everything churny or cheaply re-derivable stays LOCAL in memory per node and
// never touches the Raft log: the hot *ForumScraper (its live http.Client +
// cookiejar + uTLS transport), the csrfToken/threadID/… hot fields, and the
// last-bubble-count deltas. The cardinal rule: per-poll cookie refreshes must
// NOT replicate — only a materially-changed auth cookie set (re-login / guard
// resolution) is written here, debounced by the caller.
//
// When Colmena is not active (local dev, or a single-machine deploy without the
// Fly environment) StartColmena returns nil and the app keeps its existing
// JSON-on-volume persistence — so this integration is inert until enabled.
type ColmenaStore struct {
	cluster *fly.Cluster
	db      *sql.DB
}

// StartColmena boots a Colmena node from the Fly machine environment and forms /
// joins the Raft cluster. It is gated: with no Fly env (FLY_APP_NAME unset) or
// COLMENA_DISABLED=1 it returns (nil, nil) and the caller falls back to the
// existing single-node persistence. Returns the store, or nil when disabled.
func StartColmena() *ColmenaStore {
	// Explicit opt-in: Colmena only forms a cluster when COLMENA_ENABLED=1. This
	// keeps the current single-machine prod inert (it has the Fly env but not the
	// flag) until the multi-node deploy is deliberately switched on.
	if os.Getenv("COLMENA_ENABLED") != "1" {
		log.Printf("[colmena] disabled (set COLMENA_ENABLED=1 to form a cluster) — using local JSON persistence")
		return nil
	}

	cfg, err := fly.FromEnv()
	if err != nil {
		log.Printf("[colmena] fly.FromEnv failed, staying single-node: %v", err)
		return nil
	}
	cfg.DataDir = envOr("COLMENA_DATA_DIR", "/data")
	cfg.RaftPort = 9000
	cfg.VoterQuorum = 3

	cluster, err := fly.Start(cfg)
	if err != nil {
		log.Printf("[colmena] cluster start failed, staying single-node: %v", err)
		return nil
	}

	// Weak consistency on the request path (always fresh from the leader, ~tiny
	// staleness window); the few durable writes go through Raft regardless.
	db := cluster.Node.OpenDB("mv", colmena.ConsistencyWeak)
	cs := &ColmenaStore{cluster: cluster, db: db}
	if merr := cs.migrate(); merr != nil {
		log.Printf("[colmena] schema migrate failed: %v", merr)
	}
	log.Printf("[colmena] node %s up in region %s (Raft on :%d)", cfg.NodeID, cfg.Region, cfg.RaftPort)
	return cs
}

func (cs *ColmenaStore) migrate() error {
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
		`CREATE TABLE IF NOT EXISTS webhooks (
			username TEXT PRIMARY KEY,
			url      TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS mod_forums (
			username TEXT NOT NULL,
			slug     TEXT NOT NULL,
			PRIMARY KEY (username, slug)
		)`,
	}
	for _, s := range stmts {
		if _, err := cs.db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// Healthy reports whether this node has joined the cluster and caught up — wired
// into fly.toml's health check so a rolling deploy keeps quorum.
func (cs *ColmenaStore) Healthy() bool {
	return cs != nil && cs.cluster != nil && cs.cluster.Healthy()
}

// Leave gracefully transfers leadership and removes this node from the cluster
// (called on SIGTERM before the HTTP server shuts down).
func (cs *ColmenaStore) Leave(ctx context.Context) {
	if cs == nil || cs.cluster == nil {
		return
	}
	if err := cs.cluster.GracefulLeave(ctx); err != nil {
		log.Printf("[colmena] graceful leave: %v", err)
	}
}

// --- durable MV session (Raft) ---

// SessionRecord is the replicated bootstrap for one device's MV session. The
// live scraper is rehydrated from this on demand on whichever node serves the
// device; cookies is the serialized savedSession cookie set.
type SessionRecord struct {
	ClientID string
	User     string
	Pass     string
	Cookies  string
}

// PutSession upserts a device's durable MV session. Call ONLY when the auth
// cookie set materially changes (login / guard / re-login) — never on every
// per-poll refresh, or the Raft log floods.
func (cs *ColmenaStore) PutSession(r SessionRecord) error {
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
func (cs *ColmenaStore) GetSession(clientID string) (SessionRecord, bool, error) {
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
func (cs *ColmenaStore) AllSessions() ([]SessionRecord, error) {
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

func (cs *ColmenaStore) DeleteSession(clientID string) error {
	_, err := cs.db.Exec(`DELETE FROM mv_sessions WHERE client_id = ?`, clientID)
	return err
}

// --- durable FCM tokens (Raft) ---

func (cs *ColmenaStore) AddFCMToken(clientID, token string) error {
	_, err := cs.db.Exec(
		`INSERT OR IGNORE INTO fcm_tokens (client_id, token) VALUES (?, ?)`, clientID, token)
	return err
}

func (cs *ColmenaStore) RemoveFCMToken(clientID, token string) error {
	_, err := cs.db.Exec(`DELETE FROM fcm_tokens WHERE client_id = ? AND token = ?`, clientID, token)
	return err
}

func (cs *ColmenaStore) GetFCMTokens(clientID string) ([]string, error) {
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

// --- durable webhooks (Raft) ---

func (cs *ColmenaStore) SetWebhook(username, url string) error {
	_, err := cs.db.Exec(
		`INSERT INTO webhooks (username, url) VALUES (?, ?)
		 ON CONFLICT(username) DO UPDATE SET url=excluded.url`, username, url)
	return err
}

func (cs *ColmenaStore) RemoveWebhook(username string) error {
	_, err := cs.db.Exec(`DELETE FROM webhooks WHERE username = ?`, username)
	return err
}

func (cs *ColmenaStore) AllWebhooks() (map[string]string, error) {
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
