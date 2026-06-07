# PLAN_SCALE — mediavida-api → official app, 2000+ concurrent users

Status: planning. Authored 2026-06-07.
Goal: make `mediavida-api` ready to serve as the **official Mediavida app** for
**2000+ concurrent users**, without catastrophic failure modes.

This document captures the scaling audit, the verdict, and the phased plan.

---

## 1. What we run today

A single-machine **stateful scraper proxy** that maintains one Mediavida (MV)
session per device and exposes a REST API + SSE + push to the Flutter app.

Live state (Fly, `mediavida-api`, personal org), as observed in the proxy logs:

- **1 machine** `d899501c392068`, `shared-cpu-1x / 512MB`, region `cdg`, no
  health checks, single 1GB volume `mvdata` (`/app/.config`).
- Each device gets a bearer token; the backend ties **one `ForumScraper`
  (its own `http.Client` + cookiejar + uTLS transport) per token**, kept in an
  in-memory `SessionStore` (`map[clientID]*Session`, one global `sync.RWMutex`)
  and persisted to one JSON file per token on the volume.
- A **single-goroutine bubbles poller** (`bubbles.go`) iterates all sessions
  every 30s, scraping MV for each, and fires FCM/ntfy/webhook **synchronously
  inside the loop**.
- SSE hub (`events.go`) broadcasts bubble changes; push via FCM (`fcm.go`) and
  ntfy (`ntfy.go`, separate Fly app).
- `main.go` uses bare `http.ListenAndServe` — **no graceful shutdown**.

Today this comfortably serves ~1 active account with zero errors. Everything
below is projection from the code, not observed load.

---

## 2. Verdict: not ready as-is

Realistic ceiling **today ≈ low hundreds of active users (≈50–300)**, far from
2000. Worse than the number: when it breaks, **it does not degrade — everything
drops at once**. A multi-agent audit + adversarial verification agreed: most
"blockers" don't bite at 2000, they bite at 50–500.

### 2.1 Architectural blockers (not fixable by tuning)

1. **Single egress IP scraping MV → ban = total outage.** All traffic exits one
   machine = one IP hitting mediavida.com. Bites at ~50–75 active users: MV /
   Cloudflare returns 403/429/captcha and **every user is cut off at once**. No
   backoff, jitter, circuit breaker, or IP rotation.
2. **Single stateful machine + single volume → no horizontal scaling.** Session
   state lives in memory + a Fly volume (single-attach) → can't run multiple
   machines. 512MB is the hard ceiling for all sessions. Every redeploy kills
   all SSE + pollers (no graceful shutdown).
3. **Load on MV grows linearly with users.** ~67 req/s of polling alone at 2000,
   from one IP — effectively hammering MV.

### 2.2 Engineering blockers (fixable in code)

- Sequential 1-goroutine poller → saturates at ~200–300 sessions (one slow
  scrape blocks the rest). **[verified real]**
- No per-request context/deadline (only global 30s `http.Client` timeout) →
  under MV slowness, blocked goroutines accumulate RAM → OOM at 500–1000.
  **[verified real]**
- Data races on `ForumScraper` fields (`csrfToken`/`threadID`/…, no lock)
  between request and poller → cross-thread likes / stale CSRF at ~100–200
  active. **[verified real]**
- Push/webhook calls synchronous **inside** the poller loop → one slow endpoint
  freezes the whole cycle.
- FCM with no batching (1 POST per token×counter), no rate-limit handling.
- No per-client rate limiting on the API; the baked `appKey` is trivially
  extractable (not real anti-abuse).
- Flutter client amplifies load ×2–3: fixed 25s polling, no backoff; Dio does
  not retry 429/503 but users retry by hand → storm during outages.

---

## 3. The plan

Key reframing from the discussion:

- **Whitelist + vertical scaling + a parallelized poller put the capacity for
  2000 on a single machine.** Multi-machine is then for **availability**, not
  capacity.
- The proxy can argue **parity** with MV: 1 poll/user/30s is *fewer* requests
  than the same user with 3–5 web tabs (each tab polls). With cross-user caching
  of portada/forums, the app generates **less** aggregate MV load than the
  browser. This is the lever to get the IP whitelisted.

### Phase 0 — capacity + stop the bleeding (days, reversible)

- [ ] **Static egress IP + MV whitelist.** Fly app-scoped static egress IP
      ($3.60/mo IPv4, persists across redeploys, shared by up to 64 machines in
      a region). Without it the default egress is shared host NAT (not stable) and
      the whitelist breaks on first redeploy. Ask MV to also **lift the rate
      limit** for that IP, not just the ban. Pitch the parity argument above.
