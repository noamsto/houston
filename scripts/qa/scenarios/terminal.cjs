// Area 5: the run Terminal tab. Shell panes (WT_PANE, LESS_PANE) carry the
// keystroke, gesture and reconnect checks; only `choices` needs the real claude.
// Touch gestures are real touch events through CDP Input.dispatchTouchEvent.
const fs = require('node:fs')
const path = require('node:path')

const sh = require('./_shared.cjs')

const AREA = 5

function skipOut(reason) {
  const e = new Error(reason)
  e.skip = true
  throw e
}

async function step(ctx, id, fn) {
  try {
    const r = await fn()
    const note = r && typeof r === 'object' ? r.note : r
    ctx.check(AREA, id, 'pass', typeof note === 'string' ? note : '', r && typeof r === 'object' ? r.shot : '')
  } catch (e) {
    ctx.check(AREA, id, e && e.skip ? 'skip' : 'fail', e instanceof Error ? e.message : String(e), (e && e.shot) || '')
  }
}

async function runOf(ctx, pane) {
  const runs = (await ctx.api('/api/runs')).json
  return runs.find((r) => r.tmux && r.tmux.pane_id === pane)
}

const typeInto = (ctx, pane, text) => ctx.tmux('send-keys', '-t', pane, '-l', text)
const press = (ctx, pane, ...keys) => ctx.tmux('send-keys', '-t', pane, ...keys)
const paneLines = (ctx, pane) => ctx.capture(pane).split('\n').map((l) => l.replace(/\s+$/, ''))
const lastNonEmpty = (ctx, pane) => paneLines(ctx, pane).filter(Boolean).pop() ?? ''

const termText = (page) => page.evaluate(() => document.querySelector('.xterm-rows')?.innerText ?? '')

// Tracks every terminal websocket of a page with its frames.
function trackSockets(page) {
  const socks = []
  page.on('websocket', (ws) => {
    if (!/\/terminal/.test(ws.url())) return
    const s = { url: ws.url(), received: [], sent: [], closed: false }
    ws.on('framereceived', (f) => s.received.push(String(f.payload)))
    ws.on('framesent', (f) => s.sent.push(String(f.payload)))
    ws.on('close', () => {
      s.closed = true
    })
    socks.push(s)
  })
  return socks
}

async function touch(cdp, type, points) {
  await cdp.send('Input.dispatchTouchEvent', { type, touchPoints: points.map((p, i) => ({ x: p.x, y: p.y, id: i })) })
}

async function drag(cdp, from, to, steps = 14, delay = 20) {
  await touch(cdp, 'touchStart', [from])
  for (let i = 1; i <= steps; i++) {
    await touch(cdp, 'touchMove', [{ x: from.x + ((to.x - from.x) * i) / steps, y: from.y + ((to.y - from.y) * i) / steps }])
    await new Promise((r) => setTimeout(r, delay))
  }
  await touch(cdp, 'touchEnd', [])
}

const transformOf = (page) =>
  page.evaluate(() => {
    const t = document.querySelector('.xterm')?.parentElement?.style.transform ?? ''
    const m = /translate\((-?[\d.]+)px, (-?[\d.]+)px\) scale\(([\d.]+)\)/.exec(t)
    return m ? { tx: +m[1], ty: +m[2], scale: +m[3] } : null
  })

const screenSize = (page) =>
  page.evaluate(() => {
    const s = document.querySelector('.xterm-screen')
    return s ? { w: s.offsetWidth, h: s.offsetHeight } : null
  })

