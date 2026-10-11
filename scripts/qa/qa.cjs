// QA runner: drives headless Chromium through the scenarios in
// scripts/qa/scenarios/ against the harness setup.sh made.
//
//   S=/tmp/hqa.XXXXXX [SCENARIOS=fleet,nav] [SHOTS=dir] node scripts/qa/qa.cjs
//
// S (or the first argument) is the scratch root holding qa.env; qa.env also
// supplies NODE_PATH (playwright-core) and CHROMIUM unless set in the
// environment. SCENARIOS is a comma list, default all ten, in area order. Each
// name loads ./scenarios/<name>.cjs, which exports `async run(ctx)` (ctx is the
// scripts/qa/lib.cjs API plus `browser`); a missing module is one skip line.
// A built-in `guard` self-test (area 0) always runs first.
//
// Stdout is one JSON result line per check, then
//   {"summary":{"pass":N,"fail":N,"skip":N},"guard_violations":[...]}
// Exit 1 on any fail or any remaining guard violation, 2 on a setup error.
const fs = require('node:fs')
const path = require('node:path')

const AREAS = ['fleet', 'nav', 'chat', 'answer', 'terminal', 'crew', 'workspace', 'dispatch', 'modes', 'liveness']

async function main() {
  const lib = require('./lib.cjs').load()
  // qa.env carries the NODE_PATH that holds playwright-core; re-resolve paths
  // after adopting it, as a NODE_PATH set before launch would have been.
  if (lib.env.NODE_PATH) {
    process.env.NODE_PATH = [lib.env.NODE_PATH, process.env.NODE_PATH].filter(Boolean).join(path.delimiter)
    require('node:module').Module._initPaths()
  }
  const { chromium } = require('playwright-core')

  const wanted = (process.env.SCENARIOS ?? AREAS.join(','))
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)

  const browser = await chromium.launch({ executablePath: lib.env.CHROMIUM || undefined, headless: true })
  const ctx = { ...lib, browser }

  try {
    await guardSelfTest(ctx)
    for (const name of wanted) {
      const area = AREAS.indexOf(name) + 1
      const file = path.join(__dirname, 'scenarios', `${name}.cjs`)
      if (!/^[a-z0-9-]+$/.test(name) || !fs.existsSync(file)) {
        lib.check(area, name, 'skip', 'not implemented')
        continue
      }
      try {
        await require(file).run(ctx)
      } catch (e) {
        lib.check(area, `${name}:scenario`, 'fail', e instanceof Error ? (e.stack ?? e.message).split('\n').slice(0, 4).join(' | ') : String(e))
      }
    }
  } finally {
    await browser.close().catch(() => {})
  }

  const count = (r) => lib.results.filter((x) => x.result === r).length
  const summary = { pass: count('pass'), fail: count('fail'), skip: count('skip') }
  console.log(JSON.stringify({ summary, guard_violations: lib.violations }))
  process.exit(summary.fail > 0 || lib.violations.length > 0 ? 1 : 0)
}

// guardSelfTest proves the browser guard blocks a foreign origin. Port 9 is the
// discard service; nothing there is ever reached, because the guard aborts first.
async function guardSelfTest(ctx) {
  const probe = 'http://127.0.0.1:9/'
  await ctx.expect(0, 'guard', async () => {
    const { context, page } = await ctx.newPage(ctx.browser, 'desktop')
    try {
      await page.goto(ctx.url(), { waitUntil: 'domcontentloaded' })
      const fetched = await page.evaluate((u) => fetch(u).then(() => 'reached', () => 'blocked'), probe)
      if (fetched !== 'blocked') throw new Error(`fetch to ${probe} was not blocked (${fetched})`)
      if (!ctx.violations.includes(probe)) throw new Error(`guard did not record ${probe}`)
      const ws = await page.evaluate(
        () =>
          new Promise((resolve) => {
            const s = new WebSocket('ws://127.0.0.1:9/')
            s.onopen = () => resolve('opened')
            s.onerror = () => resolve('blocked')
            s.onclose = () => resolve('blocked')
          }),
      )
      if (ws === 'opened') throw new Error('websocket to a foreign origin stayed open')
      if (!ctx.violations.includes('ws://127.0.0.1:9/')) throw new Error('guard did not record the foreign websocket')
      return 'foreign http and ws origins refused and recorded'
    } finally {
      await context.close()
      // The probes were provoked on purpose; leave only real violations.
      for (let i = ctx.violations.length - 1; i >= 0; i--) {
        if (ctx.violations[i] === probe || ctx.violations[i] === 'ws://127.0.0.1:9/') ctx.violations.splice(i, 1)
      }
    }
  })
}

main().catch((e) => {
  console.error(e instanceof Error ? e.message : e)
  process.exit(2)
})
