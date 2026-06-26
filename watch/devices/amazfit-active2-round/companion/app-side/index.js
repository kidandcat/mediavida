/*
 * Companion app-side service (appId 1024002).
 *
 * A Zepp OS watchface cannot run a side service or do networking, so all of the
 * Mediavida networking lives here, in the companion mini-app's app-side service
 * (runs on the phone, has a global `fetch`). The device page drives it over
 * MessageBuilder.
 *
 * Persistence note: only the device page has filesystem access in this app, so
 * THIS service is stateless across wakeups. The page owns the durable
 * credentials file (pair.json) and hands the token/base_url to us on each
 * request; when we pair, we hand the freshly-minted credentials back for the
 * page to persist. This mirrors how the old companion let the page write
 * data.json.
 *
 * Request protocol (page -> app-side), payload `{ method: 'GET_BUBBLES', ... }`:
 *   - No stored token yet (page sends no token):
 *       we run the localhost pairing handshake, then fetch bubbles.
 *       response: { result: {bm,bn,bf}, paired: {token, baseUrl} }   (page persists `paired`)
 *       or, if the phone app isn't open: { needPair: true }          (page shows "Abre la app…")
 *   - Stored token present (page sends { token, baseUrl }):
 *       we fetch bubbles directly.
 *       response: { result: {bm,bn,bf} }
 *       or, on HTTP 401: { unauthorized: true }                      (page clears pair.json)
 *       or, on transient error: { error: '<msg>' }                  (page keeps last data)
 */
import { MessageBuilder } from '../shared/message-side'
import { pair, fetchBubbles } from '../shared/bubbles'

const messageBuilder = new MessageBuilder()

// Run the localhost handshake and fetch with the freshly-minted credentials.
// `oldToken` (if any) is handed to the phone so the backend revokes it.
function pairAndFetch(oldToken) {
  return pair(oldToken).then((creds) => {
    // The token is already minted (and `oldToken` revoked) on the backend by
    // the time this resolves. So we MUST hand `creds` back for the page to
    // persist regardless of what the first fetch below does — if we only
    // returned `paired` on a successful fetch, a transient post-pair fetch
    // failure (BLE blip) would make the watch DISCARD a valid token and re-pair
    // next cycle, minting yet another token while the undelivered one lingers
    // as a zombie in the paired-watches list (the "pairs as a new watch / now
    // two appear" bug).
    return fetchBubbles(creds.token, creds.baseUrl)
      .then((data) => {
        if (data && data.unauthorized) {
          // Brand-new token already rejected — surface as needing re-pair.
          return { needPair: true }
        }
        return { result: data, paired: creds }
      })
      .catch((err) => {
        console.log('post-pair fetch failed (keeping minted token): ' + err)
        return { paired: creds }
      })
  })
}

// Resolve bubbles for a request, pairing first if needed. Always resolves
// (never rejects) with a plain object the page can act on.
function handleGetBubbles(payload) {
  const token = payload && payload.token
  const baseUrl = payload && payload.baseUrl

  if (token && baseUrl) {
    // Autonomous path: we already have credentials.
    return fetchBubbles(token, baseUrl)
      .then((data) => {
        if (data && data.unauthorized) {
          // Token rejected. SELF-HEAL: try a silent re-pair right now (works
          // whenever the Mediavida app is foregrounded — its loopback server is
          // ambient). On success the page atomically replaces its credentials;
          // on failure it KEEPS the old token (the 401 may be transient) and
          // just reports the state. Never destroy credentials on a 401.
          return pairAndFetch(token).catch((err) => {
            console.log('re-pair after 401 failed: ' + err)
            return { unauthorized: true }
          })
        }
        return { result: data }
      })
      .catch((err) => {
        console.log('bubbles fetch error: ' + err)
        return { error: String(err).slice(0, 60) }
      })
  }

  // No credentials yet (first run): localhost handshake, then fetch.
  return pairAndFetch(null).catch((err) => {
    // Most common cause: phone app not open.
    console.log('pair handshake failed: ' + err)
    return { needPair: true }
  })
}

AppSideService({
  onInit() {
    messageBuilder.listen(() => {})

    messageBuilder.on('request', (ctx) => {
      const payload = messageBuilder.buf2Json(ctx.request.payload)

      if (payload && payload.method === 'GET_BUBBLES') {
        handleGetBubbles(payload).then((data) => {
          ctx.response({ data })
        })
      }
    })
  },

  onRun() {},

  onDestroy() {},
})
