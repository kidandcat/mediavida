package main

import (
	"log"
	"net/http"
	"strings"
)

// WatchTokenStore is the durable registry of paired-watch tokens, backed by
// Colmena. Each token aliases its owner's MV session (see SessionStore.
// CreateWatchAlias) so a watch can fetch /bubbles autonomously after pairing.
type WatchTokenStore struct {
	cs *ColmenaStore
}

// NewWatchTokenStore builds a watch-token store backed by the Colmena cluster.
func NewWatchTokenStore(cs *ColmenaStore) *WatchTokenStore {
	return &WatchTokenStore{cs: cs}
}

// Add registers (or relabels) a paired-watch token for an owner.
func (s *WatchTokenStore) Add(token, owner, label string) {
	if err := s.cs.AddWatchToken(token, owner, label); err != nil {
		log.Printf("[watch] failed to add token for %s: %v", owner, err)
	}
}

// List returns an owner's paired watches (empty on error).
func (s *WatchTokenStore) List(owner string) []WatchTokenRecord {
	recs, err := s.cs.ListWatchTokens(owner)
	if err != nil {
		log.Printf("[watch] failed to list tokens for %s: %v", owner, err)
		return nil
	}
	return recs
}

// Owner resolves a watch token to its owner client ID.
func (s *WatchTokenStore) Owner(token string) (string, bool) {
	owner, ok, err := s.cs.GetWatchTokenOwner(token)
	if err != nil {
		log.Printf("[watch] failed to read owner for token: %v", err)
		return "", false
	}
	return owner, ok
}

// Delete revokes a paired-watch token.
func (s *WatchTokenStore) Delete(token string) {
	if err := s.cs.DeleteWatchToken(token); err != nil {
		log.Printf("[watch] failed to delete token: %v", err)
	}
}

type watchPairRequest struct {
	Label string `json:"label,omitempty"`
}

type watchPairResponse struct {
	Token   string `json:"token"`
	BaseURL string `json:"base_url"`
}

type watchTokenInfo struct {
	Token     string `json:"token"`
	Label     string `json:"label"`
	CreatedAt int64  `json:"created_at"`
}

// RegisterWatchRoutes wires the watch-pairing endpoints. Mounted on the
// bearer-protected API mux: the phone app pairs a watch with its own device
// token, minting a separate watch token that aliases the same MV session. The
// watch then talks to /bubbles directly and autonomously — the phone app is only
// needed at pairing time to hand over the token (over a localhost handshake).
func RegisterWatchRoutes(mux *http.ServeMux, sessions *SessionStore, watch *WatchTokenStore, baseURL string) {
	// POST /watch/pair — mint a watch token aliasing the caller's session.
	mux.HandleFunc("POST /watch/pair", func(w http.ResponseWriter, r *http.Request) {
		scraper, owner := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		var req watchPairRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		token, err := sessions.CreateWatchAlias(owner)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		label := strings.TrimSpace(req.Label)
		watch.Add(token, owner, label)
		log.Printf("[watch] paired new watch for %s (label %q)", owner, label)
		writeJSON(w, http.StatusOK, watchPairResponse{Token: token, BaseURL: baseURL})
	})

	// GET /watch/tokens — list the caller's paired watches so the phone app can
	// show/manage them.
	mux.HandleFunc("GET /watch/tokens", func(w http.ResponseWriter, r *http.Request) {
		scraper, owner := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		recs := watch.List(owner)
		out := make([]watchTokenInfo, 0, len(recs))
		for _, rec := range recs {
			out = append(out, watchTokenInfo{Token: rec.Token, Label: rec.Label, CreatedAt: rec.CreatedAt})
		}
		writeJSON(w, http.StatusOK, map[string]any{"watches": out})
	})

	// DELETE /watch/pair/{token} — revoke a paired watch (owner-scoped): drop the
	// registry row and the aliased MV session.
	mux.HandleFunc("DELETE /watch/pair/{token}", func(w http.ResponseWriter, r *http.Request) {
		scraper, owner := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		token := r.PathValue("token")
		tokenOwner, ok := watch.Owner(token)
		if !ok || tokenOwner != owner {
			writeError(w, http.StatusNotFound, "watch not found")
			return
		}
		watch.Delete(token)
		if err := sessions.cs.DeleteSession(token); err != nil {
			log.Printf("[watch] failed to delete aliased session %s: %v", token, err)
		}
		sessions.mu.Lock()
		delete(sessions.sessions, token)
		sessions.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	})
}
