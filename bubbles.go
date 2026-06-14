package main

import (
	"errors"
	"fmt"
	"html"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mentasystems/colmena/jobs"
)

const (
	pollInterval  = 30 * time.Second
	saveEveryN    = 20 // save session to disk every N polls (~10 min at 30s interval)
	refreshEveryN = 10 // refresh session via main page every N polls (~5 min at 30s interval)

	pollWorkers    = 16 // per-node worker goroutines draining poll_session jobs
	breakerMaxSkip = 16 // max consecutive ticks a failing session is skipped (~8 min)
	pushQueueSize  = 4096
	pushWorkers    = 8

	// pollJobType is the colmena job type for a single per-session bubble scrape.
	// Every tick enqueues one job of this type per authenticated session; the
	// cluster-wide rate limit below bounds the AGGREGATE request rate toward
	// mediavida.com across ALL nodes (the claim-time check is race-safe via Raft).
	pollJobType = "poll_session"

	// pollRatePerSec is the cluster-wide budget of bubble scrapes that may START
	// per second across the whole fleet. Conservative on purpose: mediavida.com is
	// a third party we must not hammer. At 20/s a 600-session fleet drains in ~30s,
	// matching the 30s tick — larger fleets simply spread their scrapes out over
	// more ticks rather than bursting. Tune via SetRateLimit if MV tolerates more.
	pollRatePerSec = 20

	// pollJobTimeout caps a single scrape attempt (fetch + possible relogin).
	pollJobTimeout = 25 * time.Second
)

// breakerState backs off a session that keeps failing so a banned/slow endpoint
// doesn't get hammered and doesn't block a worker every tick.
type breakerState struct {
	fails int // consecutive failures
	skip  int // remaining ticks to skip before retrying
}

// pushJob carries a computed bubble increase out of the poll loop to the async
// push workers (so a slow FCM/webhook endpoint never freezes the poll cycle).
type pushJob struct {
	clientID string
	username string
	prev     *Bubbles
	current  *Bubbles
	fav      []ThreadActivity  // per-thread favorite enrichment (nil = count-only)
	mentions []MentionActivity // per-thread mention enrichment (nil = count-only)
}

// BubblesPoller polls bubbles.php for each authenticated session and broadcasts
// changes via the EventHub (SSE) and configured outgoing webhooks.
type BubblesPoller struct {
	cs        *ColmenaStore // durable store — its node backs the jobs.Manager
	hub       *EventHub
	sessions  *SessionStore
	webhooks  *WebhookStore
	telegram  *TelegramBot
	ntfy      *NtfyPublisher     // ntfy push (nil when not configured)
	fcm       *FCMSender         // FCM push (nil when not configured)
	pending   *PendingNotifStore // durable mirror of unseen avisos (badge source)
	modForums *ModForumsStore    // per-mv-user subforum subscriptions

	// jobs is the colmena jobs manager that runs poll_session jobs under a
	// cluster-wide rate limit. Built in Start() from cs.Node(). It may also be
	// injected via SetJobsManager before Start() (e.g. when main.go owns a single
	// shared manager); if nil at Start() the poller creates its own.
	jobs     *jobs.Manager
	ownsJobs bool // true when Start() created bp.jobs (so Stop() must Close it)

	mu     sync.Mutex
	stopCh chan struct{}

	// state mutated by concurrent poll workers — all guarded by stateMu.
	stateMu        sync.Mutex
	modMu          sync.Mutex                                // serializes checkMod (rare mod users) so its maps stay race-free
	prev           map[string]*Bubbles                       // clientID → last known bubbles
	prevFav        map[string]map[string]int                 // clientID → threadKey → last seen unread count (favorites)
	prevMod        map[string]map[string]*ModBubbles         // clientID → slug → last mod counters
	prevReports    map[string]map[string]map[string]struct{} // clientID → slug → set of report keys
	breaker        map[string]*breakerState                  // clientID → failure backoff
	gated          map[string]bool                           // clientID → was skipped last cycle (re-baseline on resume)
	saveCounter    int                                       // counts polls to periodically save sessions
	refreshCounter int                                       // counts polls to periodically refresh sessions via main page

	pushCh chan pushJob
	pushWG sync.WaitGroup
}

