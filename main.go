package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

func loadEnvFile() {
	f, err := os.Open(".env")
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			if os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}
}

func parseAPITokens() map[string]bool {
	tokens := make(map[string]bool)
	raw := os.Getenv("API_TOKENS")
	if raw == "" {
		return tokens
	}
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tokens[t] = true
		}
	}
	return tokens
}

func main() {
	addr := flag.String("addr", ":1234", "HTTP listen address")
	flag.Parse()

	loadEnvFile()

	apiTokens := parseAPITokens()
	if len(apiTokens) == 0 {
		log.Println("WARNING: No API_TOKENS configured. No clients will be able to connect.")
	} else {
		log.Printf("Loaded %d API token(s)", len(apiTokens))
	}

	sessions := NewSessionStore()
	webhooks := NewWebhookStore()
	modForums := NewModForumsStore()
	hub := NewEventHub()

	// Restore MV sessions from disk for all known API tokens. If no session is
	// on disk (e.g. a fresh deploy), fall back to a headless auto-login using
	// MV_USERNAME/MV_PASSWORD (+ MV_TOTP_SECRET for guard) so the server is
	// authenticated without any manual step.
	envUser, envPass := os.Getenv("MV_USERNAME"), os.Getenv("MV_PASSWORD")
	for token := range apiTokens {
		short := token[:min(16, len(token))]
		if sessions.RestoreFromDisk(token) {
			log.Printf("MV session restored for client %s...", short)
			continue
		}
		if envUser != "" && envPass != "" {
			log.Printf("No session on disk for client %s..., attempting headless auto-login", short)
			if err := sessions.AutoLogin(token, envUser, envPass); err != nil {
				log.Printf("Auto-login failed for client %s...: %v", short, err)
			} else {
				log.Printf("Auto-login successful for client %s...", short)
			}
		}
	}

	// Restore per-device app-login sessions persisted on disk (the volume), so
	// users stay logged in across restarts and redeploys.
	if n := sessions.RestoreAllFromDisk(); n > 0 {
		log.Printf("Restored %d per-device session(s) from disk", n)
	}

	var baseURL string
	if envURL := os.Getenv("BASE_URL"); envURL != "" {
		baseURL = strings.TrimRight(envURL, "/")
	} else {
		host := *addr
		if strings.HasPrefix(host, ":") {
			host = "localhost" + host
		}
		baseURL = fmt.Sprintf("http://%s", host)
	}

	// Optional Telegram bot for mod notifications (nil if TELEGRAM_BOT_TOKEN unset)
	telegramBot := NewTelegramBot()
	telegramBot.Start()

	// Start bubbles poller for SSE/webhook push notifications. Mod-forum
	// subscriptions are per-user and configured via /mod/forums.
	poller := NewBubblesPoller(hub, sessions, webhooks, telegramBot, modForums)
	poller.Start()

	// Start session keepalive to prevent cookies from expiring during idle periods
	keepAlive := NewSessionKeepAlive(sessions)
	keepAlive.Start()

	// Public auth flow (no Bearer required): browser flow + per-device app login.
	publicMux := http.NewServeMux()
	RegisterLoginHandler(publicMux, sessions)
	RegisterAppLoginHandler(publicMux, sessions)

	// REST API mux — everything here requires Authorization: Bearer <token>.
	apiMux := http.NewServeMux()
	RegisterAPIRoutes(apiMux, sessions, webhooks, modForums, hub, baseURL)

	apiHandler := APITokenMiddleware(apiTokens, sessions, apiMux)

	root := http.NewServeMux()
	root.Handle("/auth/app-login", publicMux)
	// /auth/login serves both the browser flow (GET ?flow / POST form) and the
	// headless JSON direct-login (POST application/json). Dispatch by method +
	// content-type so the JSON handler isn't shadowed by the browser flow.
	root.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		publicMux.ServeHTTP(w, r)
	})
	root.Handle("/auth/guard", publicMux)
	root.Handle("/", apiHandler)

	log.Printf("Mediavida API listening on %s", *addr)
	log.Printf("  Base URL:  %s", baseURL)
	log.Printf("  Auth UI:   %s/auth/login (browser flow)", baseURL)
	log.Printf("  REST API:  %s (Bearer token required)", baseURL)
	log.Fatal(http.ListenAndServe(*addr, root))
}