exports.run = async (ctx) => {
  const wt = ctx.env.WT_PANE ? await runOf(ctx, ctx.env.WT_PANE) : null
  const lessRun = ctx.env.LESS_PANE ? await runOf(ctx, ctx.env.LESS_PANE) : null
  const claude = ctx.env.CLAUDE_PANE ? await runOf(ctx, ctx.env.CLAUDE_PANE) : null
  const all = ['seed', 'live-output', 'quick-keys', 'composer', 'pan', 'pinch', 'alt-screen', 'resize', 'reconnect-hidden', 'choices']
  if (!wt || !lessRun) {
    for (const id of all) ctx.check(AREA, id, 'skip', 'agent: harness has no WT_PANE/LESS_PANE shell windows')
    return
  }
  const WT = ctx.env.WT_PANE
  const LESS = ctx.env.LESS_PANE
  const tag = Date.now().toString(36).slice(-5).toUpperCase()
  const termUrl = (r) => ctx.url(`#/fleet/${r.id}/terminal`)

  // ---------------- WT pane page: seed, live-output, quick-keys, composer, pan ----------------
  const { context, page } = await ctx.newPage(ctx.browser, 'mobile')
  const socks = trackSockets(page)
  const keyPosts = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && /\/input$/.test(r.url())) keyPosts.push(r.postData() ?? '')
  })
  const cdp = await context.newCDPSession(page)
  try {
    press(ctx, WT, 'C-c')
    press(ctx, WT, 'C-u')
    typeInto(ctx, WT, 'clear')
    press(ctx, WT, 'Enter')
    await ctx.sleep(300)
    ctx.tmux('clear-history', '-t', WT)
    const seedMarker = `QA-SEED-${tag}`
    typeInto(ctx, WT, `echo ${seedMarker}`)
    press(ctx, WT, 'Enter')
    await ctx.waitFor(() => ctx.capture(WT).includes(seedMarker), { timeout: 5000, message: 'shell never echoed the seed marker' })

    await step(ctx, 'seed', async () => {
      await page.goto(termUrl(wt))
      await page.waitForSelector('.xterm-rows', { timeout: 15000 })
      await ctx.waitFor(async () => (await termText(page)).includes(seedMarker), { timeout: 10000, message: `terminal never showed ${seedMarker}; shows ${JSON.stringify((await termText(page)).slice(0, 120))}` })
      const first = socks[0]?.received.find((m) => /"type":"seed"/.test(m))
      return { note: `marker ${seedMarker} (echoed before opening) is in the terminal; seed frame ${first ? 'received' : 'missing'}`, shot: await ctx.shot(page, 'terminal-seed') }
    })

    await step(ctx, 'live-output', async () => {
      const live = `QA-LIVE-${tag}`
      typeInto(ctx, WT, `echo ${live}`)
      press(ctx, WT, 'Enter')
      const t0 = Date.now()
      await ctx.waitFor(async () => (await termText(page)).includes(live), { timeout: 3000, interval: 50, message: `${live} not in the terminal within 3 s of being echoed` })
      return { note: `second marker appeared after ${Date.now() - t0} ms`, shot: await ctx.shot(page, 'terminal-live-output') }
    })

    await step(ctx, 'quick-keys', async () => {
      const bar = page.locator('[data-testid="quick-keys"]')
      const tap = (re) => bar.locator('button', { hasText: re }).first().click()
      const notes = []

      // `cat -vT` shows what reached the tty: Esc as ^[, Tab as ^I, and nothing
      // is submitted until a newline arrives, so no tap here may add an Enter.
      press(ctx, WT, 'C-c')
      await ctx.sleep(200)
      press(ctx, WT, 'C-u')
      typeInto(ctx, WT, 'cat -vT')
      press(ctx, WT, 'Enter')
      await ctx.sleep(500)
      keyPosts.length = 0
      await tap(/^Esc$/)
      await tap(/^Tab$/)
      await tap(/^1$/)
      await ctx.waitFor(() => ['Escape', 'Tab', '1'].every((k) => keyPosts.some((p) => p.includes(`"key":"${k}"`))), { timeout: 5000, message: `key requests seen: ${keyPosts.join(' ')}` })
      await ctx.sleep(400)
      if (ctx.capture(WT).includes('^[^I1')) throw new Error('keys were already submitted without an Enter')
      press(ctx, WT, 'Enter')
      await ctx.waitFor(() => paneLines(ctx, WT).includes('^[^I1'), { timeout: 5000, message: `Esc, Tab, 1 did not arrive as ^[ ^I 1; pane tail ${JSON.stringify(paneLines(ctx, WT).filter(Boolean).slice(-3))}` })
      notes.push('Esc, Tab and 1 reached the tty as ^[ ^I 1 with no implicit Enter')

      // ^C asks first; Cancel sends nothing, Confirm sends C-c.
      keyPosts.length = 0
      await tap(/^\^C$/)
      const group = page.locator('[role="group"][aria-label="Confirm ^C"]')
      await group.waitFor({ timeout: 5000 })
      const shot = await ctx.shot(page, 'terminal-quick-keys-confirm')
      await group.locator('button', { hasText: 'Cancel' }).click()
      await ctx.sleep(500)
      if (keyPosts.some((p) => /C-c/.test(p))) throw new Error('Cancel still sent C-c')
      if (/qa\$\s*$/.test(lastNonEmpty(ctx, WT))) throw new Error('cat ended before ^C was confirmed')
      await tap(/^\^C$/)
      await group.locator('button', { hasText: 'Confirm' }).click()
      await ctx.waitFor(() => /qa\$\s*$/.test(lastNonEmpty(ctx, WT)), { timeout: 8000, message: `^C did not interrupt cat; last line "${lastNonEmpty(ctx, WT)}"` })
      if (!ctx.capture(WT).includes('^C')) throw new Error('pane shows no ^C echo')
      notes.push('^C asked Confirm/Cancel; Cancel sent nothing; Confirm interrupted cat')
      return { note: notes.join('; '), shot }
    })

    await step(ctx, 'composer', async () => {
      const box = page.locator('.mobile-input-bar textarea')
      const send = page.locator('.mobile-input-bar button[aria-label="Send"]')
      press(ctx, WT, 'C-c')
      press(ctx, WT, 'C-u')
      const out = `QA-COMPOSER-${tag}`
      await box.fill(`echo ${out}`)
      await send.click()
      await ctx.waitFor(() => paneLines(ctx, WT).includes(out), { timeout: 8000, message: `pane never ran the composed command (output line "${out}" absent)` })
      await ctx.waitFor(async () => (await box.inputValue()) === '', { timeout: 5000, message: 'composer kept its text after a successful send' })
      const prompts = () => ctx.capture(WT, { lines: 300 }).split('\n').filter((l) => l.replace(/\s+$/, '') === 'qa$').length
      const promptsBefore = prompts()
      await send.click()
      await ctx.waitFor(() => prompts() > promptsBefore, { timeout: 5000, message: () => `empty Send did not press Enter: bare "qa$" prompt lines stayed at ${promptsBefore}` })
      // Multi-line draft: Enter is a newline, only Send submits.
      await box.click()
      await page.keyboard.type('line1')
      await page.keyboard.press('Enter')
      await page.keyboard.type('line2')
      const v = await box.inputValue()
      if (v !== 'line1\nline2') throw new Error(`Enter did not insert a newline: ${JSON.stringify(v)}`)
      await box.fill('')
      return { note: `text + Enter ran in the pane ("${out}"), field cleared, empty Send pressed Enter, Enter in the field is a newline`, shot: await ctx.shot(page, 'terminal-composer') }
    })

    await step(ctx, 'pan', async () => {
      await page.evaluate(() => document.querySelector('.xterm-screen').scrollIntoView())
      const rect = await page.evaluate(() => {
        const r = document.querySelector('.xterm-screen').getBoundingClientRect()
        return { left: r.left, top: r.top, w: r.width, h: r.height }
      })
      const before = await transformOf(page)
      if (!before) throw new Error('terminal has no transform to read')
      if (await page.$('button:has-text("↓ Live")')) throw new Error('Live pill already showing before any pan')
      const y = rect.top + 120
      await drag(cdp, { x: 320, y }, { x: 120, y }, 14)
      await ctx.sleep(300)
      const after = await transformOf(page)
      if (!(after.tx < before.tx - 50)) throw new Error(`one-finger horizontal drag moved translateX ${before.tx} -> ${after.tx}`)
      await page.waitForSelector('button:has-text("↓ Live")', { timeout: 3000 })
      const shot = await ctx.shot(page, 'terminal-pan-detached')
      await page.click('button:has-text("↓ Live")')
      await page.waitForSelector('button:has-text("↓ Live")', { state: 'detached', timeout: 3000 })
      await ctx.sleep(400)
      const reattached = await transformOf(page)
      return { note: `translateX ${before.tx} -> ${after.tx} after a ${200}px drag, "↓ Live" appeared, tapping it removed the pill (translateX ${reattached.tx})`, shot }
    })
  } finally {
    await context.close()
  }

  // ---------------- pinch (own page: it changes the persisted font size) ----------------
  {
    const { context: c2, page: p2 } = await ctx.newPage(ctx.browser, 'mobile')
    const cdp2 = await c2.newCDPSession(p2)
    try {
      await step(ctx, 'pinch', async () => {
        await p2.goto(termUrl(wt))
        await p2.waitForSelector('.xterm-rows', { timeout: 15000 })
        await ctx.sleep(1000)
        const rect = await p2.evaluate(() => {
          const r = document.querySelector('.xterm-screen').getBoundingClientRect()
          return { left: r.left, top: r.top }
        })
        const y = rect.top + 150
        const before = await screenSize(p2)
        const t0 = await transformOf(p2)
        await touch(cdp2, 'touchStart', [{ x: 160, y }, { x: 230, y }])
        for (let i = 1; i <= 12; i++) {
          await touch(cdp2, 'touchMove', [{ x: 160 - i * 8, y }, { x: 230 + i * 8, y }])
          await ctx.sleep(20)
        }
        const mid = await transformOf(p2)
        await touch(cdp2, 'touchEnd', [])
        await ctx.sleep(600)
        const after = await screenSize(p2)
        if (!(mid.scale > t0.scale * 1.2)) throw new Error(`pinch-out did not scale the view: ${t0.scale} -> ${mid.scale} mid-gesture`)
        if (!(after.w > before.w * 1.15)) throw new Error(`font size did not grow on pinch end: screen ${before.w}px -> ${after.w}px`)
        return { note: `scale ${t0.scale} -> ${mid.scale} mid-gesture; on release snapped to a real font size: screen ${before.w}px -> ${after.w}px wide`, shot: await ctx.shot(p2, 'terminal-pinch') }
      })
    } finally {
      await c2.close()
    }
  }

  // ---------------- alt-screen: less on a 200-line file ----------------
  {
    const file = path.join(ctx.S, 'qa-200.txt')
    fs.writeFileSync(file, Array.from({ length: 200 }, (_, i) => `qa-line-${String(i + 1).padStart(3, '0')}`).join('\n') + '\n')
    const { context: c3, page: p3 } = await ctx.newPage(ctx.browser, 'mobile')
    const socks3 = trackSockets(p3)
    const cdp3 = await c3.newCDPSession(p3)
    try {
      await step(ctx, 'alt-screen', async () => {
        press(ctx, LESS, 'C-u')
        typeInto(ctx, LESS, `less ${file}`)
        press(ctx, LESS, 'Enter')
        await ctx.waitFor(() => ctx.capture(LESS).includes('qa-line-001') && ctx.capture(LESS).includes('(END)') === false, { timeout: 8000, message: 'less did not start' })
        await p3.goto(termUrl(lessRun))
        await p3.waitForSelector('.xterm-rows', { timeout: 15000 })
        await ctx.waitFor(() => socks3.some((s) => s.received.some((m) => /"alternate_on":true/.test(m))), { timeout: 8000, message: 'meta never reported alternate_on' })
        const modeMeta = socks3.flatMap((s) => s.received).filter((m) => /"type":"meta"/.test(m)).pop()
        const mouseOn = /"mouse_on":true/.test(modeMeta)
        await ctx.waitFor(async () => (await termText(p3)).includes('qa-line-001'), { timeout: 8000, message: 'terminal does not show less' })
        const rect = await p3.evaluate(() => {
          const r = document.querySelector('.xterm-screen').getBoundingClientRect()
          return { top: r.top, h: r.height }
        })
        const x = 200
        const y0 = rect.top + rect.h - 40
        // 3 pages' worth of upward drag (160 px per PageDown without mouse mode).
        await drag(cdp3, { x, y: y0 }, { x, y: y0 - 480 }, 20, 15)
        await ctx.sleep(1200)
        const cap = ctx.capture(LESS)
        const nums = [...cap.matchAll(/qa-line-(\d{3})/g)].map((m) => +m[1])
        if (!nums.length) throw new Error(`less shows no qa-line after the drag: ${JSON.stringify(cap.slice(0, 100))}`)
        if (Math.min(...nums) < 30) throw new Error(`pane still near the top after dragging up 480px: first visible line ${Math.min(...nums)}`)
        const sentKeys = socks3.flatMap((s) => s.sent).filter((m) => (mouseOn ? /\\u001b\[<65;/ : /\\u001b\[6~/).test(m))
        if (!sentKeys.length) throw new Error(`no ${mouseOn ? 'SGR wheel' : 'PageDown'} input frame observed over the websocket`)
        const shot = await ctx.shot(p3, 'terminal-alt-screen')
        return { note: `alternate_on true, mouse_on ${mouseOn}; ${sentKeys.length} ${mouseOn ? 'wheel reports' : 'PageDown frames'} sent; less now shows lines ${Math.min(...nums)}-${Math.max(...nums)}`, shot }
      })
    } finally {
      press(ctx, LESS, 'q')
      await c3.close()
    }
  }

  // ---------------- resize (desktop) ----------------
  {
    const { context: c4, page: p4 } = await ctx.newPage(ctx.browser, 'desktop')
    const socks4 = trackSockets(p4)
    try {
      await step(ctx, 'resize', async () => {
        press(ctx, WT, 'C-u')
        const m = `QA-RESIZE-${tag}`
        typeInto(ctx, WT, `echo ${m}`)
        press(ctx, WT, 'Enter')
        await p4.goto(termUrl(wt))
        await p4.waitForSelector('.xterm-rows', { timeout: 15000 })
        await ctx.waitFor(async () => (await termText(p4)).includes(m), { timeout: 10000, message: 'desktop terminal never showed the marker' })
        const size = () => ctx.tmux('display', '-p', '-t', WT, '#{pane_width}x#{pane_height}').trim()
        const paneBefore = size()
        const dimsBefore = socks4[0].received.filter((x) => /"type":"dims"/.test(x)).length
        await p4.setViewportSize({ width: 900, height: 600 })
        await ctx.sleep(1500)
        await p4.setViewportSize({ width: 1280, height: 800 })
        await ctx.sleep(1500)
        const paneAfter = size()
        const resizeSent = socks4.flatMap((s) => s.sent).filter((x) => /"type":"resize"/.test(x))
        const dimsAfter = socks4.flatMap((s) => s.received).filter((x) => /"type":"dims"/.test(x)).length
        const stillThere = (await termText(p4)).includes(m)
        if (!stillThere) throw new Error('terminal lost its content after the viewport change')
        // The protocol's resize message, sent directly, for the record.
        const reply = await p4.evaluate(
          (url) =>
            new Promise((resolve) => {
              const ws = new WebSocket(url.replace(/^http/, 'ws'))
              const frames = []
              ws.onmessage = (e) => frames.push(String(e.data).slice(0, 80))
              ws.onopen = () => setTimeout(() => ws.send(JSON.stringify({ type: 'resize', data: { cols: 100, rows: 30 } })), 500)
              setTimeout(() => {
                ws.close()
                resolve(frames.filter((f) => /"type":"(dims|reseed)"/.test(f)))
              }, 2500)
            }),
          `${ctx.baseUrl}/api/runs/${wt.id}/terminal`,
        )
        const paneResized = size()
        // CLAUDE.md / server/pane_ws.go: the WS resize message is a no-op by design
        // (the control client is created with `ignore-size`), so no step may move the pane.
        const sizes = { before: paneBefore, afterViewport: paneAfter, afterWsResize: paneResized }
        const moved = Object.values(sizes).some((v) => v !== paneBefore)
        if (moved) {
          const [cols, rows] = paneBefore.split('x')
          try {
            ctx.tmux('resize-window', '-t', WT, '-x', cols, '-y', rows)
          } catch {}
          throw new Error(`the pane was resized although resize is a documented no-op: ${JSON.stringify(sizes)}; restored to ${size()}`)
        }
        return {
          note: `pane stayed ${paneBefore} through viewport 1280x800 -> 900x600 -> 1280x800 (client resize frames sent ${resizeSent.length}, dims frames ${dimsBefore} -> ${dimsAfter}, content kept) and an explicit WS {resize 100x30} (server replied ${JSON.stringify(reply)}): resize is a no-op`,
          shot: await ctx.shot(p4, 'terminal-resize'),
        }
      })
    } finally {
      await c4.close()
    }
  }

  // ---------------- reconnect-hidden ----------------
  {
    const { context: c5, page: p5 } = await ctx.newPage(ctx.browser, 'mobile')
    const socks5 = trackSockets(p5)
    try {
      await step(ctx, 'reconnect-hidden', async () => {
        await p5.goto(termUrl(wt))
        await p5.waitForSelector('.xterm-rows', { timeout: 15000 })
        await ctx.waitFor(() => socks5.length >= 1, { timeout: 8000, message: 'no terminal websocket opened' })
        await ctx.sleep(1000)
        const before = socks5.length
        // The same signal a backgrounded phone gives: visibilityState hidden,
        // then visible after more than RESYNC_AFTER_HIDDEN_MS (10 s).
        await p5.evaluate(() => {
          window.__qaHidden = true
          Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (window.__qaHidden ? 'hidden' : 'visible') })
          document.dispatchEvent(new Event('visibilitychange'))
        })
        await ctx.sleep(11000)
        await p5.evaluate(() => {
          window.__qaHidden = false
          document.dispatchEvent(new Event('visibilitychange'))
        })
        await ctx.waitFor(() => socks5.length > before, { timeout: 8000, message: `no new terminal websocket after returning from 11 s hidden (still ${socks5.length})` })
        const fresh = socks5[socks5.length - 1]
        await ctx.waitFor(() => fresh.received.some((m) => /"type":"seed"/.test(m)), { timeout: 8000, message: 'new socket received no seed' })
        const marker = `QA-AFTER-${tag}`
        typeInto(ctx, WT, `echo ${marker}`)
        press(ctx, WT, 'Enter')
        await ctx.waitFor(async () => (await termText(p5)).includes(marker), { timeout: 5000, message: 'output stopped flowing after the reconnect' })
        return { note: `${before} -> ${socks5.length} terminal sockets after 11 s hidden; old socket closed: ${socks5[before - 1].closed}; new socket seeded and live output (${marker}) arrived`, shot: await ctx.shot(p5, 'terminal-reconnect-hidden') }
      })
    } finally {
      await c5.close()
    }
  }

  // ---------------- choices (real claude permission dialog) ----------------
  await step(ctx, 'choices', async () => {
    if (!claude) skipOut('agent: no claude run in this harness')
    const probe = path.join(ctx.S, 'term-probe')
    fs.rmSync(probe, { force: true })
    const { context: c6, page: p6 } = await ctx.newPage(ctx.browser, 'mobile')
    const promptStatus = async () => (await ctx.api(`/api/runs/${claude.id}/prompt`)).status
    try {
      await p6.goto(termUrl(claude))
      await p6.waitForSelector('.mobile-input-bar', { timeout: 15000 })
      const r = await ctx.api(`/api/runs/${claude.id}/input`, { method: 'POST', body: { type: 'text', text: `Use the Bash tool to run exactly this command: touch ${probe}` } })
      if (r.status !== 204) throw new Error(`POST input -> ${r.status}`)
      const shown = await p6.waitForSelector('[data-testid="choices"] button', { timeout: 90000 }).then(() => true, () => false)
      if (!shown) {
        const dialog = await ctx.api(`/api/runs/${claude.id}/prompt`)
        if (dialog.status !== 200) skipOut(`agent: claude showed no permission dialog (GET /prompt ${dialog.status}) within 90 s`)
        const err = new Error(
          `expected tappable choice buttons for the open Claude permission dialog, got none: no \`choices\` in the terminal meta frames while GET /prompt is 200 (choices ${JSON.stringify((dialog.json?.choices ?? []).map((c) => c.label ?? c))})`,
        )
        err.shot = await ctx.shot(p6, 'terminal-choices-missing')
        throw err
      }
      const labels = await p6.$$eval('[data-testid="choices"] button', (els) => els.map((e) => e.textContent.trim()))
      if (!labels.every((l, i) => l.startsWith(`${i + 1}. `))) throw new Error(`choice buttons not numbered: ${labels.join(' | ')}`)
      const shot = await ctx.shot(p6, 'terminal-choices')
      const noIdx = labels.findIndex((l) => /^\d+\. No\b/.test(l))
      if (noIdx < 0) throw new Error(`no "No" choice: ${labels.join(' | ')}`)
      const open = (await ctx.api(`/api/runs/${claude.id}/prompt`)).json
      if (!sh.dialogIsFor(open ?? {}, probe)) throw new Error(`the open dialog is not for the requested command (detail ${JSON.stringify(String(open?.detail ?? '').slice(0, 160))}); not answering it`)
      const regionBefore = sh.dialogRegion(ctx, ctx.env.CLAUDE_PANE)
      if (!/Esc to cancel/.test(regionBefore)) throw new Error(`the region below the last rule is not the permission dialog before tapping No: ${JSON.stringify(regionBefore.slice(-200))}`)
      await p6.locator('[data-testid="choices"] button').nth(noIdx).click()
      let regionNow = regionBefore
      await ctx.waitFor(async () => {
        regionNow = sh.dialogRegion(ctx, ctx.env.CLAUDE_PANE)
        return !/Esc to cancel/.test(regionNow) && (await promptStatus()) === 404
      }, { timeout: 20000, message: () => `tapping the No choice did not close the dialog in the pane; region now ${JSON.stringify(regionNow.slice(-200))}` })
      await ctx.sleep(1000)
      if (fs.existsSync(probe)) throw new Error('probe file exists after choosing No')
      return { note: `choices rendered as ${labels.join(' / ')}; tapping "${labels[noIdx]}" sent its ordinal and the dialog closed without running the command`, shot }
    } finally {
      // Leave no dialog open for the next scenario.
      if ((await promptStatus().catch(() => 404)) === 200) ctx.tmux('send-keys', '-t', ctx.env.CLAUDE_PANE, 'Escape')
      await c6.close()
    }
  })
}
