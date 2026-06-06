# mediavida-ntfy

Self-hosted [ntfy](https://ntfy.sh) server that delivers push notifications for
the Mediavida app. Independent Fly app (`mediavida-ntfy`), one shared-cpu
machine, 512 MB, auto-stop when idle.

## How it fits together

- `mediavida-api` (the backend) **publishes** to ntfy when a user's bubble
  counters go up (new avisos / MPs / favoritos) — see `ntfy.go` /
  `NtfyPublisher`. It authenticates with a publisher **access token**
  (`NTFY_TOKEN` secret on `mediavida-api`, `NTFY_URL=https://mediavida-ntfy.fly.dev`).
- The app **subscribes** to its own secret topic over SSE and shows a local
  notification — see `mobile/lib/core/notification_service.dart`.
- **Topic** = `mv_<first 32 hex of sha256(deviceToken)>`. Derived identically on
  both sides so no registration step is needed. Per-device and not guessable.

## Access model

`NTFY_AUTH_DEFAULT_ACCESS=deny-all`. Two rules (set once, stored in the
volume's `auth.db`):

- anonymous (`*`): **read-only** on `mv_*` → the app subscribes without creds.
- user `publisher`: **read-write** on `mv_*` → the backend publishes.

The backend uses an access token for `publisher` (not the password).

## First-time / re-provision setup

```sh
# create the app + volume, then deploy
fly apps create mediavida-ntfy -o personal
fly volumes create ntfy_data -a mediavida-ntfy -r cdg -n 1 -s 1
fly deploy -c deploy/ntfy/fly.toml --dockerfile deploy/ntfy/Dockerfile

# configure auth/ACL (publisher password is arbitrary; only the token is used)
fly ssh console -a mediavida-ntfy -C "sh -lc 'NTFY_PASSWORD=<pw> ntfy user add publisher'"
fly ssh console -a mediavida-ntfy -C "sh -lc 'ntfy access publisher mv_* rw'"
fly ssh console -a mediavida-ntfy -C "sh -lc 'ntfy access everyone mv_* read-only'"
fly ssh console -a mediavida-ntfy -C "sh -lc 'ntfy token add publisher'"   # -> tk_...

# wire the token into the backend
fly secrets set NTFY_URL=https://mediavida-ntfy.fly.dev NTFY_TOKEN=tk_... -a mediavida-api
```

## Quick test

```sh
# publish (needs the publisher token)
curl -H "Authorization: Bearer tk_..." \
  -d '{"topic":"mv_test","title":"Hi","message":"hello"}' https://mediavida-ntfy.fly.dev
# read (anonymous, allowed on mv_*)
curl "https://mediavida-ntfy.fly.dev/mv_test/json?poll=1"
```
