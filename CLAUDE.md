# Mediavida — project notes

Personal project (owner **kidandcat**). Backend is a Go scraper-backed REST API
for the Mediavida forum; `mobile/` is the Flutter app.

## ⚠️ Accounts — ALWAYS use the PERSONAL account

This is a **personal** project. The machine's `gcloud` / `firebase` / `gh` CLIs
default to the **work** account (`jairo.caroaccino@agentero.com`,
projects `ag-dev` / `producerflow…`). Before ANY cloud/Firebase/GitHub operation
for mediavida, make sure you're on the personal account, or you'll create
resources in the wrong org.

- **GitHub:** `kidandcat` (repo `kidandcat/mediavida`). If a push fails on
  permissions, run `personal` and retry.
- **gcloud / Firebase / FCM:** `kidandcat@gmail.com`.

```sh
# gcloud → personal. NEVER `gcloud config set account` — it mutates the active
# named config on disk and races parallel work/personal agents. Select the
# account per-process instead:
export CLOUDSDK_ACTIVE_CONFIG_NAME=personal   # or prefix a single command with it
gcloud config get-value account               # must print kidandcat@gmail.com

# firebase CLI keeps its own login, separate from gcloud. Verify / select:
firebase login:list
firebase login:add                      # if the personal account isn't listed (interactive)
firebase --account kidandcat@gmail.com <cmd>   # or pass it per-command
```

Never create the mediavida Firebase project, FCM config, or GCP resources under
the work account.

## Infrastructure (vps2, personal)

- **`mediavida.service`** on vps2 — the REST API / scraper backend, behind Caddy at
  `https://mediavida.jairo.cloud`. Env: `/etc/mediavida/mediavida.env`.
- **ntfy** — legacy push path, disabled (the `mediavida-ntfy` Fly app is gone). The app
  only uses FCM.
- **Firebase project `mediavida-push`** (personal account) — FCM for push.
  Service account `fcm-sender@mediavida-push…` → env var `FCM_SA_JSON`
  (+ `FCM_PROJECT_ID`). Client config (`google-services.json`,
  `firebase_options.dart`, plist) is committed — those are public client keys.
- **Gotcha:** systemd `EnvironmentFile` has no multiline values and eats backslashes in
  unquoted values. `FCM_SA_JSON` must be a **single line wrapped in single quotes**
  (compact JSON, `\n` inside `private_key` kept literal). Unquoted → `\n` becomes `n`
  and the PEM is corrupt; multiline → the value truncates to `{`. Either way
  `[fcm] disabled` / bad-service-account at startup, and since the bubbles poller is
  gated on "someone is listening" (`fcm.HasTokens`), it stops scraping entirely and no
  notification of any kind is delivered. Check with `journalctl -u mediavida | grep fcm`
  → must say `[fcm] enabled for project mediavida-push`.

## Push notifications

- Backend pushes when a user's bubble counters rise (avisos/MPs/favoritos),
  never on reads. Two paths, both fired from the bubbles poller:
  - **FCM** (`fcm.go`) — primary; reliable Android+iOS delivery with the app
    closed. Devices register their FCM token via `POST /push/register`.
  - **ntfy** (`ntfy.go`) — also published (harmless; usable via the ntfy app).
- App: `mobile/lib/core/notification_service.dart` uses `firebase_messaging`;
  Android verified E2E. **iOS still needs the APNs key uploaded to Firebase.**
