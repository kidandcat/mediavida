package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

// requireAuthenticated resolves the scraper for the request's client, returning
// nil and writing a 401 if no MV session is active.
func requireAuthenticated(w http.ResponseWriter, r *http.Request, sessions *SessionStore) (*ForumScraper, string) {
	clientID := ClientIDFromContext(r.Context())
	s := sessions.Get(clientID)
	if s == nil || s.Status != "authenticated" || s.Scraper == nil {
		writeError(w, http.StatusUnauthorized, "no active mediavida session — call POST /auth/login")
		return nil, clientID
	}
	return s.Scraper, clientID
}

// errReauthRequired marks an error meaning the Mediavida session is gone and
// could not be renewed automatically (e.g. guard verification now required).
// The app must prompt the user to log in again. Mapped to HTTP 401 by
// writeAPIError so the client can drop to the login screen.
var errReauthRequired = errors.New("session expired, re-authentication required")

// withRelogin runs fn; if it returns ErrSessionExpired it relogs in once and
// retries. If the re-login itself fails the session can no longer be renewed
// without the user, so it returns an error wrapping errReauthRequired (→ 401).
func withRelogin(scraper *ForumScraper, fn func() error) error {
	err := fn()
	if err == nil {
		return nil
	}
	var expired *ErrSessionExpired
	if !errors.As(err, &expired) && scraper.IsLoggedIn() {
		return err
	}
	log.Printf("[api] session invalid for %s, attempting re-login", scraper.Username())
	if rerr := scraper.Relogin(); rerr != nil {
		return fmt.Errorf("%w: %v", errReauthRequired, rerr)
	}
	return fn()
}

