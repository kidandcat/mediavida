#!/usr/bin/env bash
#
# sync-shared.sh — copy the single source-of-truth shared/ modules into each
# Zepp app folder before `zeus build`.
#
# Why: Zeus builds per-app-directory and cannot import JS from outside the app
# folder (no monorepo workspaces). So the canonical shared code lives once at
# watch/shared/, and this script mirrors it into every app's own shared/ dir.
# Those per-app shared/ dirs are GENERATED and gitignored (see watch/.gitignore)
# — never edit them by hand; edit watch/shared/ and re-run this script.
#
# Usage:
#   ./sync-shared.sh            # sync all device apps
#   ./sync-shared.sh <appdir>   # sync a single app dir (e.g. devices/foo/companion)
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
SRC="$ROOT/shared"

if [[ ! -d "$SRC" ]]; then
  echo "error: shared source not found at $SRC" >&2
  exit 1
fi

sync_one() {
  local appdir="$1"
  # An app dir is one that has an app.json (watchface or companion).
  if [[ ! -f "$appdir/app.json" ]]; then
    return 0
  fi
  local dest="$appdir/shared"
  rm -rf "$dest"
  mkdir -p "$dest"
  cp "$SRC"/*.js "$dest"/
  echo "synced shared -> ${appdir#$ROOT/}/shared"
}

if [[ $# -ge 1 ]]; then
  sync_one "$ROOT/$1"
  exit 0
fi

# Default: every app dir under devices/*/*
for appdir in "$ROOT"/devices/*/*; do
  [[ -d "$appdir" ]] && sync_one "$appdir"
done
