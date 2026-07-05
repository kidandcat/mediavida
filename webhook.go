package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// WebhookStore manages one webhook URL per MV username, backed by the durable SQLite store.
// There is no in-memory map or JSON-on-disk persistence: every read/write goes
// straight through the durable store.
type WebhookStore struct {
	cs *Store
}

// NewWebhookStore wires the store to the durable SQLite store, the only persistence layer.
func NewWebhookStore(cs *Store) *WebhookStore {
	return &WebhookStore{cs: cs}
}

// Set configures the webhook URL for a user. Replaces any existing one.
func (ws *WebhookStore) Set(username, url string) {
	if err := ws.cs.SetWebhook(username, url); err != nil {
		log.Printf("[webhook] failed to set webhook for %s: %v", username, err)
	}
}

// Remove deletes the webhook for a user.
func (ws *WebhookStore) Remove(username string) {
	if err := ws.cs.RemoveWebhook(username); err != nil {
		log.Printf("[webhook] failed to remove webhook for %s: %v", username, err)
	}
}

// Get returns the webhook URL for a user, or empty string if none.
//
// The Store exposes no direct GetWebhook lookup, so this reads the full
// webhook map via AllWebhooks and indexes it by username.
func (ws *WebhookStore) Get(username string) string {
	all, err := ws.cs.AllWebhooks()
	if err != nil {
		log.Printf("[webhook] failed to read webhooks: %v", err)
		return ""
	}
	return all[username]
}

// Has reports whether a webhook is configured for the user (used by the poller
// to decide whether anyone is listening). nil-safe.
func (ws *WebhookStore) Has(username string) bool {
	if ws == nil {
		return false
	}
	return ws.Get(username) != ""
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