// writeAPIError maps a data-endpoint error to an HTTP status: 401 when the
// Mediavida session is gone and must be re-established by the user (so the app
// shows the login screen), 502 otherwise (upstream scrape/parse failure).
func writeAPIError(w http.ResponseWriter, err error) {
	if errors.Is(err, errReauthRequired) {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeError(w, http.StatusBadGateway, err.Error())
}

// --- auth endpoints ---

type loginRequest struct {
	User string `json:"user"`
	Pass string `json:"pass"`
	TOTP string `json:"totp,omitempty"`
}

type loginResponse struct {
	Status   string `json:"status"`             // "authenticated" | "guard_required"
	Username string `json:"username,omitempty"`
	Message  string `json:"message,omitempty"`
}

type statusResponse struct {
	Status   string `json:"status"`             // "authenticated" | "unauthenticated"
	Username string `json:"username,omitempty"`
}

type webAuthResponse struct {
	URL string `json:"url"`
}

// RegisterAPIRoutes wires all REST endpoints under the given mux.
func RegisterAPIRoutes(mux *http.ServeMux, sessions *SessionStore, webhooks *WebhookStore, modForums *ModForumsStore, hub *EventHub, baseURL string) {
	// --- auth ---
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.User == "" || req.Pass == "" {
			writeError(w, http.StatusBadRequest, "user and pass are required")
			return
		}
		clientID := ClientIDFromContext(r.Context())

		err := sessions.CreateFromCredentials(clientID, req.User, req.Pass)
		if err != nil {
			if guardErr, ok := err.(*ErrGuardRequired); ok {
				totp := req.TOTP
				// Auto-generate the code from MV_TOTP_SECRET when the caller
				// didn't supply one, so headless clients never see guard_required.
				if totp == "" {
					if secret := os.Getenv("MV_TOTP_SECRET"); secret != "" {
						if code, gerr := generateTOTP(secret); gerr == nil {
							totp = code
						}
					}
				}
				if totp == "" {
					writeJSON(w, http.StatusOK, loginResponse{
						Status:  "guard_required",
						Message: "guard verification required — call POST /auth/login again with totp set to the 6-digit code from your email or authenticator",
					})
					return
				}
				sess := sessions.Get(clientID)
				if err := sess.Scraper.SubmitGuard(guardErr.GuardURL, totp); err != nil {
					writeError(w, http.StatusUnauthorized, "guard verification failed: "+err.Error())
					return
				}
				sess.Status = "authenticated"
				sess.Scraper.SaveSession()
				log.Printf("[auth] credential login successful for %s (after guard)", req.User)
				writeJSON(w, http.StatusOK, loginResponse{Status: "authenticated", Username: req.User})
				return
			}
			writeError(w, http.StatusUnauthorized, "login failed: "+err.Error())
			return
		}
		log.Printf("[auth] credential login successful for %s", req.User)
		writeJSON(w, http.StatusOK, loginResponse{Status: "authenticated", Username: req.User})
	})

	mux.HandleFunc("GET /auth/status", func(w http.ResponseWriter, r *http.Request) {
		clientID := ClientIDFromContext(r.Context())
		s := sessions.Get(clientID)
		if s == nil || s.Status != "authenticated" || s.Scraper == nil {
			writeJSON(w, http.StatusOK, statusResponse{Status: "unauthenticated"})
			return
		}
		// Validate liveness; relogin if expired.
		if !s.Scraper.ValidateSession() {
			log.Printf("[auth/status] session invalid for %s, attempting re-login", s.Scraper.Username())
			if err := s.Scraper.Relogin(); err != nil {
				writeJSON(w, http.StatusOK, statusResponse{Status: "unauthenticated"})
				return
			}
		}
		writeJSON(w, http.StatusOK, statusResponse{Status: "authenticated", Username: s.Scraper.Username()})
	})

	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		clientID := ClientIDFromContext(r.Context())
		s := sessions.Get(clientID)
		if s != nil && s.Scraper != nil {
			s.Scraper.ClearSession()
		}
		sessions.Set(clientID, nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
	})

	mux.HandleFunc("POST /auth/web", func(w http.ResponseWriter, r *http.Request) {
		clientID := ClientIDFromContext(r.Context())
		flowID := createAuthFlow(clientID)
		writeJSON(w, http.StatusOK, webAuthResponse{
			URL: fmt.Sprintf("%s/auth/login?flow=%s", baseURL, flowID),
		})
	})

	// --- forum browse ---
	mux.HandleFunc("GET /portada", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		var items []PortadaItem
		err := withRelogin(scraper, func() error {
			var e error
			items, e = scraper.FetchPortada()
			return e
		})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})

	mux.HandleFunc("GET /forums", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		var cats []ForumCategory
		err := withRelogin(scraper, func() error {
			var e error
			cats, e = scraper.FetchForumIndex()
			return e
		})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"categories": cats})
	})

	mux.HandleFunc("GET /forums/{slug}/threads", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		slug := r.PathValue("slug")
		if slug == "" {
			writeError(w, http.StatusBadRequest, "slug required")
			return
		}
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				page = n
			}
		}
		var list *ForumThreadList
		err := withRelogin(scraper, func() error {
			var e error
			list, e = scraper.FetchForumThreads(slug, page)
			return e
		})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})

	// --- threads ---
	mux.HandleFunc("GET /threads", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		threadURL := r.URL.Query().Get("url")
		if threadURL == "" {
			writeError(w, http.StatusBadRequest, "url query parameter is required")
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))

		var result *ThreadPage
		err := withRelogin(scraper, func() error {
			res, err := scraper.FetchThread(threadURL, page)
			if err != nil {
				return err
			}
			result = res
			return nil
		})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, threadPageDTO(result))
	})

	mux.HandleFunc("POST /threads/like", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		var req struct {
			PostNum int `json:"post_num"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.PostNum <= 0 {
			writeError(w, http.StatusBadRequest, "post_num must be > 0")
			return
		}
		if err := scraper.LikeMessage(req.PostNum); err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "liked",
			"post_num": req.PostNum,
		})
	})

	mux.HandleFunc("POST /threads/reply", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		var req struct {
			Text       string `json:"text"`
			ReplyToNum int    `json:"reply_to_num,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			writeError(w, http.StatusBadRequest, "text is required")
			return
		}
		if err := scraper.PostReply(req.Text, req.ReplyToNum); err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, map[string]string{"status": "posted"})
	})

	// Raw editable source of a post in the last-read thread (call GET /threads first).
	mux.HandleFunc("GET /threads/source", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		num, _ := strconv.Atoi(r.URL.Query().Get("num"))
		if num <= 0 {
			writeError(w, http.StatusBadRequest, "num query parameter must be > 0")
			return
		}
		src, err := scraper.GetPostSource(num)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"num": num, "source": src})
	})

	// Fetch a single post of the last-read thread by number (for #NNNN refs).
	mux.HandleFunc("GET /threads/quote", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		num, _ := strconv.Atoi(r.URL.Query().Get("num"))
		if num <= 0 {
			writeError(w, http.StatusBadRequest, "num query parameter must be > 0")
			return
		}
		author, bodyHTML, err := scraper.GetQuotedPost(num)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"num": num, "author": author, "body_html": bodyHTML})
	})

	// Edit one of the user's own posts in the last-read thread.
	mux.HandleFunc("POST /threads/edit", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		var req struct {
			PostNum int    `json:"post_num"`
			Text    string `json:"text"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.PostNum <= 0 {
			writeError(w, http.StatusBadRequest, "post_num must be > 0")
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			writeError(w, http.StatusBadRequest, "text is required")
			return
		}
		if err := scraper.EditMessage(req.PostNum, req.Text); err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, map[string]any{"status": "edited", "post_num": req.PostNum})
	})

	mux.HandleFunc("POST /threads", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		var req struct {
			Subforum       string `json:"subforum"`
			Title          string `json:"title"`
			Body           string `json:"body"`
			Tag            int    `json:"tag"`
			AddToFavorites bool   `json:"add_to_favorites,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.Subforum == "" {
			writeError(w, http.StatusBadRequest, "subforum is required")
			return
		}
		if req.Title == "" || req.Body == "" {
			writeError(w, http.StatusBadRequest, "title and body are required (use GET /threads/tags?subforum=... to discover tag IDs)")
			return
		}
		raw, err := scraper.CreateThread(req.Subforum, req.Title, req.Body, req.Tag, req.AddToFavorites)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		// MV's acciones_preview returns JSON; pass through if parseable, otherwise wrap raw text.
		var passthrough any
		if json.Unmarshal([]byte(raw), &passthrough) == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"status":   "submitted",
				"response": passthrough,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":       "submitted",
			"raw_response": raw,
		})
	})

	mux.HandleFunc("GET /threads/tags", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		subforum := r.URL.Query().Get("subforum")
		if subforum == "" {
			writeError(w, http.StatusBadRequest, "subforum query parameter is required")
			return
		}
		page, err := scraper.FetchNewThreadPage(subforum)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, map[string]any{
			"subforum": subforum,
			"fid":      page.FID,
			"tags":     page.Tags,
		})
	})

	// --- search ---
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		query := r.URL.Query().Get("q")
		if query == "" {
			writeError(w, http.StatusBadRequest, "q query parameter is required")
			return
		}
		results, err := scraper.Search(query)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"query":   query,
			"results": searchResultsDTO(results),
		})
	})

	// --- inbox / messages ---
	mux.HandleFunc("GET /inbox", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		items, err := scraper.FetchInbox(page)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, map[string]any{
			"page":          page,
			"conversations": inboxItemsDTO(items),
		})
	})

	mux.HandleFunc("GET /messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "id is required")
			return
		}
		conv, err := scraper.FetchConversation(id)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, conversationDTO(conv))
	})

	mux.HandleFunc("POST /messages/{id}/reply", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "id is required")
			return
		}
		var req struct {
			Text string `json:"text"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			writeError(w, http.StatusBadRequest, "text is required")
			return
		}
		err := withRelogin(scraper, func() error { return scraper.SendPrivateMessage(id, req.Text) })
		if err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
	})

	// --- favorites / user content ---
	mux.HandleFunc("GET /favorites", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		items, err := scraper.FetchFavorites()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, map[string]any{"favorites": threadListItemsDTO(items)})
	})

	mux.HandleFunc("GET /users/{username}/posts", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		username := r.PathValue("username")
		if username == "" {
			username = scraper.Username()
		}
		items, err := scraper.FetchUserPosts(username)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, map[string]any{
			"username": username,
			"threads":  threadListItemsDTO(items),
		})
	})

	mux.HandleFunc("GET /users/{username}/mentions", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		username := r.PathValue("username")
		if username == "" {
			username = scraper.Username()
		}
		items, err := scraper.FetchMentions(username)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		scraper.AutoSave()
		writeJSON(w, http.StatusOK, map[string]any{
			"username": username,
			"mentions": mentionsDTO(items),
		})
	})

	// --- bubbles / events / webhook ---
	mux.HandleFunc("GET /bubbles", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		var b *Bubbles
		err := withRelogin(scraper, func() error {
			res, err := scraper.FetchBubbles()
			if err != nil {
				return err
			}
			b = res
			return nil
		})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{
			"messages":      b.Messages,
			"notifications": b.Notifications,
			"favorites":     b.Favorites,
		})
	})

	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		clientID := ClientIDFromContext(r.Context())
		s := sessions.Get(clientID)
		if s == nil || s.Status != "authenticated" {
			writeError(w, http.StatusUnauthorized, "no active mediavida session")
			return
		}
		hub.ServeSSE(w, r, clientID)
	})

	mux.HandleFunc("GET /webhook", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		url := webhooks.Get(scraper.Username())
		writeJSON(w, http.StatusOK, map[string]string{
			"username": scraper.Username(),
			"url":      url,
		})
	})

	mux.HandleFunc("PUT /webhook", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.URL == "" {
			writeError(w, http.StatusBadRequest, "url is required (use DELETE /webhook to remove)")
			return
		}
		webhooks.Set(scraper.Username(), req.URL)
		writeJSON(w, http.StatusOK, map[string]string{
			"username": scraper.Username(),
			"url":      req.URL,
		})
	})

	mux.HandleFunc("DELETE /webhook", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		webhooks.Remove(scraper.Username())
		w.WriteHeader(http.StatusNoContent)
	})

	// --- mod forums (per-user subforum subscriptions for mod alerts) ---
	mux.HandleFunc("GET /mod/forums", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"username": scraper.Username(),
			"forums":   modForums.Get(scraper.Username()),
		})
	})

	mux.HandleFunc("POST /mod/forums", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		var req struct {
			Subforum string `json:"subforum"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		slug := strings.TrimSpace(req.Subforum)
		if slug == "" {
			writeError(w, http.StatusBadRequest, "subforum is required")
			return
		}
		// Verify the user is a mod of this subforum before saving.
		var mb *ModBubbles
		err := withRelogin(scraper, func() error {
			res, err := scraper.FetchModBubbles(slug)
			if err != nil {
				return err
			}
			mb = res
			return nil
		})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		if mb == nil {
			writeError(w, http.StatusForbidden, fmt.Sprintf("user %s is not a moderator of /foro/%s", scraper.Username(), slug))
			return
		}
		added := modForums.Add(scraper.Username(), slug)
		writeJSON(w, http.StatusOK, map[string]any{
			"username": scraper.Username(),
			"forums":   modForums.Get(scraper.Username()),
			"added":    added,
		})
	})

	mux.HandleFunc("DELETE /mod/forums/{subforum}", func(w http.ResponseWriter, r *http.Request) {
		scraper, _ := requireAuthenticated(w, r, sessions)
		if scraper == nil {
			return
		}
		slug := r.PathValue("subforum")
		if slug == "" {
			writeError(w, http.StatusBadRequest, "subforum is required")
			return
		}
		modForums.Remove(scraper.Username(), slug)
		w.WriteHeader(http.StatusNoContent)
	})

	// --- meta ---
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// --- DTOs ---