// NewBubblesPoller builds the poller. cs is the durable Colmena store whose node
// backs the jobs.Manager used for cluster-wide rate limiting of MV scrapes; it
// must be non-nil (Colmena is the only persistence layer — no fallback).
func NewBubblesPoller(cs *ColmenaStore, hub *EventHub, sessions *SessionStore, webhooks *WebhookStore, telegram *TelegramBot, ntfy *NtfyPublisher, fcm *FCMSender, pending *PendingNotifStore, modForums *ModForumsStore) *BubblesPoller {
	prevMod, prevReports := loadModState()
	return &BubblesPoller{
		cs:          cs,
		hub:         hub,
		sessions:    sessions,
		webhooks:    webhooks,
		telegram:    telegram,
		ntfy:        ntfy,
		fcm:         fcm,
		pending:     pending,
		modForums:   modForums,
		prev:        make(map[string]*Bubbles),
		prevFav:     make(map[string]map[string]int),
		prevMod:     prevMod,
		prevReports: prevReports,
		breaker:     make(map[string]*breakerState),
		gated:       make(map[string]bool),
	}
}

// SetJobsManager lets the integrator inject a pre-built, shared jobs.Manager
// (e.g. one main.go also uses for other job types) instead of letting Start()
// create a poller-owned one. Must be called before Start(). When a manager is
// injected this way, Stop() will NOT close it — its lifecycle stays with the
// owner. When Start() builds its own, Stop() closes it.
func (bp *BubblesPoller) SetJobsManager(m *jobs.Manager) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	bp.jobs = m
	bp.ownsJobs = false
}

// persistModState snapshots the mod-tracking maps to disk. Called after any
// mutation in checkMod / handleNewReports so a restart never re-alerts on
// reports or messages that were already surfaced. Marshaling runs synchronously
// on the poll goroutine (race-free); the disk write happens in the background.
func (bp *BubblesPoller) persistModState() {
	data := marshalModState(bp.prevMod, bp.prevReports)
	go writeModState(data) // goroutine-ok: fire-and-forget async disk write
}

// Start begins polling bubbles.php in a background goroutine.
//
// The per-session scrape now runs as a colmena "poll_session" job: every tick
// enqueues one job per authenticated session and a cluster-wide rate limit on
// the job type bounds the AGGREGATE request rate toward mediavida.com across all
// nodes. The actual scrape (FetchBubbles / Relogin / diff / notify / async push)
// happens in the registered handler, which is exactly what checkOne did before.
func (bp *BubblesPoller) Start() {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	if bp.stopCh != nil {
		return
	}

	// Build (or adopt) the jobs manager. The manager owns the per-node worker
	// pool that drains poll_session jobs; cluster-wide ordering/limiting is
	// enforced at claim time inside colmena, not here.
	if bp.jobs == nil {
		m, err := jobs.New(bp.cs.Node(), jobs.Config{
			Workers:        pollWorkers,
			DefaultTimeout: pollJobTimeout,
		})
		if err != nil {
			// Dev: Colmena is the only persistence — fail loud, no fallback.
			log.Fatalf("[bubbles] jobs.New: %v", err)
		}
		bp.jobs = m
		bp.ownsJobs = true
	}

	// Register the per-session scrape handler. Registration must happen once;
	// Start() is the single startup path so this is safe.
	jobs.Register(bp.jobs, pollJobType, bp.handlePollSession)

	// Cluster-wide rate limit: at most pollRatePerSec scrapes START per second
	// across the ENTIRE fleet. This is the whole point of moving to jobs — the
	// limit is shared, not per-node, so adding nodes never increases MV load.
	if err := jobs.SetRateLimit(bp.jobs, pollJobType, jobs.Rate{N: pollRatePerSec, Per: time.Second}); err != nil {
		log.Printf("[bubbles] SetRateLimit(%s): %v", pollJobType, err)
	}

	bp.stopCh = make(chan struct{})
	bp.pushCh = make(chan pushJob, pushQueueSize)
	for i := 0; i < pushWorkers; i++ {
		bp.pushWG.Add(1)
		go bp.pushWorker() // goroutine-ok: async push worker, drained on Stop via pushWG
	}
	go bp.poll()             // goroutine-ok: long-lived background poller, lives for the process lifetime (stopped via stopCh)
	go bp.dailySummaryLoop() // goroutine-ok: long-lived background loop, lives for the process lifetime (stopped via stopCh)
}

