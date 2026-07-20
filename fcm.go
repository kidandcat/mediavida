package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// FCMTokenStore maps a client (device bearer token) to its FCM registration
// tokens, durably persisted in the durable SQLite store. A client can have more than one
// token (reinstalls, token refresh) until the stale ones are pruned on send
// failure.
type FCMTokenStore struct {
	cs *Store
}

// NewFCMTokenStore builds a token store backed by the durable SQLite store.
func NewFCMTokenStore(cs *Store) *FCMTokenStore {
	return &FCMTokenStore{cs: cs}
}

// Add registers an FCM token for a client (deduped at the storage layer).
func (s *FCMTokenStore) Add(clientID, token string) {
	if err := s.cs.AddFCMToken(clientID, token); err != nil {
		log.Printf("[fcm] failed to add token for %s: %v", clientID, err)
	}
}

// Remove deletes a specific token from a client (e.g. when FCM reports it stale).
func (s *FCMTokenStore) Remove(clientID, token string) {
	if err := s.cs.RemoveFCMToken(clientID, token); err != nil {
		log.Printf("[fcm] failed to remove token for %s: %v", clientID, err)
	}
}

// Get returns the client's registered tokens (empty slice on error).
func (s *FCMTokenStore) Get(clientID string) []string {
	tokens, err := s.cs.GetFCMTokens(clientID)
	if err != nil {
		log.Printf("[fcm] failed to get tokens for %s: %v", clientID, err)
		return nil
	}
	return tokens
}

// FCMSender sends push via the FCM HTTP v1 API using a service account. nil when
// FCM_SA_JSON / FCM_PROJECT_ID are not configured.
type FCMSender struct {
	projectID string
	tokens    oauth2.TokenSource
	store     *FCMTokenStore
	client    *http.Client
}

