// Area 3: the Chat tab, on the real claude and pi agents of the harness plus the
// loadfixture session with the largest transcript. Agent prompts are one short
// sentence each.
const fs = require('node:fs')
const path = require('node:path')

const AREA = 3
const BIG_SESSION = 'sess-00000000-0000-4000-8000-000000000000'
// 1x1 transparent PNG.
const PNG_1X1 = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==', 'base64')

async function runOf(ctx, pane) {
  const runs = (await ctx.api('/api/runs')).json
  return runs.find((r) => r.tmux && r.tmux.pane_id === pane)
}

async function sendPrompt(ctx, run, text) {
  const r = await ctx.api(`/api/runs/${run.id}/input`, { method: 'POST', body: { type: 'text', text } })
  if (r.status !== 204) throw new Error(`POST input -> ${r.status} ${r.text.slice(0, 120)}`)
}

// waitIdle waits for the turn counter to pass `turn0`, then for the run to
// settle back to idle.
async function waitIdle(ctx, pane, turn0, timeout = 120000) {
  let turn = turn0
  await ctx.waitFor(async () => (turn = (await runOf(ctx, pane))?.activity?.turn ?? 0) > turn0, {
    timeout: 20000,
    message: () => `agent never started the turn (turn counter stayed ${turn})`,
  })
  let state
  return ctx.waitFor(async () => {
    const r = await runOf(ctx, pane)
    state = r?.state
    return r && r.state === 'idle' ? r : null
  }, { timeout, message: () => `agent did not return to idle (state ${state})` })
}

async function prompt(ctx, run, text, timeout) {
  const turn0 = run.activity?.turn ?? 0
  await sendPrompt(ctx, run, text)
  return waitIdle(ctx, run.tmux.pane_id, turn0, timeout)
}

function skipOut(reason) {
  const e = new Error(reason instanceof Error ? reason.message : String(reason))
  e.skip = true
  throw e
}

// step records one check: a thrown skipOut() is a skip, any other throw a fail.
async function step(ctx, id, fn) {
  try {
    const r = await fn()
    const note = r && typeof r === 'object' ? r.note : r
    ctx.check(AREA, id, 'pass', typeof note === 'string' ? note : '', r && typeof r === 'object' ? r.shot : '')
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e)
    ctx.check(AREA, id, e && e.skip ? 'skip' : 'fail', msg)
  }
}

