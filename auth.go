package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type contextKey string

const clientIDKey contextKey = "client_id"

func ClientIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(clientIDKey).(string); ok {
		return v
	}
	return ""
}

// --- Auth flow management ---

type authFlow struct {
	ClientID  string
	CreatedAt time.Time
}

var (
	authFlows   sync.Map  // global-ok: process-wide in-memory auth-flow registry
	flowCleanup sync.Once // global-ok: process-wide once guard for the expiry sweeper
)

func generateFlowID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // safe-ignore: crypto/rand.Read never returns an error on supported platforms
	return hex.EncodeToString(b)
}

func createAuthFlow(clientID string) string {
	flowCleanup.Do(func() {
		go func() { // goroutine-ok: long-lived background expiry sweeper, lives for the process lifetime
			for {
				time.Sleep(10 * time.Minute)
				now := time.Now()
				authFlows.Range(func(key, value any) bool { // any-ok: sync.Map.Range signature requires any
					if flow, ok := value.(*authFlow); ok {
						if now.Sub(flow.CreatedAt) > 30*time.Minute {
							authFlows.Delete(key)
						}
					}
					return true
				})
			}
		}()
	})
	id := generateFlowID()
	authFlows.Store(id, &authFlow{ClientID: clientID, CreatedAt: time.Now()})
	return id
}

func getAuthFlow(id string) (string, bool) {
	v, ok := authFlows.Load(id)
	if !ok {
		return "", false
	}
	flow := v.(*authFlow) // safe-ignore: authFlows only ever stores *authFlow values
	if time.Since(flow.CreatedAt) > 30*time.Minute {
		authFlows.Delete(id)
		return "", false
	}
	return flow.ClientID, true
}

func deleteAuthFlow(id string) {
	authFlows.Delete(id)
}

// --- HTML templates ---

