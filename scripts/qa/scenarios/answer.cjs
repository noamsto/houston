// Area 4: answering claude's AskUserQuestion pickers and permission dialogs from
// the Chat tab. Real claude (haiku, default permission mode); each prompt is one
// short sentence.
const fs = require('node:fs')
const path = require('node:path')

const sh = require('./_shared.cjs')

const AREA = 4

async function runOf(ctx, pane) {
  const runs = (await ctx.api('/api/runs')).json
  return runs.find((r) => r.tmux && r.tmux.pane_id === pane)
}

async function sendPrompt(ctx, run, text) {
  const r = await ctx.api(`/api/runs/${run.id}/input`, { method: 'POST', body: { type: 'text', text } })
  if (r.status !== 204) throw new Error(`POST input -> ${r.status} ${r.text.slice(0, 120)}`)
}

// idleAgain waits until the run has read idle on two polls a second apart.
async function idleAgain(ctx, pane, timeout = 120000) {
  let last = 0
  let state
  return ctx.waitFor(async () => {
    const r = await runOf(ctx, pane)
    state = r?.state
    if (r?.state !== 'idle') {
      last = 0
      return null
    }
    if (!last) last = Date.now()
    return Date.now() - last >= 1500 ? r : null
  }, { timeout, interval: 500, message: () => `claude did not return to idle (state ${state})` })
}

function paneEscape(ctx, pane) {
  ctx.tmux('send-keys', '-t', pane, 'Escape')
}

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
    ctx.check(AREA, id, e && e.skip ? 'skip' : 'fail', e instanceof Error ? e.message : String(e))
  }
}

