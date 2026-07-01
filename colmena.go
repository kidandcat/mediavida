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
// Colmena is the ONLY persistence — there is no JSON-on-volume fallback and no
// enable gate. In a Fly environment (FLY_APP_NAME set) the node forms / joins a
// Raft cluster; otherwise it runs as a single bootstrap node against a local
// data dir for dev. Either way the *colmena.Node is the live SQL handle.
type ColmenaStore struct {
	node    *colmena.Node // underlying Colmena node — present in both modes
	cluster *fly.Cluster  // non-nil only in Fly cluster mode
	db      *sql.DB
}

// StartColmena boots the durable store. It NEVER returns nil and has no enable
// gate: Colmena is the only persistence layer.
//
//   - Fly cluster mode (FLY_APP_NAME set): builds a Config from the Fly machine
//     env, pins DataDir=/data, RaftPort=9000, VoterQuorum=3, and forms / joins
//     the Raft cluster via fly.Start.
//   - Local dev mode (no Fly env): a single bootstrap node bound to localhost
//     against COLMENA_DATA_DIR (default ./colmena-data).
//
// On any hard start error it log.Fatals — this is dev, fail loud, no fallback.
func StartColmena() *ColmenaStore {
	if os.Getenv("FLY_APP_NAME") != "" {
		return startColmenaCluster()
	}
	return startColmenaLocal()
}

// startColmenaCluster forms / joins the Raft cluster from the Fly env.
func startColmenaCluster() *ColmenaStore {
	cfg, err := fly.FromEnv()
	if err != nil {
		log.Fatalf("[colmena] fly.FromEnv: %v", err)
	}
	cfg.DataDir = "/data"
	cfg.RaftPort = 9000
	// Single-node topology: one voter, one machine. Collapsing the former 3-node
	// Raft cluster removes the cross-node in-memory thread-state races AND the
	// re-login storm (3 egress IPs logging the same MV account in tripped MV's
	// guard — see SINGLE.md in the colmena module). One node = one egress = no
	// storm. The tradeoff is no HA: a single volume, so a machine/volume loss is
	// downtime (acceptable for a personal app; add litestream for mv.db backup).
	cfg.VoterQuorum = 1

	// One-shot migration from the old 3-voter cluster to a single-server config.
	// The persisted Raft configuration lists 3 voters, so a lone surviving node
	// would recover that config and never reach quorum (needs 2 of 3) → stuck
	// read-only. Deleting ONLY the Raft state (raft.db + snapshots/) while keeping
	// every <name>.db SQLite store makes HasExistingState report false, so the
	// node cold-starts and bootstraps a fresh single-server config that adopts the
	// existing mv.db in place — all durable rows (sessions, webhooks, mod_forums,
	// watch_tokens) survive untouched. Set COLMENA_RESET_RAFT=1 for the migrating
	// deploy, then remove it (subsequent boots recover the 1-server config normally).
	if os.Getenv("COLMENA_RESET_RAFT") == "1" {
		resetRaftStateKeepingStores(cfg.DataDir)
	}

	cluster, err := fly.Start(cfg)
	if err != nil {
		log.Fatalf("[colmena] cluster start: %v", err)
	}

	// Weak consistency on the request path (always fresh from the leader, ~tiny
	// staleness window); the few durable writes go through Raft regardless.
	db := cluster.Node.OpenDB("mv", colmena.ConsistencyWeak)
	cs := &ColmenaStore{node: cluster.Node, cluster: cluster, db: db}
	// A write (CREATE TABLE) needs a leader; wait for the election to settle.
	if lerr := cluster.Node.WaitForLeader(30 * time.Second); lerr != nil {
		log.Fatalf("[colmena] no leader elected: %v", lerr)
	}
	if merr := cs.migrate(); merr != nil {
		log.Fatalf("[colmena] schema migrate: %v", merr)
	}
	log.Printf("[colmena] node %s up in region %s (Raft on :%d)", cfg.NodeID, cfg.Region, cfg.RaftPort)
	return cs
}

// resetRaftStateKeepingStores deletes the persisted Raft log + snapshots so a
// node that used to belong to a multi-voter cluster cold-starts and bootstraps a
// fresh single-server configuration, WITHOUT discarding the replicated SQLite
// stores. Unlike colmena.WipeLocalState (which also removes every <name>.db so a
// wiped node re-fetches from the leader), this keeps mv.db and its sidecars, so
// the durable rows are adopted in place by the new single-node cluster. Must run
// before fly.Start opens the data dir (no live Node holding the BoltDB lock).
func resetRaftStateKeepingStores(dataDir string) {
	for _, p := range []string{"raft.db", "snapshots"} {
		full := dataDir + "/" + p
		if err := os.RemoveAll(full); err != nil && !os.IsNotExist(err) {
			log.Printf("[colmena] reset raft state: remove %s: %v", full, err)
		} else {
			log.Printf("[colmena] reset raft state: removed %s", full)
		}
	}
}