// global-ok: html template compiled once at startup
var loginTpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html><head><title>Mediavida — Login</title>
<style>
body{font-family:system-ui;max-width:400px;margin:80px auto;padding:0 20px;background:#1a1a2e;color:#e0e0e0}
h2{color:#fff}
p{color:#ccc;line-height:1.5}
input{display:block;width:100%;padding:10px;margin:8px 0 16px;box-sizing:border-box;border:1px solid #333;border-radius:4px;background:#16213e;color:#e0e0e0}
button{padding:12px 20px;background:#0f3460;color:white;border:none;cursor:pointer;width:100%;border-radius:4px;font-size:16px}
button:hover{background:#1a4a8a}
.error{color:#e74c3c;margin-top:16px}
.success{color:#2ecc71;margin-top:16px}
</style></head>
<body>
<h2>Mediavida</h2>
<p>Introduce tus credenciales de Mediavida para iniciar sesión.</p>
<form method="POST" action="/auth/login">
  <input type="hidden" name="flow_id" value="{{.FlowID}}">
  <label>Usuario<input name="user" required autofocus></label>
  <label>Contraseña<input name="pass" type="password" required></label>
  <button type="submit">Conectar</button>
</form>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
{{if .Success}}<p class="success">{{.Success}}</p>{{end}}
</body></html>`))

// global-ok: html template compiled once at startup
var guardTpl = template.Must(template.New("guard").Parse(`<!DOCTYPE html>
<html><head><title>Mediavida — Verificación</title>
<style>
body{font-family:system-ui;max-width:400px;margin:80px auto;padding:0 20px;background:#1a1a2e;color:#e0e0e0}
h2{color:#fff}
p{color:#ccc;line-height:1.5}
input{display:block;width:100%;padding:10px;margin:8px 0 16px;box-sizing:border-box;border:1px solid #333;border-radius:4px;background:#16213e;color:#e0e0e0}
button{padding:12px 20px;background:#0f3460;color:white;border:none;cursor:pointer;width:100%;border-radius:4px;font-size:16px}
button:hover{background:#1a4a8a}
.error{color:#e74c3c}
</style></head>
<body>
<h2>Verificación por Email</h2>
<p>Revisa tu correo y escribe el código PIN.</p>
<form method="POST" action="/auth/guard">
  <input type="hidden" name="flow_id" value="{{.FlowID}}">
  <label>Código PIN<input name="code" required autofocus></label>
  <button type="submit">Verificar</button>
</form>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
</body></html>`))

// --- API Token Middleware ---

// APITokenMiddleware validates the Bearer token against the allowed API tokens.
// Sets the client ID in the request context.
//
// Watch tokens (minted by /watch/pair) have NO session of their own: they alias
// the owner's session. Duplicating the session per watch used to spawn a second
// concurrent Mediavida login per account and triggered guard/login storms, so a
// watch token instead resolves to its owner's client ID here — one MV session
// per account, shared by the phone and the watch.
func APITokenMiddleware(validTokens map[string]bool, sessions *SessionStore, watchTokens *WatchTokenStore, appAliases *AppAliasStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized: Bearer token required", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		clientID := token
		// Valid if it's a configured API token (legacy/tooling) or it has a live
		// session created via /auth/app-login (per-device app login).
		if !validTokens[token] {
			// A transient session-store failure must NOT be reported as an
			// invalid token (which logs the app out). This middleware gates EVERY
			// request, so a momentary Colmena blip here would otherwise sign every
			// device out at once. Map it to a retryable 503 instead.
			sess, err := lookupSession(sessions, token)
			if errors.Is(err, errSessionStoreUnavailable) {
				http.Error(w, "session store temporarily unavailable; retry", http.StatusServiceUnavailable)
				return
			}
			if sess == nil {
				// Otherwise it may be an alias — a watch token or a phone device
				// token — that reuses its owner's single MV session. Resolve it to
				// the owner clientID so there is only ever one MV login per account.
				owner, ok := "", false
				if appAliases != nil {
					owner, ok = appAliases.Owner(token)
				}
				if !ok && watchTokens != nil {
					owner, ok = watchTokens.Owner(token)
				}
				ownerSess, oerr := lookupSession(sessions, owner)
				if errors.Is(oerr, errSessionStoreUnavailable) {
					http.Error(w, "session store temporarily unavailable; retry", http.StatusServiceUnavailable)
					return
				}
				if !ok || ownerSess == nil {
					http.Error(w, "unauthorized: invalid API token", http.StatusUnauthorized)
					return
				}
				clientID = owner
			}
		}

		ctx := context.WithValue(r.Context(), clientIDKey, clientID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RegisterAppLoginHandler wires POST /auth/app-login: a per-device login where
// the app supplies its own bearer token plus the user's Mediavida credentials.
// Gated by the APP_KEY env var (anti-abuse) when set. No bearer needed.
func RegisterAppLoginHandler(mux *http.ServeMux, sessions *SessionStore, appAliases *AppAliasStore, validTokens map[string]bool) {
	appKey := os.Getenv("APP_KEY")
	isAPIToken := func(id string) bool { return validTokens[id] }
	mux.HandleFunc("POST /auth/app-login", func(w http.ResponseWriter, r *http.Request) {
		if appKey != "" && r.Header.Get("X-App-Key") != appKey {
			writeError(w, http.StatusUnauthorized, "invalid app key")
			return
		}
		var req struct {
			Token string `json:"token"`
			User  string `json:"user"`
			Pass  string `json:"pass"`
			TOTP  string `json:"totp,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		req.Token = strings.TrimSpace(req.Token)
		if req.Token == "" || req.User == "" || req.Pass == "" {
			writeError(w, http.StatusBadRequest, "token, user and pass are required")
			return
		}

		// One MV session per account: if the server already holds a session for
		// this account (its own boot session, or another device's), alias this
		// device token onto it instead of spawning a second concurrent MV login.
		// Two logins for the same account fight for the single session MV allows
		// and knock each other out in a perpetual re-login storm (the app was
		// stuck on 503 because its duplicate session kept losing that fight). Only
		// alias when the supplied credentials match the session we hold, so this
		// never leaks the account to a caller that couldn't have logged in itself;
		// otherwise fall through to a normal (verifying) login.
		if owner, ok := sessions.CanonicalForUser(req.User, isAPIToken); ok {
			if canon := sessions.Get(owner); canon != nil && canon.Scraper != nil &&
				canon.Scraper.MatchesCredentials(req.User, req.Pass) {
				sessions.DropSession(req.Token) // retire any stale own-session under this device token
				appAliases.Add(req.Token, owner)
				log.Printf("[alias] app-login for %q aliased device token onto existing account session", req.User)
				writeJSON(w, http.StatusOK, loginResponse{Status: "authenticated", Username: req.User})
				return
			}
		}

		err := sessions.CreateFromCredentials(req.Token, req.User, req.Pass)
		if err != nil {
			var guardErr *ErrGuardRequired
			if errors.As(err, &guardErr) {
				if req.TOTP == "" {
					writeJSON(w, http.StatusOK, loginResponse{
						Status:  "guard_required",
						Message: "verificación requerida — reenvía con el código (PIN del email o app de autenticación)",
					})
					return
				}
				sess := sessions.Get(req.Token)
				if sess == nil || sess.Scraper == nil {
					writeError(w, http.StatusUnauthorized, "sesión de verificación no encontrada; reintenta")
					return
				}
				if gerr := sess.Scraper.SubmitGuard(guardErr.GuardURL, req.TOTP); gerr != nil {
					writeError(w, http.StatusUnauthorized, "código incorrecto: "+gerr.Error())
					return
				}
				sess.Status = "authenticated"
				sess.Scraper.SaveSession()
				writeJSON(w, http.StatusOK, loginResponse{Status: "authenticated", Username: req.User})
				return
			}
			writeError(w, http.StatusUnauthorized, "login fallido: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, loginResponse{Status: "authenticated", Username: req.User})
	})
}

// --- Login Handler ---

// RegisterLoginHandler sets up the /auth/login (browser) endpoint for Mediavida authentication.
func RegisterLoginHandler(mux *http.ServeMux, sessions *SessionStore) {
	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			flowID := r.URL.Query().Get("flow")
			if flowID == "" {
				http.Error(w, "missing flow parameter", http.StatusBadRequest)
				return
			}
			if _, ok := getAuthFlow(flowID); !ok {
				http.Error(w, "invalid or expired auth flow", http.StatusBadRequest)
				return
			}
			_ = loginTpl.Execute(w, map[string]string{"FlowID": flowID}) // safe-ignore: best-effort response render

		case http.MethodPost:
			flowID := r.FormValue("flow_id")
			user := r.FormValue("user")
			pass := r.FormValue("pass")

			clientID, ok := getAuthFlow(flowID)
			if !ok {
				http.Error(w, "invalid or expired auth flow", http.StatusBadRequest)
				return
			}

			err := sessions.CreateFromCredentials(clientID, user, pass)
			if err != nil {
				var guardErr *ErrGuardRequired
				if errors.As(err, &guardErr) {
					_ = guardTpl.Execute(w, map[string]string{"FlowID": flowID}) // safe-ignore: best-effort response render
					return
				}
				_ = loginTpl.Execute(w, map[string]string{
					"FlowID": flowID,
					"Error":  err.Error(),
				}) // safe-ignore: best-effort response render
				return
			}

			deleteAuthFlow(flowID)
			log.Printf("[auth] client %s authenticated with Mediavida", clientID)
			_ = loginTpl.Execute(w, map[string]string{
				"Success": "Login correcto. La sesión está activa. Puedes cerrar esta ventana.",
			}) // safe-ignore: best-effort response render

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/auth/guard", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		flowID := r.FormValue("flow_id")
		code := r.FormValue("code")

		clientID, ok := getAuthFlow(flowID)
		if !ok {
			http.Error(w, "invalid or expired auth flow", http.StatusBadRequest)
			return
		}

		session := sessions.Get(clientID)
		if session == nil || session.Status != "guard_required" {
			http.Error(w, "no pending guard verification", http.StatusBadRequest)
			return
		}

		if err := session.Scraper.SubmitGuard(session.GuardURL, code); err != nil {
			_ = guardTpl.Execute(w, map[string]string{
				"FlowID": flowID,
				"Error":  err.Error(),
			}) // safe-ignore: best-effort response render
			return
		}

		session.Status = "authenticated"
		session.Scraper.SaveSession()
		deleteAuthFlow(flowID)
		log.Printf("[auth] client %s authenticated with Mediavida (after guard)", clientID)
		_ = loginTpl.Execute(w, map[string]string{
			"Success": "Login correcto. La sesión está activa. Puedes cerrar esta ventana.",
		}) // safe-ignore: best-effort response render
	})
}
