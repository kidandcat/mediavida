package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"log"
	"net/http"
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
	authFlows   sync.Map
	flowCleanup sync.Once
)

func generateFlowID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func createAuthFlow(clientID string) string {
	flowCleanup.Do(func() {
		go func() {
			for {
				time.Sleep(10 * time.Minute)
				now := time.Now()
				authFlows.Range(func(key, value any) bool {
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
	flow := v.(*authFlow)
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
// Sets the client ID (the token itself) in the request context.
func APITokenMiddleware(validTokens map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized: Bearer token required", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		if !validTokens[token] {
			http.Error(w, "unauthorized: invalid API token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), clientIDKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
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
			loginTpl.Execute(w, map[string]string{"FlowID": flowID})

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
				if _, ok := err.(*ErrGuardRequired); ok {
					guardTpl.Execute(w, map[string]string{"FlowID": flowID})
					return
				}
				loginTpl.Execute(w, map[string]string{
					"FlowID": flowID,
					"Error":  err.Error(),
				})
				return
			}

			deleteAuthFlow(flowID)
			log.Printf("[auth] client %s authenticated with Mediavida", clientID)
			loginTpl.Execute(w, map[string]string{
				"Success": "Login correcto. La sesión está activa. Puedes cerrar esta ventana.",
			})

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
			guardTpl.Execute(w, map[string]string{
				"FlowID": flowID,
				"Error":  err.Error(),
			})
			return
		}

		session.Status = "authenticated"
		session.Scraper.SaveSession()
		deleteAuthFlow(flowID)
		log.Printf("[auth] client %s authenticated with Mediavida (after guard)", clientID)
		loginTpl.Execute(w, map[string]string{
			"Success": "Login correcto. La sesión está activa. Puedes cerrar esta ventana.",
		})
	})
}