// Stop stops the polling goroutine and drains the async push workers.
func (bp *BubblesPoller) Stop() {
	bp.mu.Lock()
	if bp.stopCh == nil {
		bp.mu.Unlock()
		return
	}
	close(bp.stopCh)
	bp.stopCh = nil
	pushCh := bp.pushCh
	bp.pushCh = nil
	jm := bp.jobs
	ownsJobs := bp.ownsJobs
	if ownsJobs {
		bp.jobs = nil
	}
	bp.mu.Unlock()

	// Close the push queue and wait for in-flight pushes to flush.
	if pushCh != nil {
		close(pushCh)
		bp.pushWG.Wait()
	}

	// Close the jobs manager only if we created it; an injected (shared) manager
	// is the integrator's to close.
	if ownsJobs && jm != nil {
		if err := jm.Close(); err != nil {
			log.Printf("[bubbles] jobs manager close: %v", err)
		}
	}
}

// pushWorker drains async push jobs (webhook / ntfy / FCM) off the poll loop so
// a slow endpoint can never freeze the poll cycle. The prev/current snapshot is
// carried with the job (not re-read) to avoid out-of-order/stale counts.
func (bp *BubblesPoller) pushWorker() {
	defer bp.pushWG.Done()
	for job := range bp.pushCh {
		bp.webhooks.Send(job.username, job.current)
		bp.ntfy.NotifyBubbleIncrease(job.clientID, job.prev, job.current, job.fav, job.mentions)
		bp.fcm.NotifyBubbleIncrease(job.clientID, job.prev, job.current, job.fav, job.mentions)
	}
}

// enqueuePush submits a push job, dropping it if the queue is full (push is
// best-effort; the next change will re-notify) so the poller never blocks.
func (bp *BubblesPoller) enqueuePush(job pushJob) {
	bp.mu.Lock()
	ch := bp.pushCh
	bp.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- job:
	default:
		log.Printf("[bubbles] push queue full, dropping notification for %s", job.username)
	}
}

// sessionRef is a snapshot entry so workers scrape without holding the
// SessionStore lock across the network call.
type sessionRef struct {
	clientID string
	s        *Session
}

// authedSessions snapshots the currently-authenticated sessions under the store
// lock, so the poll workers can run lock-free.
func (bp *BubblesPoller) authedSessions() []sessionRef {
	var list []sessionRef
	bp.sessions.ForEach(func(clientID string, s *Session) {
		if s.Status == "authenticated" && s.Scraper != nil {
			list = append(list, sessionRef{clientID, s})
		}
	})
	return list
}

func (bp *BubblesPoller) poll() {
	log.Printf("[bubbles] polling started (every %s, %d job-workers/node, cluster cap %d/s)",
		pollInterval, pollWorkers, pollRatePerSec)

	// Establish baseline for existing sessions (serial; one-time at startup).
	for _, sr := range bp.authedSessions() {
		current, err := sr.s.Scraper.FetchBubbles()
		bp.stateMu.Lock()
		if err != nil {
			bp.prev[sr.clientID] = &Bubbles{}
		} else {
			bp.prev[sr.clientID] = current
		}
		bp.stateMu.Unlock()
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-bp.stopCh:
			log.Printf("[bubbles] polling stopped")
			return
		case <-ticker.C:
			bp.check()
		}
	}
}

// pollJobArgs is the JSON payload of a poll_session job. It carries only the
// clientID plus the per-tick save/refresh flags — the live *Session/*ForumScraper
// is resolved locally in the handler (it is node-pinned state, never serialized).
type pollJobArgs struct {
	ClientID string `json:"client_id"`
	Save     bool   `json:"save"`
	Refresh  bool   `json:"refresh"`
}

// check enqueues one poll_session job per authenticated session. The jobs are
// drained by the colmena worker pool under a CLUSTER-WIDE rate limit, so the
// aggregate request rate toward mediavida.com stays bounded no matter how many
// nodes are running. WithUniqueKey(clientID) means a session whose previous
// scrape is still pending/running won't pile up a second job — the tick is a
// no-op for that client until the prior one finishes.
func (bp *BubblesPoller) check() {
	// Counters advance once per tick (not per session) to keep intervals predictable.
	bp.saveCounter++
	bp.refreshCounter++
	save := bp.saveCounter%saveEveryN == 0
	refresh := bp.refreshCounter%refreshEveryN == 0

	bp.mu.Lock()
	jm := bp.jobs
	bp.mu.Unlock()
	if jm == nil {
		return
	}

	for _, sr := range bp.authedSessions() {
		args := pollJobArgs{ClientID: sr.clientID, Save: save, Refresh: refresh}
		if _, err := jobs.Enqueue(jm, pollJobType, args,
			jobs.WithUniqueKey(pollUniqueKey(sr.clientID)),
			jobs.WithTimeout(pollJobTimeout),
		); err != nil && err != jobs.ErrDuplicateUnique {
			log.Printf("[bubbles] enqueue poll_session for %s: %v", sr.clientID, err)
		}
	}
}

