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

func parseModForums(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
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
	hub := NewEventHub()

	// Restore MV sessions from disk for all known API tokens
	for token := range apiTokens {
		if sessions.RestoreFromDisk(token) {
			log.Printf("MV session restored for client %s...", token[:min(16, len(token))])
		}
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

	modForums := parseModForums(os.Getenv("MV_MOD_FORUMS"))
	if len(modForums) > 0 {
		log.Printf("Mod polling enabled for subforums: %v", modForums)
	}

	// Start bubbles poller for SSE/webhook push notifications
	poller := NewBubblesPoller(hub, sessions, webhooks, telegramBot, modForums)
	poller.Start()

	// Start session keepalive to prevent cookies from expiring during idle periods
	keepAlive := NewSessionKeepAlive(sessions)
	keepAlive.Start()

	// Public HTML auth flow (no API token required) — separate mux so it isn't
	// behind the Bearer middleware.
	publicMux := http.NewServeMux()
	RegisterLoginHandler(publicMux, sessions)

	// REST API mux — everything here requires Authorization: Bearer <token>.
	apiMux := http.NewServeMux()
	RegisterAPIRoutes(apiMux, sessions, webhooks, hub, baseURL)

	root := http.NewServeMux()
	root.Handle("/auth/login", publicMux)
	root.Handle("/auth/guard", publicMux)
	root.Handle("/", APITokenMiddleware(apiTokens, apiMux))

	log.Printf("Mediavida API listening on %s", *addr)
	log.Printf("  Base URL:  %s", baseURL)
	log.Printf("  Auth UI:   %s/auth/login (browser flow)", baseURL)
	log.Printf("  REST API:  %s (Bearer token required)", baseURL)
	log.Fatal(http.ListenAndServe(*addr, root))
}
