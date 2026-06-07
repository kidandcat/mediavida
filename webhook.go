package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// WebhookStore manages one webhook URL per MV username, persisted to disk.
type WebhookStore struct {
	mu       sync.RWMutex
	webhooks map[string]string // mv_username -> webhook_url
}

func NewWebhookStore() *WebhookStore {
	ws := &WebhookStore{webhooks: make(map[string]string)}
	ws.load()
	return ws
}

func webhookFile() string {
	dir, _ := os.UserConfigDir() // safe-ignore: falls back to relative path
	return filepath.Join(dir, "mediavida-mcp", "webhooks.json")
}

func (ws *WebhookStore) save() {
	ws.mu.RLock()
	data, _ := json.Marshal(ws.webhooks) // safe-ignore: marshaling a static struct never fails
	ws.mu.RUnlock()

	path := webhookFile()
	_ = os.MkdirAll(filepath.Dir(path), 0700) // safe-ignore: best-effort dir create; later write reports real errors
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("[webhook] failed to save: %v", err)
	}
}

func (ws *WebhookStore) load() {
	data, err := os.ReadFile(webhookFile())
	if err != nil {
		return
	}
	var m map[string]string
	if uerr := json.Unmarshal(data, &m); uerr != nil {
		return
	}
	ws.webhooks = m
	log.Printf("[webhook] restored %d webhooks from disk", len(m))
}

// Set configures the webhook URL for a user. Replaces any existing one.
func (ws *WebhookStore) Set(username, url string) {
	ws.mu.Lock()
	ws.webhooks[username] = url
	ws.mu.Unlock()
	go ws.save() // goroutine-ok: fire-and-forget async save
}

// Remove deletes the webhook for a user.
func (ws *WebhookStore) Remove(username string) {
	ws.mu.Lock()
	delete(ws.webhooks, username)
	ws.mu.Unlock()
	go ws.save() // goroutine-ok: fire-and-forget async save
}

// Get returns the webhook URL for a user, or empty string if none.
func (ws *WebhookStore) Get(username string) string {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return ws.webhooks[username]
}

// Has reports whether a webhook is configured for the user (used by the poller
// to decide whether anyone is listening). nil-safe.
func (ws *WebhookStore) Has(username string) bool {
	if ws == nil {
		return false
	}
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return ws.webhooks[username] != ""
}

// WebhookPayload is the JSON body sent to the webhook URL.
type WebhookPayload struct {
	Username      string `json:"username"`
	Messages      int    `json:"messages"`
	Notifications int    `json:"notifications"`
	Favorites     int    `json:"favorites"`
}

// Send fires the webhook for a user if one is configured.
func (ws *WebhookStore) Send(username string, bubbles *Bubbles) {
	url := ws.Get(username)
	if url == "" {
		return
	}

	payload := WebhookPayload{
		Username:      username,
		Messages:      bubbles.Messages,
		Notifications: bubbles.Notifications,
		Favorites:     bubbles.Favorites,
	}
	data, _ := json.Marshal(payload) // safe-ignore: marshaling a static struct never fails

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("[webhook] POST to %s failed: %v", url, err)
		return
	}
	_ = resp.Body.Close() // safe-ignore: best-effort cleanup

	if resp.StatusCode >= 400 {
		log.Printf("[webhook] POST to %s returned %d", url, resp.StatusCode)
	} else {
		log.Printf("[webhook] sent to %s for %s (bm=%d bn=%d bf=%d)",
			url, username, bubbles.Messages, bubbles.Notifications, bubbles.Favorites)
	}
}

// FormatStatus returns a human-readable status of the webhook for a user.
func (ws *WebhookStore) FormatStatus(username string) string {
	url := ws.Get(username)
	if url == "" {
		return fmt.Sprintf("No webhook configured for %s", username)
	}
	return fmt.Sprintf("Webhook for %s: %s", username, url)
}
