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

## Infrastructure (Fly.io, personal)

- **`mediavida-api`** — the REST API / scraper backend.
- **`mediavida-ntfy`** — self-hosted ntfy for push (see `deploy/ntfy/README.md`).
- **Firebase project `mediavida-push`** (personal account) — FCM for push.
  Service account `fcm-sender@mediavida-push…` → key in `mediavida-api` secret
  `FCM_SA_JSON` (+ `FCM_PROJECT_ID`). Client config (`google-services.json`,
  `firebase_options.dart`, plist) is committed — those are public client keys.

## Push notifications

- Backend pushes when a user's bubble counters rise (avisos/MPs/favoritos),
  never on reads. Two paths, both fired from the bubbles poller:
  - **FCM** (`fcm.go`) — primary; reliable Android+iOS delivery with the app
    closed. Devices register their FCM token via `POST /push/register`.
  - **ntfy** (`ntfy.go`) — also published (harmless; usable via the ntfy app).
- App: `mobile/lib/core/notification_service.dart` uses `firebase_messaging`;
  Android verified E2E. **iOS still needs the APNs key uploaded to Firebase.**
