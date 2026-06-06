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
# gcloud → personal
gcloud config set account kidandcat@gmail.com

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
- Firebase project for FCM push: **personal account** (being set up).

## Push notifications

- Backend pushes when a user's bubble counters rise (avisos/MPs/favoritos),
  never on reads — see `ntfy.go` and the FCM path (in progress).
- App: `mobile/lib/core/notification_service.dart` (ntfy SSE today; FCM being
  added for reliable Android+iOS delivery with the app closed).
