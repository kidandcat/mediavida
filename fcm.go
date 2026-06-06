package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "mediavida-mcp", "push_tokens.json")
}

func (s *FCMTokenStore) save() {
	s.mu.RLock()
	data, _ := json.Marshal(s.tokens)
	s.mu.RUnlock()
	path := fcmTokenFile()
	os.MkdirAll(filepath.Dir(path), 0700)
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
	if err := json.Unmarshal(data, &m); err != nil {
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
	go s.save()
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
	go s.save()
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

func (f *FCMSender) sendOne(clientID, token, title, body string) {
	var m fcmV1Message
	m.Message.Token = token
	m.Message.Notification.Title = title
	m.Message.Notification.Body = body
	m.Message.Android.Priority = "high"
	m.Message.APNS.Headers = map[string]string{"apns-priority": "10"}

	payload, _ := json.Marshal(m)
	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", f.projectID)
	req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	tok, err := f.tokens.Token()
	if err != nil {
		log.Printf("[fcm] oauth token error: %v", err)
		return
	}
	tok.SetAuthHeader(req)

	resp, err := f.client.Do(req)
	if err != nil {
		log.Printf("[fcm] send failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	// 404 UNREGISTERED / 400 invalid token → prune it so we stop trying.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		log.Printf("[fcm] pruning stale token for %s (HTTP %d)", clientID, resp.StatusCode)
		f.store.Remove(clientID, token)
		return
	}
	log.Printf("[fcm] send rejected (HTTP %d): %s", resp.StatusCode, string(respBody))
}

// NotifyBubbleIncrease pushes (via FCM) for each counter that went up. No-op when
// disabled or the client has no registered tokens.
func (f *FCMSender) NotifyBubbleIncrease(clientID string, prev, current *Bubbles) {
	if f == nil || prev == nil || current == nil {
		return
	}
	tokens := f.store.Get(clientID)
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

	for _, p := range pushes {
		for _, t := range tokens {
			f.sendOne(clientID, t, p.title, p.body)
		}
	}
}
