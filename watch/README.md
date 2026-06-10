# Mediavida watch

Zepp OS watchface that shows your Mediavida unread counters (messages / avisos /
favoritos) as three badges around an analog clock, on **Amazfit Active 2 (round,
480×480)**.

The data is fetched from the authenticated Mediavida backend
(`mediavida-api.fly.dev`). The watch pairs **once** with the phone's Mediavida
Flutter app over a localhost handshake; after that it is autonomous.

## Why two apps

A Zepp OS **watchface cannot run a side service and cannot do networking** — this
is a platform limitation. So the project is split into two co-installed apps:

- **watchface** (`appId 1024001`, `appType: watchface`) — pure rendering. Reads a
  small `data.json` written by the companion and draws the badges + clock. No
  network.
- **companion** mini-app (`appId 1024002`, `appType: app`) — does the networking.
  Its **app-side service** (runs on the phone, has global `fetch`) performs the
  pairing handshake and the bubbles fetch; its **device page** (runs on the
  watch, the only context with filesystem access) persists credentials/data and
  schedules the refresh alarm.

```
phone (Flutter pairing server)  <--/pair (localhost, once)--  companion app-side
backend /bubbles  <--Bearer token (every cycle)-------------  companion app-side
companion app-side  <--MessageBuilder-->  companion device page  --writes-->  data.json
data.json  --read cross-app-->  watchface  (renders)
```

## Data flow

1. **Refresh trigger.** A 5-minute alarm (or the watchface tap-to-refresh) opens
   the companion device page.
2. **Pairing (first time only).** The page reads `pair.json`; if there's no
   token, it asks the app-side to pair. The app-side does
   `GET http://127.0.0.1:28590/pair` with header
   `X-Watch-App-Key: 70f54fb484323ae9c7fcaff542bcfda8`. The Flutter app (pairing
   screen open) returns `{token, base_url}`. The app-side hands these back to the
   page, which persists them to `pair.json`. If the phone app isn't open the page
   shows **"Abre la app de Mediavida para emparejar"** and retries next cycle.
3. **Autonomous fetch.** With a token, the app-side fetches `GET <base_url>/bubbles`
   with `Authorization: Bearer <token>`, response
   `{messages, notifications, favorites}`, mapped to `{bm, bn, bf}`.
4. **Persist.** The page writes `data.json` (`{bm,bn,bf,ts}`) in the companion
   sandbox.
5. **Render.** The watchface reads `data.json` cross-app and updates the badges.
6. **Re-pair on expiry.** If `/bubbles` returns `401`, the page clears `pair.json`
   so the next cycle re-pairs.

Full constants and message shapes are in [`CONTRACT.md`](./CONTRACT.md) — the
single source of truth shared with the Flutter app and the Go backend.

## Project layout

```
watch/
  README.md            # this file
  CONTRACT.md          # handshake + bubbles contract & constants (source of truth)
  shared/              # SINGLE source of shared JS (edit here)
    message.js, message-side.js, data.js, defer.js, event.js,
    device-polyfill.js, es6-promise.js     # MessageBuilder framework + polyfills
    bubbles.js                             # handshake + bubbles fetch (device-agnostic)
  sync-shared.sh       # copies shared/ into each app's shared/ before build
  .gitignore           # ignores the generated per-app shared/ dirs
  devices/
    amazfit-active2-round/
      watchface/   # appId 1024001: app.js, app.json, watchface/index.js,
                   # app-side/index.js (stub), assets/<device>/*, package.json
      companion/   # appId 1024002: app.js, app.json, app-side/index.js,
                   # page/index.js, assets/<device>/icon.png, package.json
```

### Generated `shared/` dirs

Zeus builds each app folder in isolation and **cannot import JS from outside the
app folder** (there are no workspaces). So the canonical shared code lives once in
`watch/shared/`, and `sync-shared.sh` copies it into every app's own `shared/`
directory before building. Those per-app `devices/*/*/shared/` dirs are
**generated build artifacts** and are **gitignored** — never edit them by hand;
edit `watch/shared/` and re-run `sync-shared.sh`. (Chosen over committing them so
there's exactly one source of truth and no risk of divergent copies.)

## Build / install

> No watch is required to build; you need a Zepp OS toolchain (`@zeppos/zeus-cli`)
> and a connected device (or the simulator) only to flash/preview.

```sh
cd watch
./sync-shared.sh                     # mirror shared/ into each app dir

# Watchface
cd devices/amazfit-active2-round/watchface
zeus dev        # live preview on device/simulator
zeus build      # produce the .zab in dist/

# Companion (separate terminal / step)
cd ../companion
zeus dev
zeus build
```

Install **both** `.zab` packages on the watch (companion first so `data.json`
exists, though the watchface tolerates a missing file). Then open the Mediavida
phone app's pairing screen once and let the watch pair.

## Adding a new watch model

The device-agnostic logic (MessageBuilder framework + `bubbles.js`
handshake/fetch) lives in `shared/`, so a new model is mostly a copy + a layout
pass:

1. `cp -r devices/amazfit-active2-round devices/<new-model>`.
2. In **both** `watchface/app.json` and `companion/app.json` of the new folder,
   update `targets.<key>`, the `platforms[]` (`name` + `deviceSource`) and
   `designWidth` for the new device. Keep `appId` `1024001` / `1024002`.
3. Replace the assets under `watchface/assets/<new-model>/` (and the companion
   `icon.png`) with art sized for the new screen, and adjust the widget
   coordinates in `watchface/watchface/index.js` to the new `designWidth`.
4. The companion's `app-side/index.js` and `page/index.js` and all of `shared/`
   are reused unchanged — they have no device-specific code.
5. Run `./sync-shared.sh` and `zeus build` in each app dir.

## Notes to validate on a real device (Zepp OS lifecycle)

These follow the original project's proven patterns but should be re-verified
on hardware:

- **App-side statelessness.** The companion app-side service may be torn down
  between wakeups, so it's treated as stateless: the device page passes
  credentials on every request and persists anything minted back. If on this
  firmware the app-side actually persists for the session, this still works (just
  redundant) — but the page remains the durable store either way.
- **Alarm chain.** Each page run schedules the next alarm *before* the network
  step, so a network failure can't break the 5-minute chain. Confirm
  `@zos/alarm` `set/cancel` ids survive across the documented refresh window.
- **Cross-app file read.** The watchface reads `data.json` via
  `openSync({ path:'data.json', options:{ appId: 1024002 } })`; verify the
  companion's sandbox path is readable by the watchface on this OS version.
- **MessageBuilder handshake timing.** The page retries `sendShake()` up to ~30s
  waiting for `appSidePort`; tune `MAX_RETRIES`/`RETRY_DELAY` if pairing is slow.
- **`fetch` to plain-HTTP localhost.** `GET http://127.0.0.1:28590/pair` is
  cleartext loopback from the app-side (phone). Confirm the side service allows
  non-HTTPS loopback requests on the installed Zepp app version.