type messageDTO struct {
	Num      int    `json:"num"`
	Author   string `json:"author"`
	Body     string `json:"body"`
	BodyHTML string `json:"body_html,omitempty"`
	Avatar   string `json:"avatar,omitempty"`
	Date     string `json:"date,omitempty"`
	Time     int64  `json:"time,omitempty"`
	Liked    bool   `json:"liked"`
}

type threadPageDTOType struct {
	CurrentPage int          `json:"current_page"`
	TotalPages  int          `json:"total_pages"`
	Messages    []messageDTO `json:"messages"`
}

func threadPageDTO(p *ThreadPage) threadPageDTOType {
	out := threadPageDTOType{
		CurrentPage: p.CurrentPage,
		TotalPages:  p.TotalPages,
		Messages:    make([]messageDTO, 0, len(p.Messages)),
	}
	for _, m := range p.Messages {
		out.Messages = append(out.Messages, messageDTO{
			Num: m.Num, Author: m.Author, Body: m.Body, BodyHTML: m.BodyHTML,
			Avatar: m.Avatar, Date: m.Date, Time: m.Time, Liked: m.Liked,
		})
	}
	return out
}

type searchResultDTO struct {
	Title  string `json:"title"`
	URL    string `json:"url"`
	Forum  string `json:"forum,omitempty"`
	Author string `json:"author,omitempty"`
	Date   string `json:"date,omitempty"`
}