// startColmenaLocal brings up a single-node bootstrap cluster for dev.
func startColmenaLocal() *ColmenaStore {
	node, err := colmena.New(colmena.Config{
		NodeID:    "local",
		DataDir:   envOr("COLMENA_DATA_DIR", "./colmena-data"),
		Bind:      "127.0.0.1:9000",
		Bootstrap: true,
	})
	if err != nil {
		log.Fatalf("[colmena] local node start: %v", err)
	}

	db := node.OpenDB("mv", colmena.ConsistencyWeak)
	cs := &ColmenaStore{node: node, db: db}
	// A write (CREATE TABLE) needs a leader; wait for the bootstrap election.
	if lerr := node.WaitForLeader(30 * time.Second); lerr != nil {
		log.Fatalf("[colmena] no leader elected: %v", lerr)
	}
	if merr := cs.migrate(); merr != nil {
		log.Fatalf("[colmena] schema migrate: %v", merr)
	}
	log.Printf("[colmena] local node up (Raft on 127.0.0.1:9000, data %q)", envOr("COLMENA_DATA_DIR", "./colmena-data"))
	return cs
}

// Node exposes the underlying Colmena node for the jobs subsystem (which opens
// its own DB / registers handlers on the same Raft log).
func (cs *ColmenaStore) Node() *colmena.Node {
	return cs.node
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
		// pending_notifs decouples the in-app "avisos" badge from MV's bn
		// counter. Enriching a mention push requires fetching /notificaciones,
		// which marks everything seen on MV (bn→0) before the user has looked.
		// We mirror the unseen items here so the badge — and the per-thread push
		// count — survive that side effect; the rows are cleared when the app
		// opens the notifications feed.
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

// Healthy reports whether this node is ready to serve — wired into fly.toml's
// health check so a rolling deploy keeps quorum. Always true in local dev mode
// (a single bootstrap node is healthy as soon as it starts).
func (cs *ColmenaStore) Healthy() bool {
	if cs == nil {
		return false
	}
	if cs.cluster != nil {
		return cs.cluster.Healthy()
	}
	return true
}

// Leave gracefully shuts the node down (called on SIGTERM before the HTTP
// server stops). In cluster mode it transfers leadership and removes the node
// from the configuration; in local mode it just closes the node.
func (cs *ColmenaStore) Leave(ctx context.Context) {
	if cs == nil {
		return
	}
	if cs.cluster != nil {
		if err := cs.cluster.GracefulLeave(ctx); err != nil {
			log.Printf("[colmena] graceful leave: %v", err)
		}
		return
	}
	if cs.node != nil {
		if err := cs.node.Close(); err != nil {
			log.Printf("[colmena] close: %v", err)
		}
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

// localRead carries ConsistencyNone so a query reads this node's local SQLite
// replica directly, with NO communication to the leader. Session reads use it
// because the default (ConsistencyWeak) forwards reads to the leader, so a
// leader election or quorum blip makes the read fail — which the auth gate then
// (used to) surface as a 401 and bounce a perfectly-valid logged-in device to
// the login screen. A local read can't fail that way; stale session cookies are
// harmless here (they change only on login/guard/re-login and are long-lived).
func localRead() context.Context {
	return colmena.WithConsistency(context.Background(), colmena.ConsistencyNone)
}

// GetSession reads one device's durable session (ok=false if absent).
//
// It reads the LOCAL replica first (blip-proof: no leader contact). On a local
// miss it falls back to a leader-routed read to catch a session just created on
// another node and not yet replicated here — and if there's no reachable leader
// at that moment, that read errors and the caller maps it to a retryable 503,
// never a false "logged out". The common case (an established session, already
// replicated locally) is served entirely from the local replica and so survives
// leader elections untouched.
func (cs *ColmenaStore) GetSession(clientID string) (SessionRecord, bool, error) {
	if r, ok, err := cs.scanSession(localRead(), clientID); err != nil || ok {
		return r, ok, err
	}
	// Local miss: double-check the leader (default consistency) for a freshly
	// written session that hasn't replicated to this node yet.
	return cs.scanSession(context.Background(), clientID)
}

func (cs *ColmenaStore) scanSession(ctx context.Context, clientID string) (SessionRecord, bool, error) {
	var r SessionRecord
	err := cs.db.QueryRowContext(ctx,
		`SELECT client_id, mv_user, mv_pass, cookies FROM mv_sessions WHERE client_id = ?`, clientID,
	).Scan(&r.ClientID, &r.User, &r.Pass, &r.Cookies)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// AllSessions returns every durable session (used to rehydrate hot scrapers at
// boot, replacing RestoreAllFromDisk). Reads the local replica — a node
// rehydrates whatever it has locally and restores any not-yet-replicated
// session on demand via GetSession later.
func (cs *ColmenaStore) AllSessions() ([]SessionRecord, error) {
	rows, err := cs.db.QueryContext(localRead(), `SELECT client_id, mv_user, mv_pass, cookies FROM mv_sessions`)
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

// --- pending notifications (Raft) ---

// AddPendingNotif mirrors one unseen MV notification into the pending set
// (deduped by its MV id). created_at is supplied by the caller so the row is
// deterministic across Raft replicas.
func (cs *ColmenaStore) AddPendingNotif(clientID, notifID, url string, createdAt int64) error {
	_, err := cs.db.Exec(
		`INSERT OR IGNORE INTO pending_notifs (client_id, notif_id, url, created_at) VALUES (?, ?, ?, ?)`,
		clientID, notifID, url, createdAt)
	return err
}

// CountPendingNotifs returns how many unseen notifications are mirrored for a client.
func (cs *ColmenaStore) CountPendingNotifs(clientID string) (int, error) {
	var n int
	err := cs.db.QueryRow(`SELECT COUNT(*) FROM pending_notifs WHERE client_id = ?`, clientID).Scan(&n)
	return n, err
}

// PendingNotifURLs returns the post URLs of every mirrored unseen notification
// (used to group the per-thread push count).
func (cs *ColmenaStore) PendingNotifURLs(clientID string) ([]string, error) {
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
func (cs *ColmenaStore) ClearPendingNotifs(clientID string) error {
	_, err := cs.db.Exec(`DELETE FROM pending_notifs WHERE client_id = ?`, clientID)
	return err
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

// --- durable mod-forum subscriptions (Raft) ---

// AddModForum subscribes a moderator to a forum slug (idempotent).
func (cs *ColmenaStore) AddModForum(username, slug string) error {
	_, err := cs.db.Exec(
		`INSERT OR IGNORE INTO mod_forums (username, slug) VALUES (?, ?)`, username, slug)
	return err
}

// RemoveModForum drops one moderator's subscription to a forum slug.
func (cs *ColmenaStore) RemoveModForum(username, slug string) error {
	_, err := cs.db.Exec(`DELETE FROM mod_forums WHERE username = ? AND slug = ?`, username, slug)
	return err
}

// GetModForums returns the forum slugs a moderator is subscribed to.
func (cs *ColmenaStore) GetModForums(username string) ([]string, error) {
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
func (cs *ColmenaStore) AllModForums() (map[string][]string, error) {
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

// --- durable watch pairing tokens (Raft) ---

// WatchTokenRecord is one paired watch: a bearer token that aliases an owner's
// MV session so the watch can fetch /bubbles autonomously after a one-time pair.
type WatchTokenRecord struct {
	Token     string
	Owner     string
	Label     string
	CreatedAt int64
}

// AddWatchToken registers (or relabels) a paired-watch token for an owner.
func (cs *ColmenaStore) AddWatchToken(token, owner, label string) error {
	_, err := cs.db.Exec(
		`INSERT INTO watch_tokens (token, owner, label, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(token) DO UPDATE SET owner=excluded.owner, label=excluded.label`,
		token, owner, label, time.Now().Unix(),
	)
	return err
}

// ListWatchTokens returns an owner's paired watches, newest first.
func (cs *ColmenaStore) ListWatchTokens(owner string) ([]WatchTokenRecord, error) {
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
func (cs *ColmenaStore) GetWatchTokenOwner(token string) (string, bool, error) {
	var owner string
	err := cs.db.QueryRow(`SELECT owner FROM watch_tokens WHERE token = ?`, token).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return owner, err == nil, err
}

// DeleteWatchToken removes a paired-watch token (revocation).
func (cs *ColmenaStore) DeleteWatchToken(token string) error {
	_, err := cs.db.Exec(`DELETE FROM watch_tokens WHERE token = ?`, token)
	return err
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
