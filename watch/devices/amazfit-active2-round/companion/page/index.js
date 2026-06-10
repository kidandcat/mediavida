/*
 * Companion device page (appId 1024002).
 *
 * Runs on the watch. Triggered by the recurring alarm (param `auto=1`) or by the
 * watchface tap-to-refresh (param `manual=1`). It is the only context in this
 * app with filesystem access, so it owns durable persistence:
 *   - pair.json  : { token, baseUrl }   the paired watch credentials
 *   - data.json  : { bm, bn, bf, ts }   the counters the watchface reads
 *   - alarm.json : { id }               the pending refresh alarm id
 *
 * On each run it: reads pair.json, asks the app-side for fresh bubbles (passing
 * the stored credentials, if any), persists the result, schedules the next
 * alarm, and returns home. The app-side does the localhost pairing handshake the
 * first time (no stored token); we persist whatever credentials it mints back.
 */
import { createWidget, widget, align, prop, text_style } from '@zos/ui'
import { px } from '@zos/utils'
import { back, home } from '@zos/router'
import {
  openSync,
  readSync,
  writeSync,
  closeSync,
  O_RDONLY,
  O_WRONLY,
  O_CREAT,
  O_TRUNC,
} from '@zos/fs'
import { set, cancel } from '@zos/alarm'

const SCREEN_W = px(480)
const CENTER = px(240)

const DATA_FILE = 'data.json'
const PAIR_FILE = 'pair.json'
const ALARM_FILE = 'alarm.json'
const APP_ID = 1024002
const REFRESH_SECONDS = 5 * 60

// Max retries waiting for the MessageBuilder handshake with the app-side.
const MAX_RETRIES = 15
const RETRY_DELAY = 2000

