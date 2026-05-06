package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// EventHub broadcasts bubble change events to SSE subscribers, keyed by clientID.
// Each client may have multiple concurrent SSE connections (e.g. several tabs).
type EventHub struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan []byte]struct{} // clientID → set of buffered channels
}

func NewEventHub() *EventHub {
	return &EventHub{subscribers: make(map[string]map[chan []byte]struct{})}
}

// subscribe registers a new channel for a client and returns it together with the unsubscribe func.
func (h *EventHub) subscribe(clientID string) (chan []byte, func()) {
	ch := make(chan []byte, 16)
	h.mu.Lock()
	if h.subscribers[clientID] == nil {
		h.subscribers[clientID] = make(map[chan []byte]struct{})
	}
	h.subscribers[clientID][ch] = struct{}{}
	h.mu.Unlock()

	unsub := func() {
		h.mu.Lock()
		if subs, ok := h.subscribers[clientID]; ok {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(h.subscribers, clientID)
			}
		}
		h.mu.Unlock()
		close(ch)
	}
	return ch, unsub
}

// Publish sends a serialized event to every subscriber of clientID. Slow consumers
// are dropped silently so a stuck client never blocks the poller.
func (h *EventHub) Publish(clientID, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[events] marshal failed for client %s: %v", clientID, err)
		return
	}
	frame := fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event, data)

	h.mu.RLock()
	subs := h.subscribers[clientID]
	dest := make([]chan []byte, 0, len(subs))
	for ch := range subs {
		dest = append(dest, ch)
	}
	h.mu.RUnlock()

	for _, ch := range dest {
		select {
		case ch <- frame:
		default:
			// subscriber buffer full — skip this event for them
		}
	}
}

// HasSubscribers reports whether at least one client is connected. The bubbles
// poller can use this to skip work when nobody is listening (optional).
func (h *EventHub) HasSubscribers(clientID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers[clientID]) > 0
}

// ServeSSE upgrades the response to a Server-Sent Events stream and forwards
// every published event for clientID until the client disconnects.
func (h *EventHub) ServeSSE(w http.ResponseWriter, r *http.Request, clientID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Initial comment to flush headers immediately.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ch, unsub := h.subscribe(clientID)
	defer unsub()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case frame, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