func searchResultsDTO(items []SearchResult) []searchResultDTO {
	out := make([]searchResultDTO, 0, len(items))
	for _, r := range items {
		out = append(out, searchResultDTO{
			Title: r.Title, URL: r.URL, Forum: r.Forum, Author: r.Author, Date: r.Date,
		})
	}
	return out
}

type inboxItemDTO struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	Date    string `json:"date,omitempty"`
	Preview string `json:"preview,omitempty"`
	Unread  bool   `json:"unread"`
}

func inboxItemsDTO(items []InboxItem) []inboxItemDTO {
	out := make([]inboxItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, inboxItemDTO{
			ID: it.ID, Author: it.Author, Date: it.Date, Preview: it.Short, Unread: it.Unread,
		})
	}
	return out
}

type privateMessageDTO struct {
	Author string `json:"author"`
	Date   string `json:"date,omitempty"`
	Body   string `json:"body"`
}

type conversationDTOType struct {
	Title    string              `json:"title,omitempty"`
	Messages []privateMessageDTO `json:"messages"`
}

func conversationDTO(c *Conversation) conversationDTOType {
	out := conversationDTOType{Title: c.Title, Messages: make([]privateMessageDTO, 0, len(c.Messages))}
	for _, m := range c.Messages {
		out.Messages = append(out.Messages, privateMessageDTO{
			Author: m.Author, Date: m.Date, Body: m.Body,
		})
	}
	return out
}