Page({
  state: {
    statusWidget: null,
  },

  build() {
    this.state.statusWidget = createWidget(widget.TEXT, {
      x: px(40),
      y: CENTER - px(60),
      w: SCREEN_W - px(80),
      h: px(120),
      text: 'Refreshing...',
      text_size: px(28),
      color: 0xff6600,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      text_style: text_style.WRAP,
    })

    this.waitAndFetch(0)
  },

  setStatus(text) {
    this.state.statusWidget &&
      this.state.statusWidget.setProperty(prop.MORE, { text })
  },

  // --- small JSON file helpers (companion sandbox) -------------------------

  readJsonFile(path, bufSize) {
    try {
      const fd = openSync({ path, flag: O_RDONLY })
      if (fd === undefined || fd < 0) return null
      const buf = new ArrayBuffer(bufSize || 512)
      readSync({ fd, buffer: buf })
      closeSync({ fd })
      const v = new Uint8Array(buf)
      let s = ''
      for (let i = 0; i < v.length; i++) {
        if (v[i] === 0) break
        s += String.fromCharCode(v[i])
      }
      if (!s) return null
      return JSON.parse(s)
    } catch (e) {
      return null
    }
  },

  writeJsonFile(path, obj) {
    try {
      const json = JSON.stringify(obj)
      const buf = new ArrayBuffer(json.length)
      const view = new Uint8Array(buf)
      for (let i = 0; i < json.length; i++) view[i] = json.charCodeAt(i)
      const fd = openSync({ path, flag: O_WRONLY | O_CREAT | O_TRUNC })
      if (fd !== undefined && fd >= 0) {
        writeSync({ fd, buffer: buf, options: { length: buf.byteLength } })
        closeSync({ fd })
        return true
      }
    } catch (e) {
      this.setStatus('Write err: ' + String(e).slice(0, 40))
    }
    return false
  },

  clearPair() {
    // Overwrite pair.json with an empty object so the next cycle re-pairs.
    this.writeJsonFile(PAIR_FILE, {})
  },

  // writeStatus persists the connection status into data.json WITHOUT clobbering
  // the last-known counters, so the watchface can render the pairing/connection
  // state ('ok' | 'needpair' | 'unauth' | 'error') even when a cycle fetched no
  // fresh data. `ts` stays the timestamp of the last *successful* fetch.
  writeStatus(st) {
    const prev = this.readJsonFile(DATA_FILE) || {}
    this.writeJsonFile(DATA_FILE, {
      bm: Number(prev.bm) || 0,
      bn: Number(prev.bn) || 0,
      bf: Number(prev.bf) || 0,
      ts: prev.ts || 0,
      st: st,
    })
  },

  // --- MessageBuilder handshake with the app-side --------------------------

  waitAndFetch(attempt) {
    let mb = null
    try {
      mb = getApp()._options.globalData.messageBuilder
    } catch (e) {
      // ignore
    }

    if (!mb && attempt < MAX_RETRIES) {
      this.setStatus('Connecting... (' + (attempt + 1) + ')')
      setTimeout(() => this.waitAndFetch(attempt + 1), RETRY_DELAY)
      return
    }

    if (!mb) {
      this.setStatus('No connection')
      this.goBack(2000)
      return
    }

    if (mb.appSidePort === 0 && attempt < MAX_RETRIES) {
      this.setStatus('Handshake... (' + (attempt + 1) + ')')
      try {
        mb.sendShake()
      } catch (e) {
        /* ignore */
      }
      setTimeout(() => this.waitAndFetch(attempt + 1), RETRY_DELAY)
      return
    }

    if (mb.appSidePort === 0) {
      this.setStatus('Handshake failed')
      this.goBack(2000)
      return
    }

    this.fetchData(mb)
  },

  fetchData(mb) {
    // Schedule the next alarm FIRST so the refresh chain never breaks, even if
    // the network step throws.
    this.scheduleNextAlarm()

    // Load stored credentials (may be null on first run / after a 401 reset).
    const creds = this.readJsonFile(PAIR_FILE)
    const hasToken = creds && creds.token && creds.baseUrl

    this.setStatus(hasToken ? 'Fetching...' : 'Pairing...')

    const req = { method: 'GET_BUBBLES' }
    if (hasToken) {
      req.token = creds.token
      req.baseUrl = creds.baseUrl
    }

    mb
      .request(req)
      .then((data) => {
        if (!data) {
          this.writeStatus('error')
          this.setStatus('No data')
          this.goBack(2000)
          return
        }

        // Token was rejected by the backend — drop it so we re-pair next cycle.
        if (data.unauthorized) {
          this.clearPair()
          this.writeStatus('unauth')
          this.setStatus('Sesion caducada,\nreabriendo emparejamiento')
          this.goBack(2500)
          return
        }

        // Phone app / pairing screen not open.
        if (data.needPair) {
          this.writeStatus('needpair')
          this.setStatus('Abre la app de Mediavida\npara emparejar')
          this.goBack(3000)
          return
        }

        // Transient fetch error — keep last data.json, just report.
        if (data.error) {
          this.writeStatus('error')
          this.setStatus('Error: ' + String(data.error).slice(0, 30))
          this.goBack(2000)
          return
        }

        // Persist freshly-minted credentials from a first-time handshake.
        if (data.paired && data.paired.token && data.paired.baseUrl) {
          this.writeJsonFile(PAIR_FILE, {
            token: data.paired.token,
            baseUrl: data.paired.baseUrl,
          })
        }

        if (data.result) {
          this.writeJsonFile(DATA_FILE, {
            bm: Number(data.result.bm) || 0,
            bn: Number(data.result.bn) || 0,
            bf: Number(data.result.bf) || 0,
            ts: Date.now(),
            st: 'ok',
          })
          this.setStatus('Updated!')
        } else {
          this.writeStatus('error')
          this.setStatus('No data')
        }
        this.goBack(2000)
      })
      .catch((err) => {
        this.writeStatus('error')
        this.setStatus('Error: ' + String(err).slice(0, 30))
        this.goBack(2000)
      })
  },

  scheduleNextAlarm() {
    try {
      // Cancel the previous alarm if we persisted one.
      const old = this.readJsonFile(ALARM_FILE, 64)
      if (old && old.id && old.id > 0) {
        try {
          cancel(old.id)
        } catch (e) {
          /* no previous alarm */
        }
      }

      // Schedule the next refresh and persist its id.
      const alarmId = set({
        appId: APP_ID,
        url: 'page/index',
        delay: REFRESH_SECONDS,
        param: 'auto=1',
      })
      this.writeJsonFile(ALARM_FILE, { id: alarmId })
    } catch (e) {
      // alarm scheduling failed; will retry on the next run
    }
  },

  goBack(delay) {
    setTimeout(() => {
      try {
        home()
      } catch (e) {
        try {
          back()
        } catch (e2) {
          /* ignore */
        }
      }
    }, delay)
  },
})
