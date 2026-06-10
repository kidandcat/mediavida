/*
 * bubbles.js — device-agnostic Mediavida data layer for the companion mini-app.
 *
 * This module runs inside the companion's app-side service (Zepp OS side
 * service: has a global `fetch`, runs on the phone). It owns the whole data
 * pipeline: the one-time localhost pairing handshake with the Mediavida Flutter
 * app, and the autonomous authenticated fetch of the bubble counters.
 *
 * It is intentionally free of any watch-model-specific code, so a new device
 * just reuses it as-is. The companion's app-side wires it up; the device page
 * persists the resulting credentials/data to the companion sandbox (the page is
 * the only context with filesystem access, mirroring how the old companion
 * wrote data.json).
 *
 * Contract constants live in CONTRACT.md — keep them in sync.
 */

// --- Pairing handshake (one-time, localhost, phone-app foreground) ---------

// Loopback bridge the Mediavida Flutter app exposes while its pairing screen is
// open. See mobile/lib/core/watch_pairing_server.dart.
export const PAIR_URL = 'http://127.0.0.1:28590/pair'

// Non-secret gate the phone app checks before minting a token. Not a secret:
// it only stops random localhost callers, the real auth is the minted token.
export const WATCH_APP_KEY = '70f54fb484323ae9c7fcaff542bcfda8'

// --- Backend bubbles endpoint ---------------------------------------------

// Path appended to the base_url returned by the handshake.
export const BUBBLES_PATH = '/bubbles'

/**
 * Perform the localhost pairing handshake.
 *
 * Resolves with `{ token, baseUrl }` on success. Rejects when the phone app is
 * not reachable (pairing screen not open) or returns a non-2xx status. The
 * companion should surface "Abre la app de Mediavida para emparejar" on reject
 * and retry on the next cycle.
 */
export function pair() {
  return fetch({
    url: PAIR_URL,
    method: 'GET',
    headers: { 'X-Watch-App-Key': WATCH_APP_KEY },
  }).then((res) => {
    const status = res.status || 200
    if (status < 200 || status >= 300) {
      throw new Error('pair status ' + status)
    }
    const body = typeof res.body === 'string' ? JSON.parse(res.body) : res.body
    if (!body || !body.token || !body.base_url) {
      throw new Error('pair: malformed response')
    }
    return { token: body.token, baseUrl: body.base_url }
  })
}

/**
 * Fetch the bubble counters from the backend using a paired watch token.
 *
 * The backend responds `{ messages, notifications, favorites }`. We map those to
 * the watchface's historic field names `{ bm, bn, bf }` here, so the watchface
 * rendering code stays unchanged.
 *
 * On HTTP 401 the token is no longer valid (revoked / session gone): we surface
 * a `{ unauthorized: true }` marker so the caller can clear the stored token and
 * re-pair next cycle. Other failures reject so the caller keeps the last data.
 */
export function fetchBubbles(token, baseUrl) {
  return fetch({
    url: baseUrl + BUBBLES_PATH,
    method: 'GET',
    headers: { Authorization: 'Bearer ' + token },
  }).then((res) => {
    const status = res.status || 200
    if (status === 401) {
      return { unauthorized: true }
    }
    if (status < 200 || status >= 300) {
      throw new Error('bubbles status ' + status)
    }
    const body = typeof res.body === 'string' ? JSON.parse(res.body) : res.body
    if (!body || body.messages === undefined) {
      throw new Error('bubbles: malformed response')
    }
    // Map backend names -> watchface names.
    return {
      bm: Number(body.messages) || 0,
      bn: Number(body.notifications) || 0,
      bf: Number(body.favorites) || 0,
    }
  })
}