// pollUniqueKey namespaces the per-client dedup key for poll_session jobs.
func pollUniqueKey(clientID string) string { return "poll:" + clientID }

// handlePollSession is the poll_session job handler. It resolves the (node-local)
// session by clientID and runs the same per-session scrape checkOne did before.
// Jobs are claimed under the cluster-wide rate limit, so this only runs within
// the shared MV request budget. If the session isn't on this node (it migrated,
// disconnected, or the job was claimed elsewhere) the job is a successful no-op.
func (bp *BubblesPoller) handlePollSession(ctx jobs.Context, args pollJobArgs) error {
	s := bp.sessions.Get(args.ClientID)
	if s == nil || s.Status != "authenticated" || s.Scraper == nil {
		return nil // session not here / not authed — nothing to scrape, not an error
	}
	bp.checkOne(args.ClientID, s, args.Save, args.Refresh)
	return nil
}

// checkOne polls one session: honors the per-session circuit breaker, runs the
// periodic save/refresh, gates the scrape on whether anyone is listening, diffs
// the counters and enqueues push asynchronously.
func (bp *BubblesPoller) checkOne(clientID string, s *Session, save, refresh bool) {
	scraper := s.Scraper
	username := scraper.Username()

	// Circuit breaker: skip a failing session for its backoff window.
	bp.stateMu.Lock()
	if br := bp.breaker[clientID]; br != nil && br.skip > 0 {
		br.skip--
		bp.stateMu.Unlock()
		return
	}
	bp.stateMu.Unlock()

	// Periodic cookie save/refresh runs even when gated, so idle sessions don't rot.
	if save {
		scraper.SaveSession()
	}
	if refresh {
		if err := scraper.RefreshSession(); err != nil {
			log.Printf("[bubbles] session refresh failed for %s: %v", username, err)
		} else {
			scraper.SaveSession()
		}
	}

	// Gate: if nobody is listening (no SSE, no FCM token, no webhook) skip the
	// bubble scrape — the biggest idle-load reduction. Mark gated so we
	// re-baseline (no spurious "increase") when the session resumes.
	if !bp.shouldPoll(clientID, username) {
		bp.stateMu.Lock()
		bp.gated[clientID] = true
		bp.stateMu.Unlock()
		return
	}

	current, err := scraper.FetchBubbles()
	if err != nil {
		var expired *ErrSessionExpired
		if errors.As(err, &expired) || !scraper.IsLoggedIn() {
			log.Printf("[bubbles] session invalid for %s, attempting re-login", username)
			if rerr := scraper.Relogin(); rerr != nil {
				var guardErr *ErrGuardRequired
				if errors.As(rerr, &guardErr) {
					bp.webhooks.Send(username, &Bubbles{Messages: -1, Notifications: -1, Favorites: -1})
				}
				bp.recordFailure(clientID)
				return
			}
			current, err = scraper.FetchBubbles()
		}
		if err != nil {
			log.Printf("[bubbles] fetch error (%s): %v", username, err)
			bp.recordFailure(clientID)
			return
		}
	}
	bp.recordSuccess(clientID)

	bp.stateMu.Lock()
	wasGated := bp.gated[clientID]
	delete(bp.gated, clientID)
	prev := bp.prev[clientID]
	if prev == nil || wasGated {
		// First sight or just resumed after gating → (re)baseline, no notify.
		bp.prev[clientID] = current
		bp.stateMu.Unlock()
		bp.runCheckMod(clientID, s)
		return
	}
	changed := current.Notifications != prev.Notifications ||
		current.Messages != prev.Messages ||
		current.Favorites != prev.Favorites
	bp.prev[clientID] = current
	trackingFav := len(bp.prevFav[clientID]) > 0
	bp.stateMu.Unlock()

	// Enrichment (extra MV scrapes to name the thread / mentioning user) only
	// runs for push-enabled devices, so SSE/web-only clients keep the lighter
	// path and the aggregate MV request rate stays bounded.
	var favThreads []ThreadActivity
	var mentions []MentionActivity
	if bp.fcm.HasTokens(clientID) {
		// Favorites: fetch when bf rose OR while we are still tracking unread
		// favorite threads — extra posts in an ALREADY-unread thread don't bump
		// bf, and that "3 mensajes nuevos en X" case is exactly the point.
		if current.Favorites > prev.Favorites || trackingFav {
			favThreads = bp.enrichFavorites(clientID, scraper)
		}
		// Mentions: fetch the feed when bn rose. This marks them seen on MV
		// (bn→0); enrichMentions mirrors the unseen set into the pending store so
		// the badge survives, then groups per thread for the push.
		if current.Notifications > prev.Notifications {
			mentions = bp.enrichMentions(clientID, scraper, current.Notifications)
		}
	}

	if changed {
		log.Printf("[bubbles] changed (%s): bm=%d→%d bn=%d→%d bf=%d→%d", username,
			prev.Messages, current.Messages,
			prev.Notifications, current.Notifications,
			prev.Favorites, current.Favorites)
		bp.notifyClient(clientID, current) // SSE — in-process, fast (effective bn)
		scraper.SaveSession()
	}

	// Push when any counter changed, or when an already-unread favorite thread
	// gained posts (no bubble change, but favThreads is non-empty).
	if changed || len(favThreads) > 0 {
		bp.enqueuePush(pushJob{
			clientID: clientID, username: username,
			prev: prev, current: current,
			fav: favThreads, mentions: mentions,
		})
	}

	bp.runCheckMod(clientID, s)
}