// Output of AskUserQuestion: `"<question>"="<answer>", ...`; a multi-select
// answer joins its labels with ", ".
function parseOutput(output, questions) {
  const got = new Map()
  for (const m of output.matchAll(/"([^"]*)"="([^"]*)"(?=, "|\.|$)/g)) got.set(m[1], m[2])
  return questions.map((q) => got.get(q))
}

exports.run = async (ctx) => {
  const pane = ctx.env.CLAUDE_PANE
  let run = pane ? await runOf(ctx, pane) : null
  if (!run) {
    for (const id of ['ask-single', 'ask-multi', 'ask-wizard', 'permission', 'stale-409']) ctx.check(AREA, id, 'skip', 'agent: no claude run in this harness')
    return
  }
  const ready = await idleAgain(ctx, pane, 60000).catch((e) => e)
  if (ready instanceof Error) {
    for (const id of ['ask-single', 'ask-multi', 'ask-wizard', 'permission', 'stale-409']) ctx.check(AREA, id, 'skip', `agent: ${ready.message} within 60 s of the start`)
    return
  }
  run = ready
  const { context, page } = await ctx.newPage(ctx.browser, 'mobile')
  const chatUrl = ctx.url(`#/fleet/${run.id}/chat`)
  const answerPosts = []
  const answerFrames = []
  // The wizard polls `state` every 2 s; only the requests a check clicks are
  // counted, so `answerPosts.length > before` means that click's answer came back.
  const kindOf = (req) => {
    try {
      return JSON.parse(req.postData() ?? '{}').kind
    } catch {
      return undefined
    }
  }
  page.on('request', (r) => {
    if (r.method() !== 'POST' || !/\/answer$/.test(r.url()) || kindOf(r) === 'state') return
    try {
      answerFrames.push(JSON.parse(r.postData() ?? '{}').frame)
    } catch {
      answerFrames.push(undefined)
    }
  })
  page.on('response', (r) => {
    if (r.request().method() === 'POST' && /\/answer$/.test(r.url()) && kindOf(r.request()) !== 'state') answerPosts.push(r.status())
  })

  // tapGuarded clicks a permission choice but lets the answer leave the browser only
  // when it carries the frame of the dialog the scenario vetted: PermissionBar posts
  // the frame of its own 2 s poll, which may already be a newer dialog.
  const tapGuarded = async (locator, expectedFrame) => {
    let mismatch = null
    let decided = false
    const handler = (route) => {
      let frame
      try {
        frame = JSON.parse(route.request().postData() ?? '{}').frame
      } catch {
        frame = undefined
      }
      decided = true
      if (frame === expectedFrame) return route.continue()
      mismatch = frame
      return route.abort()
    }
    await page.route('**/api/runs/*/answer', handler)
    try {
      await locator.click()
      await ctx.waitFor(() => decided, { timeout: 30000, message: 'no POST /answer after the tap' })
    } finally {
      await page.unroute('**/api/runs/*/answer', handler)
    }
    if (mismatch !== null) throw new Error('refused to send: the tapped frame is not the frame of the dialog that was checked')
  }

  // askQuestion prompts claude and resolves once the Chat picker is up, else
  // retries once and finally skips.
  const askQuestion = async (text) => {
    for (let attempt = 0; attempt < 2; attempt++) {
      await page.goto(chatUrl)
      await page.waitForSelector('.chat-composer-textarea', { timeout: 15000 })
      await sendPrompt(ctx, run, text)
      const up = await page.waitForSelector('.chat-question .chat-question-pick', { timeout: 120000 }).then(() => true, () => false)
      if (up) return
      // The agent answered in prose; get it back to the prompt.
      await idleAgain(ctx, pane, 60000).catch(() => {})
    }
    skipOut('agent did not ask: no AskUserQuestion picker appeared in two tries')
  }

  // The answer itself is already asserted; a run that is slow to read idle
  // afterwards is recorded in the note, not failed here.
  const settleNote = () => idleAgain(ctx, pane, 120000).then(() => '', (e) => `; WARNING: ${e.message} after 120 s`)

  const toolOutput = async (callId) => (await ctx.api(`/api/runs/${run.id}/chat/tool/${encodeURIComponent(callId)}`)).json
  // The tool call id is the update id's tool call; read it from the chat page.
  const lastQuestionCallId = async () => {
    const page1 = (await ctx.api(`/api/runs/${run.id}/chat?limit=100`)).json
    const calls = page1.updates.filter((u) => u.sessionUpdate === 'tool_call' && u._meta?.tool === 'AskUserQuestion')
    return calls.length ? calls[calls.length - 1].toolCallId : null
  }

  // The tabbed wizard card; a completed question above it renders as plain blocks.
  const wiz = () => page.locator('.chat-question:has(.chat-question-wizard)').last()
  const pickBtn = (label) =>
    wiz().locator('button.chat-question-pick').filter({ has: page.locator('.chat-question-option-label', { hasText: new RegExp(`^${label}$`) }) })
  const nextBtn = (text) => wiz().locator('button.chat-question-wizard-next', { hasText: new RegExp(`^${text}$`) })
  const backBtn = () => wiz().locator('button.chat-question-wizard-back')
  const submitBtn = () => wiz().locator('button.chat-question-wizard-next', { hasText: /^Submit$/ })
  const expectProgress = async (want) => {
    let seen = ''
    await ctx.waitFor(async () => {
      seen = ((await wiz().locator('.chat-question-wizard-progress').textContent({ timeout: 1000 }).catch(() => '')) ?? '').trim()
      return seen === want
    }, { timeout: 20000, message: () => `wizard progress reads ${JSON.stringify(seen)}, want ${JSON.stringify(want)}` })
  }
  // makeDriver clicks one wizard button, waits for its POST /answer, checks the
  // status and the card's progress, and records `label status` in `posts`.
  // `dump(n, label, when)` runs before the click and after the card updated.
  const makeDriver = (dump) => {
    const posts = []
    let n = 0
    const act = async (label, locator, wantStatus, wantProgress) => {
      n++
      dump?.(n, label, 'before')
      const before = answerPosts.length
      await locator.click()
      await ctx.waitFor(() => answerPosts.length > before, { timeout: 30000, message: `no POST /answer after ${label}` })
      const status = answerPosts[answerPosts.length - 1]
      posts.push(`${label} ${status}`)
      if (status !== wantStatus) throw new Error(`${label}: POST /answer -> ${status}, want ${wantStatus}`)
      if (wantProgress) await expectProgress(wantProgress)
      dump?.(n, label, 'after')
    }
    return { act, posts }
  }

  try {
    // ---- ask-single ----
    await step(ctx, 'ask-single', async () => {
      await askQuestion('Use the AskUserQuestion tool to ask me to pick a colour: red, green or blue.')
      const group = page.locator('.chat-question-picks[role="radiogroup"]').last()
      const pick = group.locator('button[role="radio"]', { hasText: /green/i }).first()
      if (!(await pick.count())) throw new Error(`no "green" radio; options: ${(await group.locator('button').allTextContents()).join(' | ')}`)
      await pick.click()
      if ((await pick.getAttribute('aria-checked')) !== 'true') throw new Error('tapping green did not stage it')
      const sendBtn = page.locator('.chat-question-send-btn').last()
      if (await sendBtn.isDisabled()) throw new Error('Send answer stayed disabled after staging an option')
      const shotStaged = await ctx.shot(page, 'answer-single-staged')
      const before = answerPosts.length
      await sendBtn.click()
      await ctx.waitFor(() => answerPosts.length > before, { timeout: 30000, message: 'no POST /answer observed' })
      if (answerPosts[answerPosts.length - 1] !== 204) throw new Error(`POST /answer -> ${answerPosts[answerPosts.length - 1]}, want 204`)
      const callId = await lastQuestionCallId()
      await ctx.waitFor(async () => {
        const t = await toolOutput(callId)
        return t && /green/i.test(t.output ?? '') ? t : null
      }, { timeout: 60000, message: 'transcript tool_result never mentioned green' })
      const t = await toolOutput(callId)
      const paneText = ctx.capture(pane, { lines: 60 })
      const idle = await settleNote()
      return { note: `204; tool_result: ${JSON.stringify((t.output ?? '').slice(0, 160))}; green in pane: ${/green/i.test(paneText)}${idle}`, shot: shotStaged }
    })

    // ---- ask-multi ----
    await step(ctx, 'ask-multi', async () => {
      await askQuestion(
        'Use the AskUserQuestion tool with two multiSelect questions at once: pets (cat, dog, fish) and sizes (small, large).',
      )
      await expectProgress('1 of 2')
      const wantByQuestion = []
      const texts = []
      const stageMulti = async (ticks, withOther) => {
        const group = wiz().locator('.chat-question-picks')
        const kind = await group.getAttribute('role')
        if (kind !== 'group') throw new Error(`question ${texts.length + 1} is a ${kind}, want a multi-select group`)
        texts.push((await wiz().locator('.chat-question-text').textContent()).trim())
        const labels = (await group.locator('button.chat-question-pick[role="checkbox"] .chat-question-option-label').allTextContents()).map((s) => s.trim()).filter((l) => l !== 'Other')
        const chosen = labels.slice(0, ticks)
        for (const l of chosen) await pickBtn(l).click()
        if (withOther) {
          await pickBtn('Other').click()
          await wiz().locator('input.chat-question-other-input').fill(withOther)
          chosen.push(withOther)
        }
        wantByQuestion.push(chosen)
      }
      await stageMulti(2, '')
      const driveMulti = makeDriver()
      await driveMulti.act('multi-step1', nextBtn('Next'), 200, '2 of 2')
      await stageMulti(1, 'qa-other')
      const shotStaged = await ctx.shot(page, 'answer-multi-staged')
      await driveMulti.act('multi-step2', nextBtn('Review'), 200, 'Review your answers')
      await driveMulti.act('multi-submit', submitBtn(), 204)
      const callId = await lastQuestionCallId()
      const t = await ctx.waitFor(async () => {
        const x = await toolOutput(callId)
        return x && x.status === 'completed' && x.output ? x : null
      }, { timeout: 60000, message: 'AskUserQuestion never completed with an output' })
      const gotRaw = parseOutput(t.output, texts)
      if (gotRaw.some((g) => g === undefined)) throw new Error(`could not parse tool_result ${JSON.stringify(t.output)} for questions ${JSON.stringify(texts)}`)
      for (let i = 0; i < 2; i++) {
        const got = gotRaw[i].split(', ').filter(Boolean).sort()
        const want = [...wantByQuestion[i]].sort()
        if (JSON.stringify(got) !== JSON.stringify(want)) throw new Error(`question ${i + 1}: tool_result ${JSON.stringify(got)}, wanted exactly ${JSON.stringify(want)} (raw ${JSON.stringify(t.output)})`)
      }
      const idle = await settleNote()
      return { note: `${driveMulti.posts.join(', ')}; tool_result matches exactly: ${JSON.stringify(wantByQuestion)}${idle}`, shot: shotStaged }
    })

    // ---- ask-wizard: three mixed questions, Back twice and change an answer ----
    await step(ctx, 'ask-wizard', async () => {
      const shotsDir = process.env.SHOTS || path.join(ctx.S, 'shots')
      fs.mkdirSync(shotsDir, { recursive: true })
      const dump = (n, label, when) =>
        fs.writeFileSync(path.join(shotsDir, `wizard-${String(n).padStart(2, '0')}-${label}-${when}.txt`), ctx.capture(pane, { lines: 60 }))
      const drive = makeDriver(dump)
      try {
        await askQuestion(
          'Call AskUserQuestion exactly once with three questions. Q1 single-select "Which color do you prefer?" header "Color", options "Red" (description "Warm"), "Blue" (description "Cool"). Q2 multiSelect "Which toppings do you want?" header "Toppings", options "Cheese" (description "Melty"), "Olives" (description "Salty"), "Basil" (description "Fresh"). Q3 single-select "Pick a size?" header "Size", options "Small" (description "Compact"), "Large" (description "Roomy"). Nothing else.',
        )
        const callId = await ctx.waitFor(lastQuestionCallId, { timeout: 15000, message: 'no AskUserQuestion tool call in the chat page' })
        const paneState = async () => {
          const r = await ctx.api(`/api/runs/${run.id}/answer`, { method: 'POST', body: { kind: 'state', toolCallId: callId } })
          if (r.status !== 200) throw new Error(`POST /answer state -> ${r.status} ${r.text.slice(0, 120)}`)
          return r.json
        }
        const state0 = await paneState()
        const labelsOf = (q) => q.options.map((o) => o.label).join('/')
        const shape = state0.questions.map((q) => `${q.multiSelect ? 'multi' : 'single'}:${labelsOf(q)}`).join(' | ')
        if (!state0.tabbed || shape !== 'single:Red/Blue | multi:Cheese/Olives/Basil | single:Small/Large') {
          skipOut(`agent: asked a different question set than requested (${shape})`)
        }
        const texts = state0.questions.map((q) => q.question)
        await expectProgress('1 of 3')

        const squash = (s) => String(s ?? '').replace(/\s+/g, '')
        // The pane's own report of the shown tab, then what the card renders from it.
        const expectShown = async (stepIdx, options, text, on, off) => {
          const st = await paneState()
          if (st.step !== stepIdx) throw new Error(`pane reports step ${st.step}, want ${stepIdx}`)
          if (JSON.stringify(st.options ?? []) !== JSON.stringify(options)) throw new Error(`pane reports options ${JSON.stringify(st.options ?? [])} on step ${stepIdx + 1}, want ${JSON.stringify(options)}`)
          if (squash(st.text) !== squash(text)) throw new Error(`pane reports Other text ${JSON.stringify(st.text ?? '')} on step ${stepIdx + 1}, want ${JSON.stringify(text)}`)
          let seen = ''
          await ctx.waitFor(async () => {
            const ons = await Promise.all(on.map(async (l) => (await pickBtn(l).getAttribute('aria-checked')) === 'true'))
            const offs = await Promise.all(off.map(async (l) => (await pickBtn(l).getAttribute('aria-checked')) === 'false'))
            const typed = text ? await wiz().locator('input.chat-question-other-input').inputValue().catch(() => null) : ''
            seen = `on ${JSON.stringify(ons)} off ${JSON.stringify(offs)} other text ${JSON.stringify(typed)}`
            return ons.every(Boolean) && offs.every(Boolean) && (!text || squash(typed) === squash(text))
          }, { timeout: 10000, message: () => `card on step ${stepIdx + 1} does not show ${JSON.stringify(on)} on / ${JSON.stringify(off)} off / Other ${JSON.stringify(text)}: ${seen}` })
        }

        // Step 1: Blue.
        await pickBtn('Blue').click()
        await drive.act('step1-blue-next', nextBtn('Next'), 200, '2 of 3')
        // Step 2: Cheese plus Other text.
        await pickBtn('Cheese').click()
        await pickBtn('Other').click()
        await wiz().locator('input.chat-question-other-input').fill('extra pepper')
        await drive.act('step2-next', nextBtn('Next'), 200, '3 of 3')
        // Back to step 2, then step 1: the pane keeps both selections.
        await drive.act('back-from-3', backBtn(), 200, '2 of 3')
        await expectShown(1, [0], 'extra pepper', ['Cheese', 'Other'], ['Olives', 'Basil'])
        await drive.act('back-from-2', backBtn(), 200, '1 of 3')
        await expectShown(0, [1], '', ['Blue'], ['Red'])
        // Change Blue to Red and walk forward again.
        await pickBtn('Red').click()
        await drive.act('step1-red-next', nextBtn('Next'), 200, '2 of 3')
        await expectShown(1, [0], 'extra pepper', ['Cheese', 'Other'], ['Olives', 'Basil'])
        await drive.act('step2-next-again', nextBtn('Next'), 200, '3 of 3')
        // Step 3: Large, then the review screen.
        await pickBtn('Large').click()
        await drive.act('step3-large-review', nextBtn('Review'), 200, 'Review your answers')
        const rows = (await wiz().locator('.chat-question-wizard-review-a').allTextContents()).map((s) => s.replace(/\s+/g, ' ').trim())
        const wantRows = ['Red', 'Cheese, extra pepper', 'Large']
        if (JSON.stringify(rows) !== JSON.stringify(wantRows)) throw new Error(`review rows ${JSON.stringify(rows)}, want ${JSON.stringify(wantRows)}`)
        const paneReview = (await paneState()).review ?? []
        if (JSON.stringify(paneReview.map((s) => s.replace(/\s+/g, ' ').trim())) !== JSON.stringify(wantRows)) throw new Error(`pane review ${JSON.stringify(paneReview)}, want ${JSON.stringify(wantRows)}`)
        const shotReview = await ctx.shot(page, 'answer-wizard-review')
        await drive.act('submit', submitBtn(), 204)

        const t = await ctx.waitFor(async () => {
          const x = await toolOutput(callId)
          return x && x.status === 'completed' && x.output ? x : null
        }, { timeout: 60000, message: 'AskUserQuestion never completed with an output' })
        const gotRaw = parseOutput(t.output, texts)
        if (gotRaw.some((g) => g === undefined)) throw new Error(`could not parse tool_result ${JSON.stringify(t.output)} for questions ${JSON.stringify(texts)}`)
        const want = [['Red'], ['Cheese', 'extra pepper'], ['Large']]
        for (let i = 0; i < 3; i++) {
          const got = gotRaw[i].split(', ').filter(Boolean).sort()
          if (JSON.stringify(got) !== JSON.stringify([...want[i]].sort())) throw new Error(`question ${i + 1}: tool_result ${JSON.stringify(got)}, wanted exactly ${JSON.stringify(want[i])} (raw ${JSON.stringify(t.output)})`)
        }
        const idle = await settleNote()
        return { note: `POST /answer: ${drive.posts.join(', ')}; Back kept the pane selection, review ${JSON.stringify(wantRows)}; tool_result matches: ${JSON.stringify(want)}; captures wizard-NN-*.txt in ${shotsDir}${idle}`, shot: shotReview }
      } catch (e) {
        // A dialog left open would block the checks after this one.
        paneEscape(ctx, pane)
        if (e && e.skip) throw e
        throw new Error(`${e instanceof Error ? e.message : e} (POST /answer so far: ${drive.posts.join(', ')})`)
      }
    })

    // ---- permission (Yes, then No) and stale-409 on the second dialog ----
    const promptStatus = async () => (await ctx.api(`/api/runs/${run.id}/prompt`))
    const openDialog = async (text) => {
      for (let attempt = 0; attempt < 2; attempt++) {
        await page.goto(chatUrl)
        await page.waitForSelector('.chat-composer-textarea', { timeout: 15000 })
        await sendPrompt(ctx, run, text)
        const got = await ctx.waitFor(async () => {
          const r = await promptStatus()
          return r.status === 200 ? r : null
        }, { timeout: 90000, interval: 500, message: 'no permission dialog (GET /prompt never 200)' }).catch(() => null)
        if (got) return got.json
        await idleAgain(ctx, pane, 60000).catch(() => {})
      }
      return null
    }

    const probe = path.join(ctx.S, 'perm-probe')
    const probe2 = path.join(ctx.S, 'perm-probe2')
    for (const f of [probe, probe2]) fs.rmSync(f, { force: true })

    let permissionYes = null
    let permissionNo = null
    let stateAfterNo = ''
    const dialog1 = await openDialog(`Use the Bash tool to run exactly this command: touch ${probe}`)
    if (!dialog1) {
      permissionYes = 'skip: agent did not ask: no permission dialog (GET /prompt stayed 404) in two tries'
    } else if (!sh.dialogIsFor(dialog1, probe)) {
      paneEscape(ctx, pane)
      permissionYes = `skip: agent: the open dialog is not for the requested command (detail ${JSON.stringify(String(dialog1.detail ?? '').slice(0, 160))}); dismissed with Escape, nothing answered`
    } else {
      try {
        await page.waitForSelector('.permission-bar-choice', { timeout: 15000 })
        const labels = await page.$$eval('.permission-bar-choice', (els) => els.map((e) => e.textContent.trim()))
        if (!/^1\. Yes/i.test(labels[0])) throw new Error(`first choice is "${labels[0]}", want 1. Yes`)
        const shot = await ctx.shot(page, 'answer-permission-yes')
        const before = answerPosts.length
        await tapGuarded(page.locator('.permission-bar-choice').first(), dialog1.frame)
        await ctx.waitFor(() => answerPosts.length > before, { timeout: 30000, message: 'no POST /answer after tapping 1. Yes' })
        if (answerPosts[answerPosts.length - 1] !== 204) throw new Error(`POST /answer -> ${answerPosts[answerPosts.length - 1]}, want 204`)
        await ctx.waitFor(() => fs.existsSync(probe), { timeout: 60000, message: `${probe} never appeared after Yes` })
        const cap = ctx.capture(pane, { lines: 80 })
        if (!cap.includes('perm-probe')) throw new Error('pane does not show the command')
        const idle = await settleNote()
        permissionYes = `pass: tapped "${labels[0]}" (choices: ${labels.join(' / ')}); 204; ${probe} exists${idle}`
        permissionYes = { text: permissionYes, shot }
      } catch (e) {
        permissionYes = `fail: ${e instanceof Error ? e.message : e}`
      }
    }

    const dialog2 = permissionYes && String(permissionYes).startsWith('skip')
      ? null
      : await openDialog(`Use the Bash tool to run exactly this command: touch ${probe2}`)
    if (!dialog2 || !sh.dialogIsFor(dialog2, probe2)) {
      const why = dialog2
        ? (paneEscape(ctx, pane), `the open dialog is not for the requested command (detail ${JSON.stringify(String(dialog2.detail ?? '').slice(0, 160))}); dismissed with Escape`)
        : 'no second permission dialog'
      permissionNo = `skip: agent: ${why}`
      await step(ctx, 'stale-409', async () => skipOut(`agent: ${why}, so there is no open dialog to send a stale frame against`))
    } else {
      // stale-409 first: the dialog is open and untouched.
      await step(ctx, 'stale-409', async () => {
        await page.waitForSelector('.permission-bar-choice', { timeout: 15000 })
        const stale = '0'.repeat(64)
        const controlA = sh.dialogRegion(ctx, pane)
        await ctx.sleep(1500)
        const controlB = sh.dialogRegion(ctx, pane)
        if (controlA !== controlB) throw new Error('dialog region differs between two captures with no input: the comparison itself is unstable')
        const paneBefore = sh.dialogRegion(ctx, pane)
        const res = await page.evaluate(async (id) => {
          const r = await fetch(`/api/runs/${id}/answer`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ kind: 'choice', ordinal: 1, frame: '0'.repeat(64) }),
          })
          return { status: r.status, text: (await r.text()).trim() }
        }, run.id)
        await ctx.sleep(1500)
        const paneAfter = sh.dialogRegion(ctx, pane)
        if (res.status !== 409) throw new Error(`stale frame -> ${res.status} ${res.text}, want 409`)
        if (res.text !== 'prompt changed') throw new Error(`409 body "${res.text}", want "prompt changed"`)
        if (paneBefore !== paneAfter) throw new Error('dialog region changed after the stale answer (something was typed)')
        if (fs.existsSync(probe2)) throw new Error('probe2 appeared after a stale answer')
        const still = await promptStatus()
        if (still.status !== 200) throw new Error(`dialog no longer open after the stale answer (GET /prompt ${still.status})`)
        return { note: `409 "${res.text}" for frame ${stale.slice(0, 6)}...; dialog region identical before and after; dialog still open`, shot: await ctx.shot(page, 'answer-stale-409') }
      })

      try {
        const labels = await page.$$eval('.permission-bar-choice', (els) => els.map((e) => e.textContent.trim()))
        const noIdx = labels.findIndex((l) => /^\d+\. No\b/i.test(l))
        if (noIdx < 0) throw new Error(`no "No" choice among: ${labels.join(' / ')}`)
        const shot = await ctx.shot(page, 'answer-permission-no')
        const regionBefore = sh.dialogRegion(ctx, pane)
        if (!/Esc to cancel/.test(regionBefore)) throw new Error(`the region below the last rule is not the permission dialog before tapping No: ${JSON.stringify(regionBefore.slice(-200))}`)
        const before = answerPosts.length
        await tapGuarded(page.locator('.permission-bar-choice').nth(noIdx), dialog2.frame)
        await ctx.waitFor(() => answerPosts.length > before, { timeout: 30000, message: 'no POST /answer after tapping No' })
        if (answerPosts[answerPosts.length - 1] !== 204) throw new Error(`POST /answer -> ${answerPosts[answerPosts.length - 1]}, want 204`)
        // A denied dialog fires no Stop, so wait for the dialog itself to go: the
        // region below the last rule loses its "Esc to cancel" footer and GET /prompt is 404.
        let regionNow = regionBefore
        await ctx.waitFor(async () => {
          regionNow = sh.dialogRegion(ctx, pane)
          return !/Esc to cancel/.test(regionNow) && (await promptStatus()).status === 404
        }, { timeout: 30000, message: () => `the dialog never closed after tapping No; region now ${JSON.stringify(regionNow.slice(-200))}` })
        await ctx.sleep(1500)
        if (fs.existsSync(probe2)) throw new Error(`${probe2} exists after choosing No`)
        const after = await runOf(ctx, pane)
        stateAfterNo = after?.state
        permissionNo = { text: `pass: tapped "${labels[noIdx]}"; 204; ${probe2} absent; dialog gone from the pane and GET /prompt 404; run state afterwards: ${stateAfterNo}`, shot }
      } catch (e) {
        permissionNo = `fail: ${e instanceof Error ? e.message : e}`
      }
    }

    // One permission line: both halves must hold.
    const part = (p) => (typeof p === 'object' ? p.text : p)
    const parts = [permissionYes, permissionNo]
    const failed = parts.filter((p) => part(p).startsWith('fail'))
    const skipped = parts.filter((p) => part(p).startsWith('skip'))
    const shotPath = parts.map((p) => (typeof p === 'object' ? p.shot : '')).find(Boolean) ?? ''
    const note = `Yes: ${part(permissionYes).replace(/^(pass|fail|skip): /, '')} | No: ${part(permissionNo).replace(/^(pass|fail|skip): /, '')}`
    if (failed.length) ctx.check(AREA, 'permission', 'fail', note, shotPath)
    else if (skipped.length) ctx.check(AREA, 'permission', 'skip', note, shotPath)
    else ctx.check(AREA, 'permission', 'pass', note, shotPath)
  } finally {
    await context.close()
  }
}