// NewFCMSender builds the sender from env. Returns nil (push via FCM disabled)
// when the service account or project id is missing.
func NewFCMSender(store *FCMTokenStore) *FCMSender {
	saJSON, projectID := os.Getenv("FCM_SA_JSON"), os.Getenv("FCM_PROJECT_ID")
	if saJSON == "" || projectID == "" {
		log.Printf("[fcm] disabled (FCM_SA_JSON or FCM_PROJECT_ID not set)")
		return nil
	}
	cfg, err := google.JWTConfigFromJSON([]byte(saJSON),
		"https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		log.Printf("[fcm] disabled: bad service account JSON: %v", err)
		return nil
	}
	log.Printf("[fcm] enabled for project %s", projectID)
	return &FCMSender{
		projectID: projectID,
		tokens:    cfg.TokenSource(context.Background()),
		store:     store,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// fcmV1Message is the FCM HTTP v1 request body. A `notification` block is
// included so the OS displays it even with the app closed (required on iOS).
//
// Grouping: android.notification.tag and the apns-collapse-id header carry a
// per-thread key, so a second push about the same thread REPLACES the first in
// the tray (rather than stacking) even with the app closed. The same key rides
// along in `data` so the foreground handler can mirror that behaviour.
type fcmV1Message struct {
	Message struct {
		Token        string `json:"token"`
		Notification struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"notification"`
		Data    map[string]string `json:"data,omitempty"`
		Android struct {
			Priority     string             `json:"priority"`
			Notification *androidNotifBlock `json:"notification,omitempty"`
		} `json:"android"`
		APNS struct {
			Headers map[string]string `json:"headers"`
		} `json:"apns"`
	} `json:"message"`
}

// androidNotifBlock overrides only the Android `tag` (collapse key); title/body
// stay inherited from the common notification block.
type androidNotifBlock struct {
	Tag string `json:"tag,omitempty"`
}

// pushMsg is one notification to deliver: title/body plus an optional grouping
// tag and a data payload for the in-app (foreground) renderer.
type pushMsg struct {
	title string
	body  string
	tag   string            // per-thread grouping key ("" = no grouping)
	data  map[string]string // mirrored into the FCM data block
}

const (
	// fcmMaxConcurrency bounds the number of in-flight FCM POSTs per
	// NotifyBubbleIncrease call (FCM HTTP v1 has no multicast endpoint).
	fcmMaxConcurrency = 8
	// fcmMaxRetries is the number of extra attempts after the first on a
	// transient (429 / 5xx) FCM response.
	fcmMaxRetries = 3
	// fcmBaseBackoff is the base delay for exponential backoff between retries.
	fcmBaseBackoff = 500 * time.Millisecond
	// fcmMaxBackoff caps a single backoff wait.
	fcmMaxBackoff = 10 * time.Second
)

// sendOne posts a single notification to one token, retrying on transient
// (429 / 5xx) FCM responses with bounded exponential backoff (honouring a
// Retry-After header when present), and pruning the token on a 404/400.
func (f *FCMSender) sendOne(clientID, token string, msg pushMsg) {
	var m fcmV1Message
	m.Message.Token = token
	m.Message.Notification.Title = msg.title
	m.Message.Notification.Body = msg.body
	m.Message.Data = msg.data
	m.Message.Android.Priority = "high"
	m.Message.APNS.Headers = map[string]string{"apns-priority": "10"}
	if msg.tag != "" {
		m.Message.Android.Notification = &androidNotifBlock{Tag: msg.tag}
		// apns-collapse-id replaces a prior iOS notification with the same id.
		// Capped at 64 bytes by APNs; threadKey() keeps it short ("t123456").
		m.Message.APNS.Headers["apns-collapse-id"] = msg.tag
	}

	payload, _ := json.Marshal(m) // safe-ignore: marshaling a static struct never fails
	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", f.projectID)

	for attempt := 0; ; attempt++ {
		retryAfter, done := f.sendAttempt(clientID, token, url, payload)
		if done {
			return
		}
		if attempt >= fcmMaxRetries {
			log.Printf("[fcm] giving up on token for %s after %d retries", clientID, fcmMaxRetries)
			return
		}
		wait := retryAfter
		if wait <= 0 {
			wait = fcmBackoff(attempt)
		}
		time.Sleep(wait)
	}
}

// sendAttempt performs a single FCM POST. It returns done=true when no further
// retry should happen (success, prune, non-retryable error, or unrecoverable
// local error), and otherwise done=false with a suggested Retry-After delay
// (0 when none was supplied, so the caller falls back to exponential backoff).
func (f *FCMSender) sendAttempt(clientID, token, url string, payload []byte) (retryAfter time.Duration, done bool) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		return 0, true
	}
	req.Header.Set("Content-Type", "application/json")

	tok, err := f.tokens.Token()
	if err != nil {
		log.Printf("[fcm] oauth token error: %v", err)
		return 0, true
	}
	tok.SetAuthHeader(req)

	resp, err := f.client.Do(req)
	if err != nil {
		// Network/timeout errors are transient → allow a retry.
		log.Printf("[fcm] send failed: %v", err)
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return 0, true
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024)) // safe-ignore: body already validated / best-effort read
	// 404 UNREGISTERED / 400 invalid token → prune it so we stop trying.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		log.Printf("[fcm] pruning stale token for %s (HTTP %d)", clientID, resp.StatusCode)
		f.store.Remove(clientID, token)
		return 0, true
	}
	// 429 Too Many Requests / 5xx → transient, retry with backoff.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		log.Printf("[fcm] transient error for %s (HTTP %d), will retry", clientID, resp.StatusCode)
		return parseRetryAfter(resp.Header.Get("Retry-After")), false
	}
	log.Printf("[fcm] send rejected (HTTP %d): %s", resp.StatusCode, string(respBody))
	return 0, true
}

// fcmBackoff returns the exponential-backoff delay for the given attempt index
// (0-based), with full jitter, capped at fcmMaxBackoff.
func fcmBackoff(attempt int) time.Duration {
	d := fcmBaseBackoff << uint(attempt)
	if d > fcmMaxBackoff || d <= 0 {
		d = fcmMaxBackoff
	}
	// Full jitter to avoid a thundering herd of retries.
	return time.Duration(rand.Int63n(int64(d) + 1)) // #nosec G404 -- jitter, not security-sensitive
}

// parseRetryAfter interprets a Retry-After header (delay-seconds form only;
// HTTP-date form is ignored). Returns 0 when absent/unparseable so the caller
// falls back to exponential backoff.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	secs, err := strconv.Atoi(h)
	if err != nil || secs < 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > fcmMaxBackoff {
		d = fcmMaxBackoff
	}
	return d
}

// HasTokens reports whether this client has at least one registered FCM token,
// i.e. push can reach it even with no SSE subscriber. nil-safe.
func (f *FCMSender) HasTokens(clientID string) bool {
	if f == nil || f.store == nil {
		return false
	}
	return len(f.store.Get(clientID)) > 0
}

// NotifyBubbleIncrease pushes (via FCM) for each counter that went up. No-op when
// disabled or the client has no registered tokens.
//
// fav and mentions are the per-thread enrichment computed by the poller: when
// present, favoritos (and optionally avisos) are sent as one grouped
// (replace-in-place) push per thread. Avisos enrichment is currently disabled
// (fetching /notificaciones marks them seen on MV), so mentions is usually empty
// and the count-only fallback fires. Private messages stay count-based.
func (f *FCMSender) NotifyBubbleIncrease(clientID string, prev, current *Bubbles, fav []ThreadActivity, mentions []MentionActivity) {
	if f == nil || prev == nil || current == nil {
		return
	}
	tokens := dedupTokens(f.store.Get(clientID))
	if len(tokens) == 0 {
		return
	}

	pushes := buildActivityPushes(prev, current, fav, mentions)
	if len(pushes) == 0 {
		return
	}

	// Fan the per-(message × token) POSTs out over a small bounded worker pool.
	// FCM HTTP v1 has no multicast endpoint, so this is the closest we get to a
	// batch send while staying friendly to FCM rate limits.
	type job struct {
		token string
		msg   pushMsg
	}
	jobs := make(chan job)
	concurrency := fcmMaxConcurrency
	if total := len(pushes) * len(tokens); total < concurrency {
		concurrency = total
	}

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				f.sendOne(clientID, j.token, j.msg)
			}
		}()
	}

	for _, p := range pushes {
		for _, t := range tokens {
			jobs <- job{token: t, msg: p}
		}
	}
	close(jobs)
	wg.Wait()
}

// buildActivityPushes turns a bubble diff + per-thread enrichment into the set
// of notifications to deliver. Shared by FCM and ntfy so both render the same
// text. Each push carries a grouping tag so repeated pushes about the same
// thread (or category) replace one another in the tray.
func buildActivityPushes(prev, current *Bubbles, fav []ThreadActivity, mentions []MentionActivity) []pushMsg {
	var pushes []pushMsg

	// Avisos (mentions/quotes): per-thread when enriched, else count-only.
	switch {
	case len(mentions) > 0:
		for _, m := range mentions {
			pushes = append(pushes, pushMsg{
				title: "Avisos",
				body:  mentionBody(m),
				tag:   "n" + m.Key,
				data:  map[string]string{"type": "mention", "url": m.URL, "thread": m.Key, "tag": "n" + m.Key},
			})
		}
	case current.Notifications > prev.Notifications:
		pushes = append(pushes, pushMsg{
			title: "Mediavida",
			body:  plural(current.Notifications, "Tienes %d aviso nuevo", "Tienes %d avisos nuevos"),
			tag:   "avisos",
			data:  map[string]string{"type": "mention", "tag": "avisos"},
		})
	}

	// Private messages: always count-based (no cheap per-PM context to scrape).
	if current.Messages > prev.Messages {
		pushes = append(pushes, pushMsg{
			title: "Mensajes privados",
			body:  plural(current.Messages, "Tienes %d mensaje privado nuevo", "Tienes %d mensajes privados nuevos"),
			tag:   "mensajes",
			data:  map[string]string{"type": "pm"},
		})
	}

	// Favoritos: per-thread when enriched, else count-only.
	switch {
	case len(fav) > 0:
		for _, t := range fav {
			pushes = append(pushes, pushMsg{
				title: "Favoritos",
				body:  favBody(t.Count, t.Title),
				tag:   "f" + t.Key,
				data:  map[string]string{"type": "favorite", "url": t.URL, "thread": t.Key, "tag": "f" + t.Key},
			})
		}
	case current.Favorites > prev.Favorites:
		pushes = append(pushes, pushMsg{
			title: "Favoritos",
			body:  plural(current.Favorites, "Actividad nueva en %d hilo favorito", "Actividad nueva en %d hilos favoritos"),
			tag:   "favoritos",
			data:  map[string]string{"type": "favorite", "tag": "favoritos"},
		})
	}

	return pushes
}

// dedupTokens returns the input with exact duplicates removed, preserving order.
// Distinct device tokens for the same client are kept.
func dedupTokens(tokens []string) []string {
	if len(tokens) < 2 {
		return tokens
	}
	seen := make(map[string]struct{}, len(tokens))
	out := tokens[:0]
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