// enrichFavorites scrapes the favorites page and returns the threads whose
// unread count rose since the last poll (so the push can name the thread and
// show "N mensajes nuevos en …"). It also updates prevFav so an unchanged
// thread doesn't re-notify and a fully-read thread is dropped (re-notifies on
// the next new post). Returns nil on scrape error.
func (bp *BubblesPoller) enrichFavorites(clientID string, scraper *ForumScraper) []ThreadActivity {
	favs, err := scraper.FetchFavorites()
	if err != nil {
		log.Printf("[bubbles] favorites enrich failed for %s: %v", clientID, err)
		return nil
	}

	bp.stateMu.Lock()
	defer bp.stateMu.Unlock()
	prevMap := bp.prevFav[clientID]
	newMap := make(map[string]int)

	var out []ThreadActivity
	for _, f := range favs {
		// Only threads with an unread badge are "new activity".
		if f.URL == "" || f.Title == "" || f.UnreadCount == "" {
			continue
		}
		key := threadKey(f.URL)
		cnt := parseUnread(f.UnreadCount)
		newMap[key] = cnt
		prevCnt, seen := prevMap[key]
		// Notify on a freshly-unread thread, or one whose count grew. When the
		// count is unknown ("new", cnt==0) only notify the first time we see it.
		if !seen || cnt > prevCnt {
			out = append(out, ThreadActivity{Title: f.Title, URL: f.URL, Key: key, Count: cnt})
		}
	}
	bp.prevFav[clientID] = newMap
	return out
}

// enrichMentions fetches the notifications feed (marking everything seen on MV),
// mirrors the unseen items into the pending store so the in-app badge survives,
// and returns one MentionActivity per thread with the cumulative unseen count
// (so the per-thread push can be updated in place as more avisos arrive).
func (bp *BubblesPoller) enrichMentions(clientID string, scraper *ForumScraper, unread int) []MentionActivity {
	items, err := scraper.FetchNotifications()
	if err != nil {
		log.Printf("[bubbles] mentions enrich failed for %s: %v", clientID, err)
		return nil
	}
	// The feed is newest-first; the first `unread` entries are the unseen ones.
	if unread > len(items) {
		unread = len(items)
	}
	if unread <= 0 {
		return nil
	}
	unseen := items[:unread]
	bp.pending.Add(clientID, unseen)

	// Cumulative unseen-per-thread from the durable store (covers avisos from
	// earlier ticks that the user hasn't opened yet).
	perThread := bp.pending.CountByThread(clientID)

	// One MentionActivity per thread touched this tick, newest item as the face.
	seen := make(map[string]bool)
	var out []MentionActivity
	for _, it := range unseen {
		key := threadKey(it.URL)
		if seen[key] {
			continue
		}
		seen[key] = true
		cnt := perThread[key]
		if cnt == 0 {
			cnt = 1
		}
		out = append(out, MentionActivity{
			Key: key, Author: it.Author, Text: it.Text,
			Target: it.Target, URL: it.URL, Count: cnt,
		})
	}
	return out
}

