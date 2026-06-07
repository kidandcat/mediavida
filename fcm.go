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
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// FCMTokenStore maps a client (device bearer token) to its FCM registration
// tokens, persisted to disk. A client can have more than one token (reinstalls,
// token refresh) until the stale ones are pruned on send failure.
type FCMTokenStore struct {
	mu     sync.RWMutex
	tokens map[string][]string // clientID -> []fcmToken
}

func NewFCMTokenStore() *FCMTokenStore {
	s := &FCMTokenStore{tokens: make(map[string][]string)}
	s.load()
	return s
}

func fcmTokenFile() string {
	dir, _ := os.UserConfigDir() // safe-ignore: falls back to relative path
	return filepath.Join(dir, "mediavida-mcp", "push_tokens.json")
}

func (s *FCMTokenStore) save() {
	s.mu.RLock()
	data, _ := json.Marshal(s.tokens) // safe-ignore: marshaling a static struct never fails
	s.mu.RUnlock()
	path := fcmTokenFile()
	_ = os.MkdirAll(filepath.Dir(path), 0700) // safe-ignore: best-effort dir create; later write reports real errors
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("[fcm] failed to save tokens: %v", err)
	}
}

func (s *FCMTokenStore) load() {
	data, err := os.ReadFile(fcmTokenFile())
	if err != nil {
		return
	}
	var m map[string][]string
	if uerr := json.Unmarshal(data, &m); uerr != nil {
		return
	}
	s.tokens = m
	log.Printf("[fcm] restored push tokens for %d client(s)", len(m))
}

// Add registers an FCM token for a client (deduped).
func (s *FCMTokenStore) Add(clientID, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tokens[clientID] {
		if t == token {
			return
		}
	}
	s.tokens[clientID] = append(s.tokens[clientID], token)
	go s.save() // goroutine-ok: fire-and-forget async save
}

// Remove deletes a specific token from a client (e.g. when FCM reports it stale).
func (s *FCMTokenStore) Remove(clientID, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.tokens[clientID][:0]
	for _, t := range s.tokens[clientID] {
		if t != token {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(s.tokens, clientID)
	} else {
		s.tokens[clientID] = kept
	}
	go s.save() // goroutine-ok: fire-and-forget async save
}

// Get returns a copy of the client's tokens.
func (s *FCMTokenStore) Get(clientID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.tokens[clientID]...)
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
type fcmV1Message struct {
	Message struct {
		Token        string `json:"token"`
		Notification struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"notification"`
		Android struct {
			Priority string `json:"priority"`
		} `json:"android"`
		APNS struct {
			Headers map[string]string `json:"headers"`
		} `json:"apns"`
	} `json:"message"`
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
func (f *FCMSender) sendOne(clientID, token, title, body string) {
	var m fcmV1Message
	m.Message.Token = token
	m.Message.Notification.Title = title
	m.Message.Notification.Body = body
	m.Message.Android.Priority = "high"
	m.Message.APNS.Headers = map[string]string{"apns-priority": "10"}

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
func (f *FCMSender) NotifyBubbleIncrease(clientID string, prev, current *Bubbles) {
	if f == nil || prev == nil || current == nil {
		return
	}
	tokens := dedupTokens(f.store.Get(clientID))
	if len(tokens) == 0 {
		return
	}

	type push struct{ title, body string }
	var pushes []push
	if current.Notifications > prev.Notifications {
		pushes = append(pushes, push{"Mediavida",
			plural(current.Notifications, "Tienes %d aviso nuevo", "Tienes %d avisos nuevos")})
	}
	if current.Messages > prev.Messages {
		pushes = append(pushes, push{"Mensajes privados",
			plural(current.Messages, "Tienes %d mensaje privado nuevo", "Tienes %d mensajes privados nuevos")})
	}
	if current.Favorites > prev.Favorites {
		pushes = append(pushes, push{"Favoritos",
			plural(current.Favorites, "Actividad nueva en %d hilo favorito", "Actividad nueva en %d hilos favoritos")})
	}

	if len(pushes) == 0 {
		return
	}

	// Fan the per-(message × token) POSTs out over a small bounded worker pool.
	// FCM HTTP v1 has no multicast endpoint, so this is the closest we get to a
	// batch send while staying friendly to FCM rate limits.
	type job struct {
		token, title, body string
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
				f.sendOne(clientID, j.token, j.title, j.body)
			}
		}()
	}

	for _, p := range pushes {
		for _, t := range tokens {
			jobs <- job{token: t, title: p.title, body: p.body}
		}
	}
	close(jobs)
	wg.Wait()
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
