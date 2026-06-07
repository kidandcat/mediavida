package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// raiseNOFILE lifts the open-file-descriptor soft limit to the hard max. With
// ~2000 SSE streams plus outbound MV connections the process needs several
// thousand fds; the common 1024 default would fail before RAM does.
func raiseNOFILE() {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		log.Printf("[rlimit] getrlimit NOFILE failed: %v", err)
		return
	}
	if lim.Cur < lim.Max {
		lim.Cur = lim.Max
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
			log.Printf("[rlimit] setrlimit NOFILE failed: %v", err)
			return
		}
	}
	log.Printf("[rlimit] NOFILE cur=%d max=%d", lim.Cur, lim.Max)
}

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
				_ = os.Setenv(k, v) // safe-ignore: best-effort env propagation
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

	raiseNOFILE()
	loadEnvFile()

	apiTokens := parseAPITokens()
	if len(apiTokens) == 0 {
		log.Println("WARNING: No API_TOKENS configured. No clients will be able to connect.")
	} else {
		log.Printf("Loaded %d API token(s)", len(apiTokens))
	}

	// Colmena: durable Raft-replicated store. This is the ONLY persistence layer
	// (no JSON-on-volume fallback, no enable gate). StartColmena never returns nil
	// — in a Fly cluster it forms/joins Raft, otherwise it runs a single local
	// bootstrap node for dev. All stores below are backed by it.
	colmenaStore := StartColmena()

	sessions := NewSessionStore(colmenaStore)
	webhooks := NewWebhookStore(colmenaStore)
	modForums := NewModForumsStore(colmenaStore)
	hub := NewEventHub()

	// Rehydrate every durable session from Colmena (per-device app-login tokens
	// included, not just the configured API tokens), so logins survive restarts
	// and redeploys. This replaces the old per-token JSON-on-disk restore loop.
	if n := sessions.RestoreAll(); n > 0 {
		log.Printf("Restored %d session(s) from Colmena", n)
	}

	// For any configured API token that still has no live session (e.g. a fresh
	// cluster with an empty Raft log), fall back to a headless auto-login using
	// MV_USERNAME/MV_PASSWORD (+ MV_TOTP_SECRET for guard) so the server is
	// authenticated without any manual step. RestoreSession re-checks Colmena per
	// token in case it wasn't picked up above.
	envUser, envPass := os.Getenv("MV_USERNAME"), os.Getenv("MV_PASSWORD")
	for token := range apiTokens {
		short := token[:min(16, len(token))]
		if sessions.Get(token) != nil || sessions.RestoreSession(token) {
			log.Printf("MV session restored for client %s...", short)
			continue
		}
		if envUser != "" && envPass != "" {
			log.Printf("No stored session for client %s..., attempting headless auto-login", short)
			if err := sessions.AutoLogin(token, envUser, envPass); err != nil {
				log.Printf("Auto-login failed for client %s...: %v", short, err)
			} else {
				log.Printf("Auto-login successful for client %s...", short)
			}
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

	// Optional ntfy push publisher (nil if NTFY_URL/NTFY_TOKEN unset)
	ntfyPub := NewNtfyPublisher()

	// FCM push (nil if FCM_SA_JSON/FCM_PROJECT_ID unset). Per-device tokens are
	// registered via POST /push/register.
	fcmTokens := NewFCMTokenStore(colmenaStore)
	fcmSender := NewFCMSender(fcmTokens)

	// Start bubbles poller for SSE/webhook push notifications. Mod-forum
	// subscriptions are per-user and configured via /mod/forums.
	poller := NewBubblesPoller(colmenaStore, hub, sessions, webhooks, telegramBot, ntfyPub, fcmSender, modForums)
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
	RegisterAPIRoutes(apiMux, sessions, webhooks, modForums, hub, fcmTokens, baseURL)

	// Per-client API rate limiter (PLAN_SCALE 2.2 / Phase 1). Sits AFTER auth so
	// it can read the clientID from the request context. Defaults: ~5 req/s
	// sustained, burst 20.
	limiter := NewRateLimiter(5, 20)
	apiHandler := APITokenMiddleware(apiTokens, sessions, limiter.Middleware(apiMux))

	root := http.NewServeMux()
	// Health check for Fly's rolling deploy (keeps Raft quorum). 200 once this
	// node is caught up; 503 while a cluster node is still joining. Local dev mode
	// is always healthy as soon as the bootstrap node is up.
	root.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if colmenaStore.Healthy() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok")) // safe-ignore: best-effort health body
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
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

	srv := &http.Server{Addr: *addr, Handler: root}

	log.Printf("Mediavida API listening on %s", *addr)
	log.Printf("  Base URL:  %s", baseURL)
	log.Printf("  Auth UI:   %s/auth/login (browser flow)", baseURL)
	log.Printf("  REST API:  %s (Bearer token required)", baseURL)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown: Fly sends SIGTERM before replacing/killing the machine.
	// Stop the background loops first so they can't publish to SSE channels that
	// are mid-drain, end the SSE streams, then let in-flight requests finish.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	log.Printf("received %s — shutting down gracefully", sig)

	poller.Stop()
	keepAlive.Stop()
	hub.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown timed out: %v", err)
	}
	// Leave the Raft cluster last (transfers leadership) so a rolling deploy
	// keeps quorum. In local dev mode this just closes the node.
	leaveCtx, leaveCancel := context.WithTimeout(context.Background(), 8*time.Second)
	colmenaStore.Leave(leaveCtx)
	leaveCancel()
	log.Println("shutdown complete")
}