// runCheckMod serializes the mod-tracking work (rare mod users) so its maps stay
// race-free under the concurrent worker pool.
func (bp *BubblesPoller) runCheckMod(clientID string, s *Session) {
	if bp.telegram == nil {
		return
	}
	bp.modMu.Lock()
	defer bp.modMu.Unlock()
	bp.checkMod(clientID, s)
}

func (bp *BubblesPoller) recordFailure(clientID string) {
	bp.stateMu.Lock()
	defer bp.stateMu.Unlock()
	br := bp.breaker[clientID]
	if br == nil {
		br = &breakerState{}
		bp.breaker[clientID] = br
	}
	br.fails++
	skip := 1 << min(br.fails, 4) // 2, 4, 8, 16 ticks
	if skip > breakerMaxSkip {
		skip = breakerMaxSkip
	}
	br.skip = skip
}

func (bp *BubblesPoller) recordSuccess(clientID string) {
	bp.stateMu.Lock()
	defer bp.stateMu.Unlock()
	if br := bp.breaker[clientID]; br != nil {
		br.fails = 0
		br.skip = 0
	}
}

// shouldPoll reports whether anyone is listening for this client's bubbles: an
// SSE subscriber, a registered FCM token, or a webhook. If none, the scrape is
// skipped (gated) — this preserves app-closed FCM/ntfy/webhook push, which is
// the whole reason to keep polling without an SSE subscriber.
func (bp *BubblesPoller) shouldPoll(clientID, username string) bool {
	return bp.hub.HasSubscribers(clientID) ||
		bp.fcm.HasTokens(clientID) ||
		bp.webhooks.Has(username)
}

// checkMod polls the subforums this user subscribed to via /mod/forums and
// fires a Telegram alert when a counter increases (new reports or new mod messages).
func (bp *BubblesPoller) checkMod(clientID string, s *Session) {
	if bp.telegram == nil {
		return
	}
	slugs := bp.modForums.Get(s.Scraper.Username())
	if len(slugs) == 0 {
		return
	}

	prevBySlug, ok := bp.prevMod[clientID]
	if !ok {
		prevBySlug = make(map[string]*ModBubbles)
		bp.prevMod[clientID] = prevBySlug
	}

	for _, slug := range slugs {
		current, err := s.Scraper.FetchModBubbles(slug)
		if err != nil {
			log.Printf("[mod] fetch failed (%s, /foro/%s): %v", s.Scraper.Username(), slug, err)
			continue
		}
		if current == nil {
			// Not a mod of this subforum
			continue
		}

		var prevReports, prevMessages int
		if prev, havePrev := prevBySlug[slug]; havePrev {
			prevReports = prev.Reports
			prevMessages = prev.Messages
		} else {
			log.Printf("[mod] initial fetch (%s, /foro/%s): reports=%d messages=%d — surfacing existing items as new",
				s.Scraper.Username(), slug, current.Reports, current.Messages)
		}
		prevBySlug[slug] = current
		bp.persistModState()

		if current.Reports > prevReports {
			bp.handleNewReports(clientID, slug, s, current)
		}

		if current.Messages > prevMessages {
			log.Printf("[mod] messages changed (%s, /foro/%s): %d→%d",
				s.Scraper.Username(), slug, prevMessages, current.Messages)
			url := fmt.Sprintf("https://www.mediavida.com/mensajes/mod?fid=%s", current.ForumID)
			text := fmt.Sprintf("Mensaje(s) de mod en /foro/%s: %d pendiente(s).\n%s",
				slug, current.Messages, url)
			bp.telegram.SendModAlert(s.Scraper.Username(), text)
		}
	}
}