type threadListItemDTO struct {
	Title        string `json:"title"`
	URL          string `json:"url"`
	Forum        string `json:"forum,omitempty"`
	Replies      string `json:"replies,omitempty"`
	LastActivity string `json:"last_activity,omitempty"`
	UnreadCount  string `json:"unread_count,omitempty"`
	UnreadURL    string `json:"unread_url,omitempty"`
}

func threadListItemsDTO(items []ThreadListItem) []threadListItemDTO {
	out := make([]threadListItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, threadListItemDTO{
			Title: it.Title, URL: it.URL, Forum: it.Forum,
			Replies: it.Replies, LastActivity: it.LastActivity,
			UnreadCount: it.UnreadCount, UnreadURL: it.UnreadURL,
		})
	}
	return out
}

type mentionDTO struct {
	ThreadTitle string `json:"thread_title"`
	ThreadURL   string `json:"thread_url"`
	Author      string `json:"author"`
	PostNum     int    `json:"post_num"`
	Body        string `json:"body"`
	Date        string `json:"date,omitempty"`
}

func mentionsDTO(items []MentionItem) []mentionDTO {
	out := make([]mentionDTO, 0, len(items))
	for _, it := range items {
		out = append(out, mentionDTO{
			ThreadTitle: it.ThreadTitle, ThreadURL: it.ThreadURL,
			Author: it.Author, PostNum: it.PostNum, Body: it.Body, Date: it.Date,
		})
	}
	return out
}
