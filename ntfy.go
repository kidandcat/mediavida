package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// NtfyPublisher pushes notifications to a self-hosted ntfy server. Topics are
// derived from each device's bearer token (see ntfyTopic) so they are secret
// and per-device. nil when NTFY_URL/NTFY_TOKEN are not configured.
type NtfyPublisher struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewNtfyPublisher builds a publisher from NTFY_URL / NTFY_TOKEN. Returns nil
// (push disabled) when either is missing.
func NewNtfyPublisher() *NtfyPublisher {
	url, token := os.Getenv("NTFY_URL"), os.Getenv("NTFY_TOKEN")
	if url == "" || token == "" {
		log.Printf("[ntfy] disabled (NTFY_URL or NTFY_TOKEN not set)")
		return nil
	}
	log.Printf("[ntfy] enabled at %s", url)
	return &NtfyPublisher{
		baseURL: url,
		token:   token,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// ntfyTopic maps a client/device token to its (secret) ntfy topic. The app
// computes the same value from its own device token to subscribe. We hash the
// token so the bearer itself is never exposed as a topic in URLs/logs.
func ntfyTopic(clientID string) string {
	sum := sha256.Sum256([]byte(clientID))
	return "mv_" + hex.EncodeToString(sum[:])[:32]
}

type ntfyMessage struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message"`
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
}

// publish POSTs a single message to ntfy. Best-effort: logs and returns on error.
func (n *NtfyPublisher) publish(msg ntfyMessage) {
	if n == nil {
		return
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	req, err := http.NewRequest("POST", n.baseURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+n.token)
	resp, err := n.client.Do(req)
	if err != nil {
		log.Printf("[ntfy] publish failed (topic %s): %v", msg.Topic, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Printf("[ntfy] publish rejected (topic %s): HTTP %d", msg.Topic, resp.StatusCode)
	}
}

// NotifyBubbleIncrease sends a push for each counter that went up for a device.
// Counters going down (the user read them) never notify. No-op when disabled.
//
// fav/mentions are the per-thread enrichment from the poller; ntfy reuses the
// same body text as FCM (via buildActivityPushes) so both channels read alike.
// ntfy has no collapse-key, so per-thread messages stack rather than replace —
// acceptable for the secondary channel.
func (n *NtfyPublisher) NotifyBubbleIncrease(clientID string, prev, current *Bubbles, fav []ThreadActivity, mentions []MentionActivity) {
	if n == nil || prev == nil || current == nil {
		return
	}
	topic := ntfyTopic(clientID)

	for _, p := range buildActivityPushes(prev, current, fav, mentions) {
		msg := ntfyMessage{
			Topic:    topic,
			Title:    p.title,
			Message:  p.body,
			Priority: ntfyPriority(p.data["type"]),
			Tags:     []string{ntfyTag(p.data["type"])},
		}
		if url := p.data["url"]; url != "" {
			msg.Click = url
		}
		n.publish(msg)
	}
}

// ntfyPriority maps a push category to an ntfy priority.
func ntfyPriority(kind string) int {
	if kind == "favorite" {
		return 3
	}
	return 4
}

// ntfyTag maps a push category to an ntfy icon tag.
func ntfyTag(kind string) string {
	switch kind {
	case "favorite":
		return "star"
	case "pm":
		return "envelope"
	default:
		return "bell"
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf(one, n)
	}
	return fmt.Sprintf(many, n)
}