- [ ] **Vertical scale to dedicated CPU.** `performance-2x`/`4x` (not
      `shared-cpu-Nx`): HTML parsing with goquery is CPU-bound under load and
      suffers CPU steal on shared. 2–4 GB RAM.
- [ ] **Raise `ulimit -n` (NOFILE).** ~2000 SSE in + outbound MV conns ≈ 4000+
      fds; the 1024 default fails before RAM does.
- [ ] **Graceful shutdown.** Wrap in `http.Server`, handle SIGTERM, call
      `poller.Stop()` + drain SSE before exit (`bubbles.go` already has `Stop()`,
      never invoked).
- [ ] **Per-request context deadlines** on all scraper calls (kills the OOM
      accumulation failure mode).

Outcome: hundreds of stable users, no outage-by-ban.

### Phase 1 — make one machine reach ~1–2k (1–2 weeks)

- [ ] **Parallelize the poller**: worker pool + jitter + exponential backoff on
      429/5xx + per-session circuit breaker. Stagger the baseline fetch over the
      interval to avoid thundering herd.
- [ ] **Gate polling on `hub.HasSubscribers(clientID)`** — don't poll sessions
      nobody is listening to (~-75% load when most users are idle).
- [ ] **Cross-user cache** of portada / forum lists (identical for everyone).
- [ ] **Async push/webhooks**: queue + worker pool, out of the poller loop;
      batch FCM, dedup tokens, handle rate limits.
- [ ] **Per-session lock** on `ForumScraper` (RWMutex) to kill the data races.
- [ ] **Per-client rate limiting** on the API.
- [ ] **Client (Flutter)**: exponential backoff + jitter, retry 429/503, request
      dedup, SSE heartbeat/reconnect.

### Phase 2 — HA / multi-machine (when "drops on deploy" is unacceptable)

For an official app it will be. The target store is **Colmena** (our own
embedded distributed SQLite over Raft), **not Redis** — it's a Go `import`, not
another service to deploy/monitor, and it fits the "it's just a Go process"
philosophy.

Why Colmena fits this specific problem:

- **`colmena/jobs` with cluster-wide rate limits** directly solves blocker #3:
  a **global req/s budget toward MV split across all nodes**, with retry/jitter
  built in. The poller becomes Raft-logged poll jobs any worker picks up.
- **Full replication + `OnApply` hooks**: every node holds the whole DB → any
  node serves any user → **real HA without mandatory stickiness**. `fly-replay`
  becomes an optimization (hot-cache affinity), not a correctness requirement.

Design rules that must hold (these are the real work):

1. **Don't put churn in the Raft log.** What needs consensus/durability (the MV
   session: token, valid cookies, push tokens) goes through Raft; ephemeral /
   derivable state (last bubble count, the hot scraper) stays local in memory.
   Cookie refreshes that fire every poll must NOT replicate per change.
2. **All nodes in one region (cdg).** Raft needs low inter-node latency for
   quorum; multi-region kills it. Matches the per-region egress IP (≤64 machines).
3. **Fly-native discovery** (Colmena's `lan/` uses mDNS, which doesn't traverse
   Fly's 6PN private network). Tracked in `~/colmena/PLAN_FLY.md`.
4. **Relaxed read consistency** (`Weak`/`None`) on the request path; reserve
   `Strong` for the few writes that need it — avoid quorum latency per request.

Target stack: **Go `mediavida-api` binary + embedded Colmena, 3 nodes in cdg,
shared whitelisted static egress IP, poller + push as Colmena jobs with a global
MV rate limit, MV session in Raft / hot scraper in local memory.** No external
service added.

Maturity / risk: Colmena is **extensively tested and benchmarked**, but in
production it has only run on a secondary app (anonat.org) with negligible
traffic. So **mediavida would be its first serious-traffic deployment**. Keep
Phase 2 gated accordingly: ship Phases 0–1 first (capacity on one machine),
harden the Fly integration (`~/colmena/PLAN_FLY.md`) and soak-test Colmena under
synthetic load in parallel, and adopt it for HA only once capacity is already
covered. Treat mediavida as Colmena's hardening milestone, not a bet.

---

## 4. Reframed ceilings

| Setup | Realistic ceiling | Failure mode |
|---|---|---|
| Today | ~50–300 active | total outage (IP ban / OOM) |
| Phase 0 (whitelist + vertical + graceful + deadlines) | hundreds, stable | graceful, no ban |
| Phase 1 (parallel poller + cache + async push) | ~1–2k on one machine | degrades, recovers |
| Phase 2 (Colmena multi-node, cdg) | 2k+ with fault tolerance | survives deploy/crash |
