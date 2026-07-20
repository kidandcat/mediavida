package main

import (
	"errors"
	"fmt"
	"html"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	pollInterval  = 30 * time.Second
	saveEveryN    = 20 // save session to disk every N polls (~10 min at 30s interval)
	refreshEveryN = 10 // refresh session via main page every N polls (~5 min at 30s interval)

	pollWorkers    = 16 // worker goroutines draining poll-session jobs
	breakerMaxSkip = 16 // max consecutive ticks a failing session is skipped (~8 min)
	pushQueueSize  = 4096
	pushWorkers    = 8
	pollQueueSize  = 4096 // buffered poll-job queue (one entry per session per tick)

	// pollRatePerSec is the budget of bubble scrapes that may START per second.
	// Conservative on purpose: mediavida.com is a third party we must not hammer.
	// At 20/s a 600-session set drains in ~30s, matching the 30s tick — larger
	// sets simply spread their scrapes out over more ticks rather than bursting.
	// Enforced by a shared ticker the workers block on before each scrape.
	pollRatePerSec = 20
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
	cs        *Store // durable store (badge/pending reads on the poll path)
	hub       *EventHub
	sessions  *SessionStore
	webhooks  *WebhookStore
	telegram  *TelegramBot
	ntfy      *NtfyPublisher     // ntfy push (nil when not configured)
	fcm       *FCMSender         // FCM push (nil when not configured)
	pending   *PendingNotifStore // durable mirror of unseen avisos (badge source)
	modForums *ModForumsStore    // per-mv-user subforum subscriptions

	// In-process poll worker pool (replaces the former colmena distributed jobs
	// queue — single node now, no distribution needed). Every tick enqueues one
	// pollJobArgs per authenticated session onto pollCh; pollWorkers goroutines
	// drain it, each blocking on pollLimiter (a shared ticker at 1/pollRatePerSec)
	// before starting a scrape, so the aggregate request rate toward
	// mediavida.com stays bounded. inflight dedups per client: a session whose
	// previous scrape is still queued/running is skipped that tick.
	pollCh      chan pollJobArgs
	pollWG      sync.WaitGroup
	pollLimiter *time.Ticker

	inflightMu sync.Mutex
	inflight   map[string]bool // clientID → a poll job is queued or running

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

// NewBubblesPoller builds the poller. cs is the durable SQLite store; it must
// be non-nil (it is the only persistence layer — no fallback).
func NewBubblesPoller(cs *Store, hub *EventHub, sessions *SessionStore, webhooks *WebhookStore, telegram *TelegramBot, ntfy *NtfyPublisher, fcm *FCMSender, pending *PendingNotifStore, modForums *ModForumsStore) *BubblesPoller {
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
		inflight:    make(map[string]bool),
	}
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
// The per-session scrape runs on an in-process worker pool: every tick
// enqueues one poll job per authenticated session and the shared rate limiter
// bounds the aggregate request rate toward mediavida.com. The actual scrape
// (FetchBubbles / Relogin / diff / notify / async push) happens in
// handlePollSession, which is exactly what checkOne did before.
func (bp *BubblesPoller) Start() {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	if bp.stopCh != nil {
		return
	}

	bp.stopCh = make(chan struct{})
	bp.pollCh = make(chan pollJobArgs, pollQueueSize)
	bp.pollLimiter = time.NewTicker(time.Second / pollRatePerSec)
	for i := 0; i < pollWorkers; i++ {
		bp.pollWG.Add(1)
		go bp.pollWorker(bp.stopCh, bp.pollCh) // goroutine-ok: poll worker, drained on Stop via pollWG
	}
	bp.pushCh = make(chan pushJob, pushQueueSize)
	for i := 0; i < pushWorkers; i++ {
		bp.pushWG.Add(1)
		go bp.pushWorker() // goroutine-ok: async push worker, drained on Stop via pushWG
	}
	go bp.poll()             // goroutine-ok: long-lived background poller, lives for the process lifetime (stopped via stopCh)
	go bp.dailySummaryLoop() // goroutine-ok: long-lived background loop, lives for the process lifetime (stopped via stopCh)
}

// Stop stops the polling goroutine and drains the poll and push workers (in
// that order — poll workers are the only producers of push jobs).
func (bp *BubblesPoller) Stop() {
	bp.mu.Lock()
	if bp.stopCh == nil {
		bp.mu.Unlock()
		return
	}
	close(bp.stopCh)
	bp.stopCh = nil
	pollCh := bp.pollCh
	bp.pollCh = nil
	limiter := bp.pollLimiter
	bp.pollLimiter = nil
	bp.mu.Unlock()

	// Close the poll queue and wait for the workers to drain (stopCh is closed,
	// so they discard queued jobs without scraping).
	if pollCh != nil {
		close(pollCh)
		bp.pollWG.Wait()
	}
	if limiter != nil {
		limiter.Stop()
	}

	// Now that no producer is left, close the push queue and flush in-flight pushes.
	bp.mu.Lock()
	pushCh := bp.pushCh
	bp.pushCh = nil
	bp.mu.Unlock()
	if pushCh != nil {
		close(pushCh)
		bp.pushWG.Wait()
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
	log.Printf("[bubbles] polling started (every %s, %d job-workers, rate cap %d/s)",
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

// pollJobArgs is the payload of one poll-session job. It carries only the
// clientID plus the per-tick save/refresh flags — the live *Session/*ForumScraper
// is resolved in the handler (it is hot in-memory state, never serialized).
type pollJobArgs struct {
	ClientID string
	Save     bool
	Refresh  bool
}

// check enqueues one poll job per authenticated session. The jobs are drained
// by the in-process worker pool under the shared rate limit, so the aggregate
// request rate toward mediavida.com stays bounded. The inflight dedup means a
// session whose previous scrape is still queued/running won't pile up a second
// job — the tick is a no-op for that client until the prior one finishes.
func (bp *BubblesPoller) check() {
	// Counters advance once per tick (not per session) to keep intervals predictable.
	bp.saveCounter++
	bp.refreshCounter++
	save := bp.saveCounter%saveEveryN == 0
	refresh := bp.refreshCounter%refreshEveryN == 0

	bp.mu.Lock()
	ch := bp.pollCh
	bp.mu.Unlock()
	if ch == nil {
		return
	}

	for _, sr := range bp.authedSessions() {
		if !bp.markInflight(sr.clientID) {
			continue // previous scrape still queued/running — skip this tick
		}
		select {
		case ch <- pollJobArgs{ClientID: sr.clientID, Save: save, Refresh: refresh}:
		default:
			bp.clearInflight(sr.clientID)
			log.Printf("[bubbles] poll queue full, skipping %s this tick", sr.clientID)
		}
	}
}

// markInflight claims the per-client poll slot; false when a job for this
// client is already queued or running (the dedup that replaces the former
// distributed-queue unique key).
func (bp *BubblesPoller) markInflight(clientID string) bool {
	bp.inflightMu.Lock()
	defer bp.inflightMu.Unlock()
	if bp.inflight[clientID] {
		return false
	}
	bp.inflight[clientID] = true
	return true
}

// clearInflight releases the per-client poll slot.
func (bp *BubblesPoller) clearInflight(clientID string) {
	bp.inflightMu.Lock()
	delete(bp.inflight, clientID)
	bp.inflightMu.Unlock()
}

// pollWorker drains poll jobs off the queue, pacing scrape starts on the shared
// rate limiter so the aggregate request rate toward mediavida.com never exceeds
// pollRatePerSec no matter how many workers are running. On shutdown (stop
// closed) it discards the remaining queued jobs without scraping.
func (bp *BubblesPoller) pollWorker(stop <-chan struct{}, ch <-chan pollJobArgs) {
	defer bp.pollWG.Done()
	for args := range ch {
		select {
		case <-stop:
			bp.clearInflight(args.ClientID) // shutting down — drop without scraping
			continue
		case <-bp.pollLimiterC():
		}
		bp.handlePollSession(args)
		bp.clearInflight(args.ClientID)
	}
}

// pollLimiterC snapshots the limiter channel under the lock (Stop nils the
// ticker); a nil channel blocks forever, which is fine because Stop has already
// closed stopCh so the select's stop arm fires.
func (bp *BubblesPoller) pollLimiterC() <-chan time.Time {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.pollLimiter == nil {
		return nil
	}
	return bp.pollLimiter.C
}

// handlePollSession is the poll-job handler. It resolves the session by
// clientID and runs the same per-session scrape checkOne did before. If the
// session is gone or no longer authenticated the job is a no-op.
func (bp *BubblesPoller) handlePollSession(args pollJobArgs) {
	s := bp.sessions.Get(args.ClientID)
	if s == nil || s.Status != "authenticated" || s.Scraper == nil {
		return // session gone / not authed — nothing to scrape
	}
	bp.checkOne(args.ClientID, s, args.Save, args.Refresh)
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

	// Favorites enrichment (extra MV scrape to name the thread) only runs for
	// push-enabled devices, so SSE/web-only clients keep the lighter path.
	// Avisos are intentionally NOT enriched: visiting /notificaciones marks
	// every aviso as seen on MV, which would clear them on the website (and in
	// the app WebView). Aviso pushes use the count-only body instead.
	var favThreads []ThreadActivity
	if bp.fcm.HasTokens(clientID) {
		// Favorites: fetch when bf rose OR while we are still tracking unread
		// favorite threads — extra posts in an ALREADY-unread thread don't bump
		// bf, and that "3 mensajes nuevos en X" case is exactly the point.
		// /foro/favoritos is non-destructive (does not mark unread as read).
		if current.Favorites > prev.Favorites || trackingFav {
			favThreads = bp.enrichFavorites(clientID, scraper)
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
			fav: favThreads, mentions: nil,
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
// larger of MV's live bn and our pending mirror. The poller no longer marks
// avisos seen (so bn stays live), but GET /notifications still uses pending
// for clients that open the feed via the API. nil-safe.
func (bp *BubblesPoller) effectiveNotifications(clientID string, mvBn int) int {
	if bp.pending == nil {
		return mvBn
	}
	if c := bp.pending.Count(clientID); c > mvBn {
		return c
	}
	return mvBn
}
