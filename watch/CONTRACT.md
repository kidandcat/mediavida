# Watch data contract

Single source of truth for the constants and message shapes shared between the
watch apps, the Mediavida Flutter app, and the backend. Any change here must be
mirrored in all three places.

## 1. Localhost pairing handshake (one-time)

While the Mediavida Flutter app's pairing screen is open it runs a tiny HTTP
server bound to loopback. The companion mini-app's app-side service calls it
once, when it has no stored watch token.

| Field            | Value                                                  |
| ---------------- | ------------------------------------------------------ |
| URL              | `http://127.0.0.1:28590/pair`                          |
| Method           | `GET`                                                  |
| Required header  | `X-Watch-App-Key: 70f54fb484323ae9c7fcaff542bcfda8`    |
| Success response | `200 { "token": "wf_<hex>", "base_url": "https://mediavida-api.fly.dev" }` |
| Wrong key        | `403 { "error": "forbidden" }`                         |
| Not reachable    | connection refused (phone app / screen not open)       |

- The app key is a **non-secret gate** — it only stops random localhost callers.
  The real credential is the minted `token`. Safe to commit.
- Flutter side: `mobile/lib/core/watch_pairing_server.dart`.
- The token aliases the owner's Mediavida session server-side
  (`watch.go` → `SessionStore.CreateWatchAlias`), so the watch is autonomous
  after pairing.

## 2. Backend bubbles fetch (autonomous, every cycle)

Once paired, the companion's app-side fetches counters directly from the
backend, with no phone-app involvement.

| Field            | Value                                                       |
| ---------------- | ---------------------------------------------------------- |
| URL              | `<base_url>/bubbles` (e.g. `https://mediavida-api.fly.dev/bubbles`) |
| Method           | `GET`                                                      |
| Required header  | `Authorization: Bearer <token>`                            |
| Success response | `200 { "messages": N, "notifications": N, "favorites": N }` |
| Bad/expired token| `401` → companion clears stored token and re-pairs         |

- Backend route: `api.go` `GET /bubbles` → `{messages, notifications, favorites}`.

## 3. Field-name mapping

The backend uses `{messages, notifications, favorites}`. The watchface has always
read `{bm, bn, bf}`. **Decision: map at the companion boundary** (in
`shared/bubbles.js`), so the watchface rendering code stays untouched:

```
messages      -> bm   (envelope badge)
notifications -> bn   (exclamation badge)
favorites     -> bf   (star badge)
```

## 4. data.json (companion sandbox → watchface)

The companion device page writes `data.json` in its own app sandbox
(appId 1024002). The watchface reads it cross-app via
`openSync({ path: 'data.json', options: { appId: 1024002 } })`. Shape unchanged
from the old design:

```json
{ "bm": 3, "bn": 0, "bf": 1, "ts": 1717900000000 }
```

`ts` is `Date.now()` at write time (used for the "last updated" status line).

## 5. Companion-internal persistence files (device page only)

Only the device page has filesystem access, so it owns all durable files in the
companion sandbox:

| File         | Shape                       | Purpose                                  |
| ------------ | --------------------------- | ---------------------------------------- |
| `pair.json`  | `{ token, baseUrl }`        | Paired watch credentials (survives wakeups) |
| `data.json`  | `{ bm, bn, bf, ts }`        | Counters the watchface reads             |
| `alarm.json` | `{ id }`                    | Pending refresh alarm id (to cancel/replace) |

After a `401` the page overwrites `pair.json` with `{}` so the next cycle
re-pairs.

## 6. MessageBuilder protocol (device page ↔ app-side)

The companion's app-side service is **stateless across wakeups** (no FS access),
so the page passes credentials on each request and persists anything minted back.

Request (page → app-side):

```js
{ method: 'GET_BUBBLES', token?: '<token>', baseUrl?: '<base_url>' }
```

Response (app-side → page), exactly one of:

| Response                              | Meaning / page action                        |
| ------------------------------------- | -------------------------------------------- |
| `{ result: {bm,bn,bf} }`              | fetched OK → write `data.json`               |
| `{ result: {bm,bn,bf}, paired: {token,baseUrl} }` | first-time pair OK → persist `pair.json` + write `data.json` |
| `{ needPair: true }`                  | phone app not open → status "Abre la app…"   |
| `{ unauthorized: true }`              | 401 → clear `pair.json`, re-pair next cycle  |
| `{ error: '<msg>' }`                  | transient fetch error → keep last `data.json`|

## 7. App IDs

| App                | appId     | appType     |
| ------------------ | --------- | ----------- |
| Watchface          | `1024001` | `watchface` |
| Companion mini-app | `1024002` | `app`       |

These are baked into cross-app file reads and alarm/launch targets — do not
change without updating both apps.
