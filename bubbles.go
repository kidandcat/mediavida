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
)

// BubblesPoller polls bubbles.php for each authenticated session and broadcasts
// changes via the EventHub (SSE) and configured outgoing webhooks.
type BubblesPoller struct {
	hub       *EventHub
	sessions  *SessionStore
	webhooks  *WebhookStore
	telegram  *TelegramBot
	modForums *ModForumsStore // per-mv-user subforum subscriptions

	mu             sync.Mutex
	stopCh         chan struct{}
	prev           map[string]*Bubbles                       // clientID → last known bubbles
	prevMod        map[string]map[string]*ModBubbles         // clientID → slug → last mod counters
	prevReports    map[string]map[string]map[string]struct{} // clientID → slug → set of report keys
	saveCounter    int                                       // counts polls to periodically save sessions
	refreshCounter int                                       // counts polls to periodically refresh sessions via main page
}

func NewBubblesPoller(hub *EventHub, sessions *SessionStore, webhooks *WebhookStore, telegram *TelegramBot, modForums *ModForumsStore) *BubblesPoller {
	prevMod, prevReports := loadModState()
	return &BubblesPoller{
		hub:         hub,
		sessions:    sessions,
		webhooks:    webhooks,
		telegram:    telegram,
		modForums:   modForums,
		prev:        make(map[string]*Bubbles),
		prevMod:     prevMod,
		prevReports: prevReports,
	}
}

// persistModState snapshots the mod-tracking maps to disk. Called after any
// mutation in checkMod / handleNewReports so a restart never re-alerts on
// reports or messages that were already surfaced. Marshaling runs synchronously
// on the poll goroutine (race-free); the disk write happens in the background.
func (bp *BubblesPoller) persistModState() {
	data := marshalModState(bp.prevMod, bp.prevReports)
	go writeModState(data)
}

// Start begins polling bubbles.php in a background goroutine.
func (bp *BubblesPoller) Start() {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	if bp.stopCh != nil {
		return
	}
	bp.stopCh = make(chan struct{})
	go bp.poll()
	go bp.dailySummaryLoop()
}

// Stop stops the polling goroutine.
func (bp *BubblesPoller) Stop() {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	if bp.stopCh != nil {
		close(bp.stopCh)
		bp.stopCh = nil
	}
}

func (bp *BubblesPoller) poll() {
	log.Printf("[bubbles] polling started (every %s)", pollInterval)

	// Establish baseline for existing sessions
	bp.sessions.ForEach(func(clientID string, s *Session) {
		if s.Status != "authenticated" || s.Scraper == nil {
			return
		}
		prev, err := s.Scraper.FetchBubbles()
		if err != nil {
			log.Printf("[bubbles] initial fetch failed for %s: %v", s.Scraper.Username(), err)
			bp.prev[clientID] = &Bubbles{}
		} else {
			bp.prev[clientID] = prev
			log.Printf("[bubbles] baseline (%s): bm=%d bn=%d bf=%d", s.Scraper.Username(), prev.Messages, prev.Notifications, prev.Favorites)
		}
	})

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

func (bp *BubblesPoller) check() {
	// Increment counters once per tick (not per session) to keep intervals predictable
	bp.saveCounter++
	bp.refreshCounter++

	bp.sessions.ForEach(func(clientID string, s *Session) {
		if s.Status != "authenticated" || s.Scraper == nil {
			return
		}

		// Periodically save session to keep cookies on disk fresh.
		// Runs regardless of whether FetchBubbles succeeds.
		if bp.saveCounter%saveEveryN == 0 {
			s.Scraper.SaveSession()
		}

		// Periodically refresh session by visiting main page (like a browser would).
		// Runs regardless of whether FetchBubbles succeeds.
		if bp.refreshCounter%refreshEveryN == 0 {
			if err := s.Scraper.RefreshSession(); err != nil {
				log.Printf("[bubbles] session refresh failed for %s: %v", s.Scraper.Username(), err)
			} else {
				s.Scraper.SaveSession()
				log.Printf("[bubbles] session refreshed and saved for %s", s.Scraper.Username())
			}
		}

		current, err := s.Scraper.FetchBubbles()
		if err != nil {
			var expired *ErrSessionExpired
			if errors.As(err, &expired) || !s.Scraper.IsLoggedIn() {
				log.Printf("[bubbles] session invalid for %s, attempting re-login", s.Scraper.Username())
				if err := s.Scraper.Relogin(); err != nil {
					log.Printf("[bubbles] re-login failed for %s: %v", s.Scraper.Username(), err)
					// Notify via webhook that re-auth is needed
					if _, ok := err.(*ErrGuardRequired); ok {
						bp.webhooks.Send(s.Scraper.Username(), &Bubbles{Messages: -1, Notifications: -1, Favorites: -1})
					}
					return
				}
				current, err = s.Scraper.FetchBubbles()
				if err != nil {
					log.Printf("[bubbles] fetch error after re-login (%s): %v", s.Scraper.Username(), err)
					return
				}
			} else {
				log.Printf("[bubbles] fetch error (%s): %v", s.Scraper.Username(), err)
				return
			}
		}

		prev := bp.prev[clientID]
		if prev == nil {
			bp.prev[clientID] = current
			log.Printf("[bubbles] baseline set (%s): bm=%d bn=%d bf=%d", s.Scraper.Username(), current.Messages, current.Notifications, current.Favorites)
			return
		}

		changed := current.Notifications != prev.Notifications ||
			current.Messages != prev.Messages ||
			current.Favorites != prev.Favorites

		if changed {
			log.Printf("[bubbles] changed (%s): bm=%d→%d bn=%d→%d bf=%d→%d",
				s.Scraper.Username(),
				prev.Messages, current.Messages,
				prev.Notifications, current.Notifications,
				prev.Favorites, current.Favorites)

			bp.notifyClient(clientID, current)
			bp.webhooks.Send(s.Scraper.Username(), current)
			// Save session after change to persist any refreshed cookies
			s.Scraper.SaveSession()
		}

		bp.prev[clientID] = current

		bp.checkMod(clientID, s)
	})
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
		"notifications": bubbles.Notifications,
		"favorites":     bubbles.Favorites,
	})
}
