# Mediavida API

HTTP REST API to interact with the [Mediavida](https://www.mediavida.com) forum.
Wraps a stateful scraper that maintains the MV session (cookies, CSRF tokens,
guard re-verification via TOTP) so client code doesn't need to.

- **Base URL:** configurable, defaults to `http://localhost:1234`
- **Auth:** `Authorization: Bearer <api-token>` (every endpoint except `/auth/login` HTML and `/auth/guard` HTML)
- **Format:** JSON in / JSON out (UTF-8)
- **Push:** Server-Sent Events (`GET /events`) and outgoing webhooks for bubble changes

---

## Configuration

Environment variables (also loadable from `.env`):

| Variable | Required | Description |
| --- | --- | --- |
| `API_TOKENS` | yes | Comma-separated list of allowed Bearer tokens. Each token represents one client and gets its own MV session. |
| `BASE_URL` | no | Public URL prefix used to build the browser auth flow URL. Defaults to `http://localhost:<addr>`. |
| `MV_TOTP_SECRET` | no | Base32 TOTP secret for automatic guard re-verification on session expiry. |
| `MV_MOD_FORUMS` | no | Comma-separated subforum slugs (e.g. `off-topic,general`) to poll for moderation counters. Requires the logged-in MV user to be a mod of those subforums. |
| `TELEGRAM_BOT_TOKEN` | no | Telegram bot token for mod alerts. |
| `TELEGRAM_ADMINS` | no | `tg_user_id:mv_username,...` mapping for the Telegram bot. |

Run:

```bash
go run . -addr :1234
```

Or build a binary:

```bash
go build -o mediavida .
./mediavida -addr :1234
```

---

## Authentication

There are two ways to log in to Mediavida:

### 1. Direct login (recommended for headless clients)

```bash
curl -X POST http://localhost:1234/auth/login \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"user":"yourname","pass":"yourpass"}'
```

If MV requires guard verification (email PIN or TOTP), the response is:

```json
{ "status": "guard_required", "message": "..." }
```

Resend the same request including `totp`:

```bash
curl -X POST http://localhost:1234/auth/login \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"user":"yourname","pass":"yourpass","totp":"123456"}'
```

On success:

```json
{ "status": "authenticated", "username": "yourname" }
```

### 2. Browser flow (interactive)

Get a one-time login URL:

```bash
curl -X POST http://localhost:1234/auth/web \
  -H "Authorization: Bearer $TOKEN"
```

Response:

```json
{ "url": "http://localhost:1234/auth/login?flow=abc123..." }
```

Open the URL in a browser, fill in user/pass (and PIN if requested). The server
ties the resulting MV session to the API token used to start the flow.

### Status and logout

```bash
curl http://localhost:1234/auth/status -H "Authorization: Bearer $TOKEN"
# → {"status":"authenticated","username":"yourname"}

curl -X POST http://localhost:1234/auth/logout -H "Authorization: Bearer $TOKEN"
# → {"status":"logged_out"}
```

Sessions are persisted to disk per API token, so restarting the server keeps you
logged in. With `MV_TOTP_SECRET` set, expired sessions are re-verified
automatically without intervention.

---

## Endpoints

All endpoints below require `Authorization: Bearer <token>` and return JSON
unless noted otherwise.

### Threads

#### `GET /threads`

Read a single page of a thread.

| Query param | Type | Notes |
| --- | --- | --- |
| `url` | string | **required.** Full thread URL, e.g. `https://www.mediavida.com/foro/dev/taberna-682201` |
| `page` | int | Optional. 1-based page number. Defaults to the last page. |

```bash
curl -G http://localhost:1234/threads \
  -H "Authorization: Bearer $TOKEN" \
  --data-urlencode "url=https://www.mediavida.com/foro/dev/taberna-682201" \
  --data-urlencode "page=3"
```

Response:

```json
{
  "current_page": 3,
  "total_pages": 42,
  "messages": [
    { "num": 41, "author": "alice", "body": "hola...", "liked": false },
    { "num": 42, "author": "bob",   "body": "qué tal", "liked": true  }
  ]
}
```

> **Stateful note.** `GET /threads` updates the scraper's "current thread" state
> (CSRF token, thread ID, forum ID, page number). `POST /threads/like` and
> `POST /threads/reply` operate on that last-read thread, so call them right
> after a `GET /threads` for the thread you want to act on.

#### `POST /threads/like`

Like (mano) a post in the last-read thread.

```bash
curl -X POST http://localhost:1234/threads/like \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"post_num": 42}'
```

Response: `{"status":"liked","post_num":42}`

#### `POST /threads/reply`

Reply to the last-read thread. `reply_to_num` is optional; when set, the body is
prepended with `#N ` to quote that post.

```bash
curl -X POST http://localhost:1234/threads/reply \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"text": "buena reflexión", "reply_to_num": 42}'
```

Response: `{"status":"posted"}`

#### `GET /threads/tags`

List the tag IDs available for a subforum's "new thread" form. Use this before
`POST /threads` to discover the right `tag` value.

```bash
curl -G http://localhost:1234/threads/tags \
  -H "Authorization: Bearer $TOKEN" \
  --data-urlencode "subforum=dev"
```

Response:

```json
{
  "subforum": "dev",
  "fid": "10",
  "tags": [
    { "id": "21", "name": "General" },
    { "id": "22", "name": "Pregunta" }
  ]
}
```

#### `POST /threads`

Create a new thread.

```bash
curl -X POST http://localhost:1234/threads \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "subforum": "dev",
    "title": "Mi nuevo hilo",
    "body": "Contenido del primer post.",
    "tag": 21,
    "add_to_favorites": true
  }'
```

Response: `{"status":"submitted","response": <MV JSON> }`

### Search

#### `GET /search`

Searches Mediavida via DuckDuckGo (MV's own search uses Google CSE which can't
be scraped). Returns the top results scoped to `mediavida.com/foro`.

```bash
curl -G http://localhost:1234/search \
  -H "Authorization: Bearer $TOKEN" \
  --data-urlencode "q=cloudflare bypass"
```

Response:

```json
{
  "query": "cloudflare bypass",
  "results": [
    { "title": "...", "url": "https://www.mediavida.com/foro/...", "date": "..." }
  ]
}
```

### Private messages

#### `GET /inbox`

```bash
curl http://localhost:1234/inbox?page=1 -H "Authorization: Bearer $TOKEN"
```

Response:

```json
{
  "page": 1,
  "conversations": [
    { "id": "12345", "author": "alice", "date": "...", "preview": "...", "unread": true }
  ]
}
```

#### `GET /messages/{id}`

```bash
curl http://localhost:1234/messages/12345 -H "Authorization: Bearer $TOKEN"
```

Response:

```json
{
  "title": "Re: Hola",
  "messages": [
    { "author": "alice", "date": "...", "body": "Mensaje 1" },
    { "author": "you",   "date": "...", "body": "Respuesta" }
  ]
}
```

### Favorites and user content

#### `GET /favorites`

Returns the user's favorited (starred) threads with unread counts.

#### `GET /users/{username}/posts`

Threads where `username` has posted. Pass `me` or omit (and call without trailing
slash via `/users//posts`) to use the logged-in user — easiest is to just pass
the username explicitly.

#### `GET /users/{username}/mentions`

Recent posts where `@username` was mentioned.

```bash
curl http://localhost:1234/users/alice/mentions -H "Authorization: Bearer $TOKEN"
```

Response:

```json
{
  "username": "alice",
  "mentions": [
    {
      "thread_title": "...",
      "thread_url": "https://www.mediavida.com/foro/...",
      "author": "bob",
      "post_num": 17,
      "body": "@alice qué piensas...",
      "date": "..."
    }
  ]
}
```

### Notifications (bubbles)

#### `GET /bubbles`

Current notification counters.

```bash
curl http://localhost:1234/bubbles -H "Authorization: Bearer $TOKEN"
# → {"messages":0,"notifications":2,"favorites":0}
```

#### `GET /events` (Server-Sent Events)

Streams a `bubbles` event every time the counters change for the authenticated
client. The server sends a comment ping every 25s to keep the connection open
through proxies. Reconnect logic should be the standard SSE behavior (browsers
do this automatically with `EventSource`).

```bash
curl -N http://localhost:1234/events -H "Authorization: Bearer $TOKEN"
```

Stream:

```
: connected

event: bubbles
data: {"messages":0,"notifications":3,"favorites":1}

: ping
```

JavaScript example:

```js
const es = new EventSource("/events", { withCredentials: false });
// EventSource doesn't support custom headers; for tokens use a query string,
// a cookie, or a fetch-based polyfill. In Go/CLI use fetch+ReadableStream.
es.addEventListener("bubbles", (e) => {
  const b = JSON.parse(e.data);
  console.log("new bubbles:", b);
});
```

### Outgoing webhook

Configure a URL that the server will POST to whenever bubbles change for the
authenticated user. One webhook per MV user.

#### `GET /webhook`

```json
{ "username": "yourname", "url": "https://example.com/hook" }
```

#### `PUT /webhook`

```bash
curl -X PUT http://localhost:1234/webhook \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://example.com/hook"}'
```

#### `DELETE /webhook`

Removes the webhook (returns 204).

#### Webhook payload

```json
{
  "username": "yourname",
  "messages": 0,
  "notifications": 3,
  "favorites": 1
}
```

If the server detects the MV session has expired and re-login also fails
(e.g. guard required and no `MV_TOTP_SECRET`), it sends a sentinel payload with
all counters set to `-1` so your webhook receiver can prompt for re-auth.

### Health

`GET /healthz` → `{"status":"ok"}`. Public-style sanity check (still requires a
Bearer token because everything under `/` is behind the middleware).

---

## Errors

Errors are JSON with HTTP status codes:

```json
{ "error": "no active mediavida session — call POST /auth/login" }
```

| Status | Meaning |
| --- | --- |
| 400 | Invalid input (missing param, malformed JSON) |
| 401 | Missing or invalid Bearer token, or no MV session |
| 502 | Mediavida returned an unexpected response or could not be reached |

When MV returns a session-expired sentinel, the server transparently re-logs in
once (using stored credentials, plus `MV_TOTP_SECRET` if guard verification
fires) and retries the call. You should rarely see 502s in practice.

---

## Background work

Even with no clients connected, the server runs:

- **Bubbles polling** every 30s for each authenticated session, broadcasting
  changes via SSE and webhooks.
- **Session refresh** every ~5 min (visits `mediavida.com/`) and **session save
  to disk** every ~10 min, so cookies stay fresh.
- **Keepalive** every 4h, an extra safety net that visits the home page even if
  bubble polling is failing.
- **Mod polling** for `MV_MOD_FORUMS` if the user is a moderator there;
  Telegram alerts go out for new reports and a daily summary at 21:00
  Europe/Madrid.

---

## Storage

Per-client session cookies and credentials live in:

```
~/Library/Application Support/mediavida-mcp/session-<token>.json   # macOS
~/.config/mediavida-mcp/session-<token>.json                       # Linux
```

Webhook configuration is in the same directory under `webhooks.json`.

---

## Tests

```bash
go test -run TestLoginFlow -v
```

The test suite expects valid `MV_USERNAME`/`MV_PASSWORD` env vars and hits the
real Mediavida site, so don't run it in CI without rate-limit considerations.
