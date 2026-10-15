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
    for (const id of ['ask-single', 'ask-multi', 'permission', 'stale-409']) ctx.check(AREA, id, 'skip', 'agent: no claude run in this harness')
    return
  }
  const ready = await idleAgain(ctx, pane, 60000).catch((e) => e)
  if (ready instanceof Error) {
    for (const id of ['ask-single', 'ask-multi', 'permission', 'stale-409']) ctx.check(AREA, id, 'skip', `agent: ${ready.message} within 60 s of the start`)
    return
  }
  run = ready
  const { context, page } = await ctx.newPage(ctx.browser, 'mobile')
  const chatUrl = ctx.url(`#/fleet/${run.id}/chat`)
  const answerPosts = []
  const answerFrames = []
  page.on('request', (r) => {
    if (r.method() !== 'POST' || !/\/answer$/.test(r.url())) return
    try {
      answerFrames.push(JSON.parse(r.postData() ?? '{}').frame)
    } catch {
      answerFrames.push(undefined)
    }
  })
  page.on('response', (r) => {
    if (r.request().method() === 'POST' && /\/answer$/.test(r.url())) answerPosts.push(r.status())
  })

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
      const blocks = page.locator('.chat-question').last().locator('.chat-question-block')
      const n = await blocks.count()
      if (n < 2) throw new Error(`picker shows ${n} question block(s), want 2`)
      const wantByQuestion = []
      const texts = []
      for (let i = 0; i < 2; i++) {
        const b = blocks.nth(i)
        texts.push((await b.locator('.chat-question-text').textContent()).trim())
        const kind = await b.locator('.chat-question-picks').getAttribute('role')
        if (kind !== 'group') throw new Error(`question ${i + 1} is a ${kind}, want a multi-select group`)
        const picks = b.locator('button.chat-question-pick[role="checkbox"]')
        const labels = (await picks.locator('.chat-question-option-label').allTextContents()).map((s) => s.trim())
        const chosen = i === 0 ? [labels[0], labels[1]] : [labels[0]]
        for (const idx of i === 0 ? [0, 1] : [0]) await picks.nth(idx).click()
        wantByQuestion.push(chosen)
      }
      // Other text on the second question.
      const second = blocks.nth(1)
      const otherPick = second.locator('button.chat-question-pick[role="checkbox"]').filter({ has: page.locator('.chat-question-option-label', { hasText: /^Other$/ }) })
      await otherPick.click()
      const otherInput = second.locator('input.chat-question-other-input')
      await otherInput.fill('qa-other')
      wantByQuestion[1].push('qa-other')
      const shotStaged = await ctx.shot(page, 'answer-multi-staged')
      const sendBtn = page.locator('.chat-question-send-btn').last()
      const before = answerPosts.length
      await sendBtn.click()
      await ctx.waitFor(() => answerPosts.length > before, { timeout: 30000, message: 'no POST /answer observed' })
      if (answerPosts[answerPosts.length - 1] !== 204) throw new Error(`POST /answer -> ${answerPosts[answerPosts.length - 1]}, want 204`)
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
      return { note: `204; tool_result matches exactly: ${JSON.stringify(wantByQuestion)}${idle}`, shot: shotStaged }
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
        await page.locator('.permission-bar-choice').first().click()
        await ctx.waitFor(() => answerPosts.length > before, { timeout: 30000, message: 'no POST /answer after tapping 1. Yes' })
        if (answerFrames[before] !== dialog1.frame) throw new Error('POSTed frame is not the frame of the dialog that was checked')
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
        await page.locator('.permission-bar-choice').nth(noIdx).click()
        await ctx.waitFor(() => answerPosts.length > before, { timeout: 30000, message: 'no POST /answer after tapping No' })
        if (answerFrames[before] !== dialog2.frame) throw new Error('POSTed frame is not the frame of the dialog that was checked')
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