// handleNewReports fetches the full report list, compares against previously
// seen keys, and sends a detailed Telegram alert for each genuinely new report.
func (bp *BubblesPoller) handleNewReports(clientID, slug string, s *Session, mb *ModBubbles) {
	reports, err := s.Scraper.FetchModReports(mb.ForumID)
	if err != nil {
		log.Printf("[mod] reports fetch failed (%s, /foro/%s): %v — sending fallback alert",
			s.Scraper.Username(), slug, err)
		url := fmt.Sprintf("https://www.mediavida.com/foro/reportes.php?fid=%s", mb.ForumID)
		text := fmt.Sprintf("Nuevo(s) reporte(s) en /foro/%s (%d pendiente(s)).\n%s",
			slug, mb.Reports, url)
		bp.telegram.SendModAlert(s.Scraper.Username(), text)
		return
	}

	prevKeys, ok := bp.prevReports[clientID][slug]
	if !ok {
		prevKeys = make(map[string]struct{})
		if bp.prevReports[clientID] == nil {
			bp.prevReports[clientID] = make(map[string]map[string]struct{})
		}
		bp.prevReports[clientID][slug] = prevKeys
	}

	currentKeys := make(map[string]struct{}, len(reports))
	for i := range reports {
		currentKeys[reports[i].Key()] = struct{}{}
	}

	var newReports []ModReport
	for i := range reports {
		if _, seen := prevKeys[reports[i].Key()]; !seen {
			newReports = append(newReports, reports[i])
		}
	}

	bp.prevReports[clientID][slug] = currentKeys
	bp.persistModState()

	if len(newReports) == 0 {
		// Counter went up but no new keys (e.g. baseline effect on first ever fetch).
		// Still send a summary so the mod knows something changed.
		url := fmt.Sprintf("https://www.mediavida.com/foro/reportes.php?fid=%s", mb.ForumID)
		text := fmt.Sprintf("Cambio en reportes de /foro/%s (%d pendiente(s)).\n%s", slug, mb.Reports, url)
		bp.telegram.SendModAlert(s.Scraper.Username(), text)
		return
	}

	for i := range newReports {
		text := formatReportAlert(slug, &newReports[i])
		bp.telegram.SendModAlert(s.Scraper.Username(), text)
	}
	log.Printf("[mod] %d new report(s) for %s in /foro/%s", len(newReports), s.Scraper.Username(), slug)
}

// dailySummaryLoop sends a daily mod summary at 21:00 Europe/Madrid.
func (bp *BubblesPoller) dailySummaryLoop() {
	if bp.telegram == nil {
		return
	}
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		log.Printf("[mod-summary] timezone load failed, using UTC: %v", err)
		loc = time.UTC
	}
	for {
		now := time.Now().In(loc)
		next := time.Date(now.Year(), now.Month(), now.Day(), 21, 0, 0, 0, loc)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		wait := time.Until(next)
		log.Printf("[mod-summary] next run in %s (at %s)", wait.Round(time.Second), next.Format(time.RFC3339))
		select {
		case <-bp.stopCh:
			return
		case <-time.After(wait):
		}
		bp.sendDailySummary()
	}
}

func (bp *BubblesPoller) sendDailySummary() {
	bp.sessions.ForEach(func(clientID string, s *Session) {
		if s.Status != "authenticated" || s.Scraper == nil {
			return
		}
		slugs := bp.modForums.Get(s.Scraper.Username())
		if len(slugs) == 0 {
			return
		}
		for _, slug := range slugs {
			mb, err := s.Scraper.FetchModBubbles(slug)
			if err != nil {
				log.Printf("[mod-summary] fetch bubbles failed (%s, /foro/%s): %v",
					s.Scraper.Username(), slug, err)
				continue
			}
			if mb == nil {
				continue
			}
			text := bp.buildDailySummary(slug, s, mb)
			bp.telegram.SendModAlert(s.Scraper.Username(), text)
			log.Printf("[mod-summary] sent for %s /foro/%s (reports=%d messages=%d)",
				s.Scraper.Username(), slug, mb.Reports, mb.Messages)
		}
	})
}

