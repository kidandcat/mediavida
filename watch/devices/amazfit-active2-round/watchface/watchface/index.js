import { createWidget, widget, align, prop, show_level } from '@zos/ui'
import { px } from '@zos/utils'
import { openSync, readSync, closeSync, O_RDONLY } from '@zos/fs'
import { set, cancel } from '@zos/alarm'
import { launchApp } from '@zos/router'

const SCREEN_W = px(480)
const CENTER = px(240)

// How often to re-read the shared file (30 seconds)
const READ_INTERVAL = 30 * 1000
const COMPANION_APP_ID = 1024002
const DATA_FILE = 'data.json'
const REFRESH_MINUTES = 5

WatchFace({
  state: {
    bm: '--',
    bn: '--',
    bf: '--',
    widgets: {},
    readTimer: null,
    alarmId: null,
  },

  build() {
    // Dial background (numbers + tick marks) - visible in normal + AOD
    createWidget(widget.IMG, {
      x: 0,
      y: 0,
      src: 'dial.png',
      show_level: show_level.ONLY_NORMAL | show_level.ONAL_AOD,
    })

    // MV logo at center-top area. Doubles as the pairing indicator: colored when
    // paired & data is fresh, desaturated (gray) when pairing is needed.
    this.state.widgets.logo = createWidget(widget.IMG, {
      x: CENTER - px(28),
      y: px(140),
      src: 'mv_logo.png',
      show_level: show_level.ONLY_NORMAL | show_level.ONAL_AOD,
    })

    // Analog clock hands (normal mode)
    createWidget(widget.TIME_POINTER, {
      hour_centerX: CENTER,
      hour_centerY: CENTER,
      hour_posX: 5,
      hour_posY: 120,
      hour_path: 'hour_hand.png',

      minute_centerX: CENTER,
      minute_centerY: CENTER,
      minute_posX: 4,
      minute_posY: 170,
      minute_path: 'minute_hand.png',

      show_level: show_level.ONLY_NORMAL,
    })

    // Analog clock hands (AOD mode)
    createWidget(widget.TIME_POINTER, {
      hour_centerX: CENTER,
      hour_centerY: CENTER,
      hour_posX: 5,
      hour_posY: 120,
      hour_path: 'hour_hand.png',

      minute_centerX: CENTER,
      minute_centerY: CENTER,
      minute_posX: 4,
      minute_posY: 170,
      minute_path: 'minute_hand.png',

      show_level: show_level.ONAL_AOD,
    })

    // Center dot
    createWidget(widget.IMG, {
      x: CENTER - px(7),
      y: CENTER - px(7),
      src: 'center_dot.png',
      show_level: show_level.ONLY_NORMAL,
    })

    // Notification icons + counters (centered, visible in normal + AOD)
    // Order: exclamation (bn), star (bf), envelope (bm)
    const BOTH = show_level.ONLY_NORMAL | show_level.ONAL_AOD

    // Notifications (exclamation circle) - LEFT
    this.state.widgets.bnIcon = createWidget(widget.IMG, {
      x: px(136),
      y: px(330),
      src: 'icon_notif_gray.png',
      show_level: BOTH,
    })
    this.state.widgets.bn = createWidget(widget.TEXT, {
      x: px(122),
      y: px(360),
      w: px(56),
      h: px(24),
      text: '',
      text_size: px(18),
      color: 0xff3232,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      show_level: BOTH,
    })

    // Favorites (star) - CENTER
    this.state.widgets.bfIcon = createWidget(widget.IMG, {
      x: px(226),
      y: px(330),
      src: 'icon_fav_gray.png',
      show_level: BOTH,
    })
    this.state.widgets.bf = createWidget(widget.TEXT, {
      x: px(212),
      y: px(360),
      w: px(56),
      h: px(24),
      text: '',
      text_size: px(18),
      color: 0xff3232,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      show_level: BOTH,
    })

    // Messages (envelope) - RIGHT
    this.state.widgets.bmIcon = createWidget(widget.IMG, {
      x: px(316),
      y: px(330),
      src: 'icon_msg_gray.png',
      show_level: BOTH,
    })
    this.state.widgets.bm = createWidget(widget.TEXT, {
      x: px(302),
      y: px(360),
      w: px(56),
      h: px(24),
      text: '',
      text_size: px(18),
      color: 0xff3232,
      align_h: align.CENTER_H,
      align_v: align.CENTER_V,
      show_level: BOTH,
    })

    // Resume/pause lifecycle
    createWidget(widget.WIDGET_DELEGATE, {
      resume_call: () => {
        this.readData()
      },
      pause_call: () => {},
    })

    // Initial read from shared file
    this.readData()

    // Set recurring alarm to launch companion app for data refresh
    this.scheduleRefresh()

    // Periodically re-read the shared file
    this.state.readTimer = setInterval(() => this.readData(), READ_INTERVAL)

    // Invisible clickable area over logo - tap to refresh
    createWidget(widget.BUTTON, {
      x: CENTER - px(28),
      y: px(140),
      w: px(56),
      h: px(56),
      text: '',
      normal_src: 'transparent.png',
      press_src: 'transparent.png',
      click_func: () => {
        launchApp({ appId: COMPANION_APP_ID, url: 'page/index', param: 'manual=1' })
      },
      show_level: show_level.ONLY_NORMAL,
    })
  },

  readData() {
    try {
      const fd = openSync({
        path: DATA_FILE,
        flag: O_RDONLY,
        options: { appId: COMPANION_APP_ID },
      })

      if (fd === undefined || fd === null || fd < 0) {
        // No data file yet → companion has never run / never paired.
        this.renderStatus('unpaired', 0)
        return
      }

      const buffer = new ArrayBuffer(512)
      readSync({ fd, buffer })
      closeSync({ fd })

      const view = new Uint8Array(buffer)
      let json = ''
      for (let i = 0; i < view.length; i++) {
        if (view[i] === 0) break
        json += String.fromCharCode(view[i])
      }

      if (!json) {
        this.renderStatus('unpaired', 0)
        return
      }

      const data = JSON.parse(json)

      if (data) {
        this.state.bm = data.bm !== undefined ? String(data.bm) : '0'
        this.state.bn = data.bn !== undefined ? String(data.bn) : '0'
        this.state.bf = data.bf !== undefined ? String(data.bf) : '0'
        this.updateWidgets()
        // `st` is written by the companion on every cycle; older files without
        // it but with counters mean a successful fetch → treat as 'ok'.
        this.renderStatus(data.st || 'ok', data.ts || 0)
      }
    } catch (e) {
      this.renderStatus('error', 0)
    }

  },

  // renderStatus uses the MV logo itself as the pairing indicator: colored when
  // everything is fine ('ok'), desaturated (gray) whenever pairing is needed —
  // any non-ok state ('needpair' | 'unpaired' | 'unauth' | 'error').
  renderStatus(st) {
    const src = st === 'ok' ? 'mv_logo.png' : 'mv_logo_gray.png'
    this.state.widgets.logo &&
      this.state.widgets.logo.setProperty(prop.MORE, { src })
  },

  updateWidgets() {
    const { widgets, bm, bn, bf } = this.state
    const bmN = Number(bm) || 0
    const bnN = Number(bn) || 0
    const bfN = Number(bf) || 0

    widgets.bm && widgets.bm.setProperty(prop.MORE, { text: bmN > 0 ? String(bmN) : '' })
    widgets.bmIcon && widgets.bmIcon.setProperty(prop.MORE, { src: bmN > 0 ? 'icon_msg_active.png' : 'icon_msg_gray.png' })

    widgets.bn && widgets.bn.setProperty(prop.MORE, { text: bnN > 0 ? String(bnN) : '' })
    widgets.bnIcon && widgets.bnIcon.setProperty(prop.MORE, { src: bnN > 0 ? 'icon_notif_active.png' : 'icon_notif_gray.png' })

    widgets.bf && widgets.bf.setProperty(prop.MORE, { text: bfN > 0 ? String(bfN) : '' })
    widgets.bfIcon && widgets.bfIcon.setProperty(prop.MORE, { src: bfN > 0 ? 'icon_fav_active.png' : 'icon_fav_gray.png' })
  },

  scheduleRefresh() {
    try {
      // Cancel previous alarm if any
      if (this.state.alarmId !== null) {
        try { cancel(this.state.alarmId) } catch (e) { /* ignore */ }
      }
      // Set alarm to open companion app in REFRESH_MINUTES
      // Try both appId (v3 API) and appid (legacy)
      this.state.alarmId = set({
        appId: COMPANION_APP_ID,
        appid: COMPANION_APP_ID,
        url: 'page/index',
        delay: REFRESH_MINUTES * 60,
        param: 'auto=1',
      })
      // alarm set successfully
    } catch (e) {
      // alarm setup failed, will retry on next build
    }
  },

  onDestroy() {
    if (this.state.readTimer) {
      clearInterval(this.state.readTimer)
      this.state.readTimer = null
    }
    if (this.state.alarmId !== null) {
      try { cancel(this.state.alarmId) } catch (e) { /* ignore */ }
    }
  },
})