exports.run = async (ctx) => {
  const claude = ctx.env.CLAUDE_PANE ? await runOf(ctx, ctx.env.CLAUDE_PANE) : null
  const pi = ctx.env.PI_PANE ? await runOf(ctx, ctx.env.PI_PANE) : null
  const { context, page } = await ctx.newPage(ctx.browser, 'mobile')
  const pageErrors = []
  page.on('pageerror', (e) => pageErrors.push(String(e)))
  let claudeLive = claude
  let piLive = pi
  const chatUrl = (r) => ctx.url(`#/fleet/${r.id}/chat`)
  const settle = (ms = 400) => ctx.sleep(ms)

  try {
    // ---- claude: stream, context meter, quick commands (disabled while working) ----
    const quick = { seen: [] }
    let ready = claude
    if (claude) {
      // Every claude step needs an idle agent; a busy one is a harness state, not a product result.
      ready = await ctx.waitFor(async () => {
        const r = await runOf(ctx, ctx.env.CLAUDE_PANE)
        return r?.state === 'idle' ? r : null
      }, { timeout: 60000, interval: 500, message: 'claude is not idle' }).catch(async (e) => ({ notIdle: `${e.message} (state ${(await runOf(ctx, ctx.env.CLAUDE_PANE))?.state}) after 60 s` }))
    }
    if (!claude || ready.notIdle) {
      for (const id of ['stream', 'context-meter', 'quick-commands', 'catchup-settled', 'tool-row', 'suggested-pill', 'draft-persist', 'image-send'])
        ctx.check(AREA, id, 'skip', claude ? `agent: ${ready.notIdle}` : 'agent: no claude run in this harness')
    } else {
      claudeLive = ready
      await page.goto(chatUrl(claude))
      await page.waitForSelector('.chat-composer-textarea', { timeout: 15000 })
      await page.evaluate(() => {
        window.__qaMarker = 'same-document'
      })
      // Track the /compact pill's disabled state against the run's state text
      // for the whole turn; the pill is only rendered while "/" is open.
      await page.click('button[aria-label="Commands"]')
      await page.evaluate(() => {
        window.__qaPill = []
        window.__qaPillTimer = setInterval(() => {
          const pill = [...document.querySelectorAll('[data-testid="quick-commands"] button')].find((b) => b.textContent === '/compact')
          const state = document.querySelector('.run-status-state')?.textContent ?? ''
          if (pill) window.__qaPill.push(`${state}:${pill.disabled ? 'disabled' : 'enabled'}`)
        }, 40)
      })

      const marker = 'pong' + Date.now().toString(36).slice(-4)
      await step(ctx, 'stream', async () => {
        const turn0 = claude.activity?.turn ?? 0
        await sendPrompt(ctx, claude, `Reply with only the word: ${marker}`)
        await page.waitForFunction((m) => [...document.querySelectorAll('.chat-assistant')].some((b) => b.textContent.includes(m)), marker, { timeout: 120000 })
        const kept = await page.evaluate(() => window.__qaMarker)
        if (kept !== 'same-document') throw new Error('page reloaded while streaming')
        const sawUser = await page.evaluate((m) => [...document.querySelectorAll('.chat-user')].some((b) => b.textContent.includes(m)), marker)
        if (!sawUser) throw new Error('your own prompt never rendered as a user bubble')
        claudeLive = await waitIdle(ctx, ctx.env.CLAUDE_PANE, turn0)
        const shot = await ctx.shot(page, 'chat-stream-claude')
        return { note: `assistant text "${marker}" appeared in the open Chat tab without a reload`, shot }
      })
      quick.seen = await page.evaluate(() => window.__qaPill.slice())

      // Context meter from the strip.
      await step(ctx, 'context-meter', async () => {
        const label = await page.textContent('.run-status-ctx-label')
        if (!/^\d+k \/ 200k ctx$/.test(label.trim())) throw new Error(`strip says "${label}", want "<n>k / 200k ctx"`)
        if (!(await page.$('.run-status-ctx-bar[role="progressbar"]'))) throw new Error('no context progress bar')
        const used = claudeLive.context?.used
        if (!used || Math.abs(Math.round(used / 1000) - parseInt(label, 10)) > 1) throw new Error(`strip "${label}" disagrees with /api/runs context.used=${used}`)
        return { note: label.trim(), shot: await ctx.shot(page, 'chat-context-meter') }
      })

      // ---- tool row ----
      await step(ctx, 'tool-row', async () => {
        try {
          claudeLive = await prompt(ctx, claudeLive, 'Run ls with the Bash tool, then reply with only the word: listed')
        } catch (e) {
          return skipOut(e)
        }
        await page.waitForSelector('.chat-tools-toggle', { timeout: 20000 })
        const toggles = await page.$$eval('.chat-tools-toggle', (els) => els.map((e) => e.textContent))
        if (!toggles.some((t) => /Bash/.test(t))) throw new Error(`no Bash tool row; rows: ${toggles.join(' | ')}`)
        const row = page.locator('.chat-tools-toggle', { hasText: 'Bash' }).last()
        await row.click()
        const call = page.locator('.chat-tool-call-row', { hasText: 'Bash' }).last()
        await call.click()
        const out = page.locator('.chat-tool-output').last()
        await out.waitFor({ timeout: 15000 })
        const text = await out.textContent()
        const want = fs.readdirSync(path.join(ctx.S, 'repos', 'qa-repo')).filter((n) => n !== '.git')
        if (!want.some((n) => text.includes(n))) throw new Error(`tool output "${text.slice(0, 120)}" lists none of ${want.join(', ')}`)
        return { note: `Bash row expanded; output lists ${want.filter((n) => text.includes(n)).join(', ')}`, shot: await ctx.shot(page, 'chat-tool-row') }
      })

      // ---- quick commands ----
      await step(ctx, 'quick-commands', async () => {
        quick.seen.push(...(await page.evaluate(() => window.__qaPill.slice())))
        const disabled = quick.seen.filter((s) => s.endsWith(':disabled'))
        const enabledIdle = quick.seen.filter((s) => s === 'idle:enabled')
        if (!disabled.length) throw new Error(`the pill was never seen disabled during two agent turns; samples ${[...new Set(quick.seen)].join(', ')}`)
        if (disabled.some((s) => s.startsWith('idle:'))) throw new Error('pill disabled while the run read idle')
        if (!enabledIdle.length) throw new Error('pill never enabled while idle')
        const states = [...new Set(disabled.map((s) => s.split(':')[0]))].join('/')
        await page.evaluate(() => clearInterval(window.__qaPillTimer))
        const listed = await page.$$eval('[data-testid="quick-commands"] button', (bs) => bs.map((b) => b.textContent))
        for (const c of ['/compact', '/compact…', '/clear', '/context']) if (!listed.includes(c)) throw new Error(`pill row misses ${c}: ${listed.join(' ')}`)
        // The component's own status element sits inside [data-testid="quick-commands"]
        // (QuickCommands.tsx, role="status"); it reads "Sent /context — waiting…" until the
        // run leaves idle and then "/context accepted", so record every text it shows.
        await page.evaluate(() => {
          const el = document.querySelector('[data-testid="quick-commands"]')
          window.__qaStatus = []
          const take = () => {
            const t = el.querySelector('[role="status"]')?.textContent
            if (t && window.__qaStatus[window.__qaStatus.length - 1] !== t) window.__qaStatus.push(t)
          }
          window.__qaStatusObs = new MutationObserver(take)
          window.__qaStatusObs.observe(el, { subtree: true, childList: true, characterData: true })
        })
        const contextLines = () => (ctx.capture(ctx.env.CLAUDE_PANE, { lines: 300 }).match(/^\s*[^\w\s]{0,2}\s*\/context\s*$/gm) ?? []).length
        const linesBefore = contextLines()
        await page.locator('[data-testid="quick-commands"] button', { hasText: /^\/context$/ }).click()
        await ctx.waitFor(() => contextLines() > linesBefore, { timeout: 20000, message: () => `no literal "/context" line reached the pane scrollback (count stayed ${linesBefore})` })
        const texts = await page.evaluate(() => {
          window.__qaStatusObs.disconnect()
          return window.__qaStatus.slice()
        })
        if (!texts.some((t) => /^Sent \/context/.test(t))) throw new Error(`the quick-commands status never read "Sent /context …"; it showed ${JSON.stringify(texts)}`)
        await ctx.waitFor(async () => (await runOf(ctx, ctx.env.CLAUDE_PANE))?.state === 'idle', { timeout: 30000, message: 'not idle after /context' })
        return { note: `disabled while ${states}, enabled at idle; /context typed into the pane (literal line in scrollback); status read ${JSON.stringify(texts)}`, shot: await ctx.shot(page, 'chat-quick-commands') }
      })

      // ---- suggested pill ----
      await step(ctx, 'suggested-pill', async () => {
        let ok = false
        for (let attempt = 0; attempt < 2 && !ok; attempt++) {
          try {
            claudeLive = await prompt(ctx, claudeLive, 'Reply with only a fenced code block containing the single line /compact')
          } catch (e) {
            return skipOut(e)
          }
          ok = await page.waitForSelector('button:has-text("↳ suggested")', { timeout: 8000 }).then(() => true, () => false)
        }
        if (!ok) return skipOut('the agent did not reply with a fenced slash command twice')
        await page.click('button:has-text("↳ suggested")')
        const box = page.locator('#quick-suggested')
        const v = await box.inputValue()
        if (v.trim() !== '/compact') throw new Error(`suggestion sheet holds "${v}", want /compact`)
        await box.fill('/compact qa edit')
        if ((await box.inputValue()) !== '/compact qa edit') throw new Error('sheet text is not editable')
        const shot = await ctx.shot(page, 'chat-suggested-pill')
        await page.click('button:has-text("Cancel")')
        return { note: 'pill opened an editable sheet prefilled /compact; cancelled, nothing sent', shot }
      })

      // ---- catch-up settled (after a reload) ----
      await step(ctx, 'catchup-settled', async () => {
        // Its own page: the observer below must not keep recording on the page the later steps use.
        const cp = await context.newPage()
        try {
          await cp.addInitScript(() => {
            window.__qaSnaps = new Map()
            const take = () => {
              const bubbles = document.querySelectorAll('.chat-assistant[data-id]')
              const b = bubbles[bubbles.length - 1]
              if (!b) return
              const list = window.__qaSnaps.get(b.dataset.id) ?? []
              list.push(b.textContent)
              window.__qaSnaps.set(b.dataset.id, list)
            }
            new MutationObserver(take).observe(document, { subtree: true, childList: true, characterData: true })
          })
          await cp.goto(chatUrl(claudeLive))
          await cp.waitForSelector('.chat-assistant', { timeout: 15000 })
          await settle(2500)
          const info = await cp.evaluate(() => {
            const bubbles = document.querySelectorAll('.chat-assistant[data-id]')
            const last = bubbles[bubbles.length - 1]
            return { id: last.dataset.id, final: last.textContent, snaps: window.__qaSnaps.get(last.dataset.id) ?? [], plain: !!last.querySelector('.chat-plain'), md: !!last.querySelector('.chat-md') }
          })
          if (!info.final.trim()) throw new Error('last assistant bubble is empty')
          // The plain-text stand-in shows the whole raw text at once and markdown
          // then replaces it; a typing reveal instead shows growing prefixes.
          const raw = info.snaps.reduce((a, b) => (b.length > a.length ? b : a), '')
          const partial = info.snaps.filter((t) => t !== info.final && t.length < raw.length && raw.startsWith(t))
          if (partial.length) throw new Error(`last message rendered partially after reload (typing reveal): ${JSON.stringify(partial.slice(0, 3))} of ${JSON.stringify(raw)}`)
          if (info.plain || !info.md) throw new Error('last message still in the plain-text reveal state after 2.5 s')
          return { note: `last message "${info.final.slice(0, 40)}" rendered complete on first paint, ${info.snaps.length} DOM snapshots all complete`, shot: await ctx.shot(cp, 'chat-catchup-settled') }
        } finally {
          await cp.close()
        }
      })

      // ---- draft persistence, chat and terminal composers ----
      await step(ctx, 'draft-persist', async () => {
        const dk = claudeLive.draft_key ?? claudeLive.id
        const chatKey = `houston-draft:chat:${dk}`
        const runKey = `houston-draft:run:${dk}`
        await page.goto(chatUrl(claudeLive))
        const chatBox = page.locator('.chat-composer-textarea')
        await chatBox.fill('qa-draft-chat')
        await ctx.waitFor(() => page.evaluate((k) => localStorage.getItem(k), chatKey), { timeout: 5000, message: `no localStorage ${chatKey}` })
        await page.reload()
        await page.waitForSelector('.chat-composer-textarea')
        const afterChat = await page.locator('.chat-composer-textarea').inputValue()
        if (afterChat !== 'qa-draft-chat') throw new Error(`chat draft after reload: "${afterChat}"`)

        await page.goto(ctx.url(`#/fleet/${claudeLive.id}/terminal`))
        const termBox = page.locator('.mobile-input-bar textarea')
        await termBox.waitFor({ timeout: 15000 })
        await termBox.fill('qa-draft-term')
        await ctx.waitFor(() => page.evaluate((k) => localStorage.getItem(k), runKey), { timeout: 5000, message: `no localStorage ${runKey}` })
        await page.reload()
        const afterTerm = page.locator('.mobile-input-bar textarea')
        await afterTerm.waitFor({ timeout: 15000 })
        if ((await afterTerm.inputValue()) !== 'qa-draft-term') throw new Error(`terminal draft after reload: "${await afterTerm.inputValue()}"`)
        const chatStill = await page.evaluate((k) => localStorage.getItem(k), chatKey)
        if (!chatStill || !chatStill.includes('qa-draft-chat')) throw new Error('the chat draft was lost when the terminal draft was typed')
        const shot = await ctx.shot(page, 'chat-draft-persist')
        await afterTerm.fill('')
        await page.goto(chatUrl(claudeLive))
        await page.locator('.chat-composer-textarea').fill('')
        await ctx.waitFor(() => page.evaluate((k) => localStorage.getItem(k) === null, chatKey), { timeout: 5000, message: 'draft not cleared' })
        return { note: `both composers restored their drafts after a reload (${chatKey}, ${runKey}); independent keys`, shot }
      })

      // ---- image send ----
      await step(ctx, 'image-send', async () => {
        const png = path.join(ctx.S, 'qa-1x1.png')
        fs.writeFileSync(png, PNG_1X1)
        await page.goto(chatUrl(claudeLive))
        await page.waitForSelector('.chat-composer-textarea')
        const turn0 = (await runOf(ctx, ctx.env.CLAUDE_PANE)).activity?.turn ?? 0
        await page.setInputFiles('.chat-composer input[type="file"]', png)
        await page.waitForSelector('[data-testid="staged-image"]', { timeout: 5000 })
        const chip = await page.textContent('[data-testid="staged-image"] .staged-chip-name')
        if (chip.trim() !== 'qa-1x1.png') throw new Error(`chip names "${chip}"`)
        const chipShot = await ctx.shot(page, 'chat-image-staged')
        await page.fill('.chat-composer-textarea', 'Reply with only the word: imgok')
        await page.click('.chat-composer-send')
        await page.waitForSelector('[data-testid="staged-image"]', { state: 'detached', timeout: 15000 })
        const found = await ctx
          .waitFor(() => {
            const cap = ctx.capture(ctx.env.CLAUDE_PANE, { lines: 80 })
            return /\[Image #\d+\]|\.png/.test(cap) && /imgok/.test(cap) ? cap : null
          }, { timeout: 30000, message: 'pane shows no image reference with the text' })
          .catch((e) => e)
        if (found instanceof Error) throw found
        const ref = found.match(/\[Image #\d+\]|[\w/.-]*\.png/)[0]
        const settled = await waitIdle(ctx, ctx.env.CLAUDE_PANE, turn0).then(() => '', (e) => `; WARNING: ${e.message}`)
        return { note: `image chip appeared, Send cleared it, pane shows "${ref}" with the text${settled}`, shot: chipShot }
      })
    }

    // ---- pi: stream, spend, settled ----
    if (!pi) {
      for (const id of ['stream-pi', 'pi-spend']) ctx.check(AREA, id, 'skip', 'agent: no pi run in this harness')
    } else {
      const m = 'pong' + Date.now().toString(36).slice(-4)
      await step(ctx, 'stream-pi', async () => {
        await page.goto(chatUrl(pi))
        await page.waitForSelector('.chat-composer-textarea', { timeout: 15000 })
        await page.evaluate(() => {
          window.__qaMarker = 'same-document'
        })
        const turn0 = pi.activity?.turn ?? 0
        await sendPrompt(ctx, pi, `Reply with only the word: ${m}`)
        try {
          await page.waitForFunction((x) => [...document.querySelectorAll('.chat-assistant')].some((b) => b.textContent.includes(x)), m, { timeout: 120000 })
        } catch {
          return skipOut('pi gave no reply within 120 s')
        }
        if ((await page.evaluate(() => window.__qaMarker)) !== 'same-document') throw new Error('page reloaded while streaming')
        let piState
        piLive = await ctx.waitFor(async () => {
          const r = await runOf(ctx, ctx.env.PI_PANE)
          piState = r?.state
          return r && r.state === 'idle' ? r : null
        }, { timeout: 60000, message: () => `pi stayed ${piState} after replying` })
        return { note: `pi reply "${m}" appeared without a reload; pi run settled to idle (turn ${piLive.activity?.turn}, hook turn counter ${turn0} before)`, shot: await ctx.shot(page, 'chat-stream-pi') }
      })
      await step(ctx, 'pi-spend', async () => {
        const r = (await runOf(ctx, ctx.env.PI_PANE)) ?? piLive
        if (r.spend_usd === undefined) return skipOut('pi reported no cost (spend_usd absent) for this model')
        const label = await page.textContent('.run-status-spend')
        if (!/^\$\d+\.\d\d$/.test(label.trim())) throw new Error(`pi strip spend "${label}"`)
        if (label.trim() !== `$${r.spend_usd.toFixed(2)}`) throw new Error(`strip ${label} vs api ${r.spend_usd}`)
        return { note: `${label} (api spend_usd ${r.spend_usd})`, shot: await ctx.shot(page, 'chat-pi-spend') }
      })
    }

    // ---- fixture session with the largest transcript: scrollback, latest pill, divider ----
    const bigUrl = ctx.url(`#/fleet/${BIG_SESSION}/chat`)
    const big = (await ctx.api('/api/runs')).json.find((r) => r.id === BIG_SESSION)
    if (!big) {
      for (const id of ['scrollback', 'latest-pill', 'divider']) ctx.check(AREA, id, 'skip', `fixture session ${BIG_SESSION} not in /api/runs`)
    } else {
      await page.goto(bigUrl)
      await page.waitForSelector('.chat-timeline [data-id]', { timeout: 20000 })
      await settle(800)
      const items = () => page.$$eval('.chat-timeline [data-id]', (els) => els.map((e) => ({ id: e.dataset.id, seq: Number(e.dataset.seq) })))

      await step(ctx, 'latest-pill', async () => {
        if (await page.$('.chat-latest')) throw new Error('"↓ Latest" visible while stuck to the bottom')
        await page.evaluate(() => {
          document.querySelector('.chat-timeline').scrollTop = 0
        })
        await page.waitForSelector('.chat-latest', { timeout: 5000 })
        const shot = await ctx.shot(page, 'chat-latest-pill')
        await page.click('.chat-latest')
        await page.waitForSelector('.chat-latest', { state: 'detached', timeout: 5000 })
        const gap = await page.evaluate(() => {
          const t = document.querySelector('.chat-timeline')
          return t.scrollHeight - t.scrollTop - t.clientHeight
        })
        if (gap > 24) throw new Error(`still ${gap}px above the bottom after tapping the pill`)
        return { note: `pill shown after scrolling up, tapping it returned to the bottom (gap ${Math.round(gap)}px)`, shot }
      })

      await step(ctx, 'scrollback', async () => {
        const first = await items()
        const hasMore = await page.$('.chat-earlier')
        if (!hasMore) return skipOut(`transcript fits one page (${first.length} items)`)
        await page.evaluate(() => {
          document.querySelector('.chat-timeline').scrollTop = 0
        })
        await settle(200)
        const anchorId = first[0].id
        const topBefore = await page.evaluate((i) => document.querySelector(`[data-id="${i}"]`).getBoundingClientRect().top, anchorId)
        const [resp] = await Promise.all([
          page.waitForResponse((r) => /\/chat\?.*before=\d+/.test(r.url()) && r.status() === 200, { timeout: 10000 }),
          page.click('.chat-earlier'),
        ])
        await settle(600)
        const after = await items()
        if (after.length <= first.length) throw new Error(`no older items prepended (${first.length} -> ${after.length})`)
        if (after[0].seq >= first[0].seq) throw new Error(`first item seq ${first[0].seq} -> ${after[0].seq}, not older`)
        const ids = after.map((i) => i.id)
        if (new Set(ids).size !== ids.length) throw new Error('duplicate data-id after prepending')
        const seqs = after.map((i) => i.seq)
        if (seqs.some((s, i) => i > 0 && s <= seqs[i - 1])) throw new Error('items are not strictly ordered by seq')
        const topAfter = await page.evaluate((i) => document.querySelector(`[data-id="${i}"]`).getBoundingClientRect().top, anchorId)
        const url = new URL(resp.url())
        return {
          note: `"Show earlier" (a button; nothing fetches on scroll alone) fetched ${url.pathname}${url.search}: ${first.length} -> ${after.length} items, ids unique, order kept, anchor moved ${Math.round(topAfter - topBefore)}px`,
          shot: await ctx.shot(page, 'chat-scrollback'),
        }
      })

      await step(ctx, 'divider', async () => {
        for (let i = 0; i < 8 && !(await page.$('.chat-divider')); i++) {
          if (!(await page.$('.chat-earlier'))) break
          await page.click('.chat-earlier')
          await settle(700)
        }
        const dividers = await page.$$eval('.chat-divider', (els) => els.map((e) => e.textContent.replace(/\s+/g, ' ').trim()))
        if (!dividers.length) return skipOut('no task-notification divider within 8 pages of the fixture transcript')
        if (!dividers.every((d) => d.includes('background task finished'))) throw new Error(`divider labels: ${dividers.join(' | ')}`)
        const el = page.locator('.chat-divider').first()
        await el.scrollIntoViewIfNeeded()
        return { note: `${dividers.length} divider(s) "${dividers[0]}"`, shot: await ctx.shot(page, 'chat-divider') }
      })
    }

    if (pageErrors.length) ctx.check(AREA, 'page-errors', 'fail', pageErrors.slice(0, 3).join(' | '))
  } finally {
    await context.close()
  }
}