func (bp *BubblesPoller) buildDailySummary(slug string, s *Session, mb *ModBubbles) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("📋 <b>Resumen diario — /foro/%s</b>", html.EscapeString(slug)))

	if mb.Reports == 0 && mb.Messages == 0 {
		lines = append(lines, "")
		lines = append(lines, "Sin reportes ni mensajes de mod pendientes ✅")
		return strings.Join(lines, "\n")
	}

	lines = append(lines, fmt.Sprintf("<b>Reportes pendientes:</b> %d", mb.Reports))
	if mb.Messages > 0 {
		lines = append(lines, fmt.Sprintf("<b>Mensajes de mod pendientes:</b> %d", mb.Messages))
	}

	if mb.Reports > 0 {
		reports, err := s.Scraper.FetchModReports(mb.ForumID)
		if err != nil {
			log.Printf("[mod-summary] reports fetch failed (%s, /foro/%s): %v",
				s.Scraper.Username(), slug, err)
			lines = append(lines, "")
			lines = append(lines, fmt.Sprintf("https://www.mediavida.com/foro/reportes.php?fid=%s", html.EscapeString(mb.ForumID)))
			return strings.Join(lines, "\n")
		}
		for i := range reports {
			r := &reports[i]
			lines = append(lines, "")
			lines = append(lines, fmt.Sprintf("• <b>%s</b>", html.EscapeString(r.ThreadTitle)))
			if r.PostAuthor != "" {
				lines = append(lines, fmt.Sprintf("  <b>Autor:</b> %s", html.EscapeString(r.PostAuthor)))
			}
			if r.Reporter != "" {
				if r.ReportedAgo != "" {
					lines = append(lines, fmt.Sprintf("  <b>Reportado por:</b> %s (%s)",
						html.EscapeString(r.Reporter), html.EscapeString(r.ReportedAgo)))
				} else {
					lines = append(lines, fmt.Sprintf("  <b>Reportado por:</b> %s", html.EscapeString(r.Reporter)))
				}
			}
			if r.Reason != "" {
				lines = append(lines, fmt.Sprintf("  <b>Motivo:</b> %s", html.EscapeString(r.Reason)))
			}
			if r.PostURL != "" {
				lines = append(lines, "  "+html.EscapeString(r.PostURL))
			}
		}
	}

	if mb.Messages > 0 {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("https://www.mediavida.com/mensajes/mod?fid=%s", html.EscapeString(mb.ForumID)))
	}

	return strings.Join(lines, "\n")
}

func formatReportAlert(slug string, r *ModReport) string {
	const maxBody = 400

	body := r.PostBody
	if len(body) > maxBody {
		body = body[:maxBody] + "…"
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("🚨 <b>Nuevo reporte en /foro/%s</b>", html.EscapeString(slug)))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("<b>Hilo:</b> %s", html.EscapeString(r.ThreadTitle)))
	lines = append(lines, fmt.Sprintf("<b>Autor:</b> %s", html.EscapeString(r.PostAuthor)))
	if r.Reporter != "" {
		if r.ReportedAgo != "" {
			lines = append(lines, fmt.Sprintf("<b>Reportado por:</b> %s (%s)",
				html.EscapeString(r.Reporter), html.EscapeString(r.ReportedAgo)))
		} else {
			lines = append(lines, fmt.Sprintf("<b>Reportado por:</b> %s", html.EscapeString(r.Reporter)))
		}
	}
	if r.Reason != "" {
		lines = append(lines, fmt.Sprintf("<b>Motivo:</b> %s", html.EscapeString(r.Reason)))
	}
	lines = append(lines, "")
	lines = append(lines, html.EscapeString(body))
	if r.PostURL != "" {
		lines = append(lines, "")
		lines = append(lines, html.EscapeString(r.PostURL))
	}
	return strings.Join(lines, "\n")
}

func (bp *BubblesPoller) notifyClient(clientID string, bubbles *Bubbles) {
	bp.hub.Publish(clientID, "bubbles", map[string]int{
		"messages":      bubbles.Messages,
		"notifications": bp.effectiveNotifications(clientID, bubbles.Notifications),
		"favorites":     bubbles.Favorites,
	})
}

// effectiveNotifications is the avisos badge value the app should show: the
// larger of MV's live bn and our pending mirror. After we enrich (which marks
// avisos seen on MV → bn=0) the pending count is the source of truth; before
// the poller has caught up to a fresh aviso, MV's bn is higher. Either way the
// badge stays correct, and it clears once the app opens the feed (pending
// cleared) and MV reports 0. nil-safe (pending store may be unconfigured).
func (bp *BubblesPoller) effectiveNotifications(clientID string, mvBn int) int {
	if bp.pending == nil {
		return mvBn
	}
	if c := bp.pending.Count(clientID); c > mvBn {
		return c
	}
	return mvBn
}
