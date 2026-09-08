# Plan: DOM test utility and useRuns() lifecycle coverage

Closes #21.

## Context

`useRuns()` (`ui/src/hooks/useRuns.ts`) is the store the new fleet shell reads from.
It has no lifecycle test today because the repo's vitest setup has no DOM — existing
tests (`ui/src/fleet/staleness.test.ts`, `ui/src/hooks/useRuns.test.ts`) only exercise
pure functions (`applyEvent`). The open question: does unmounting the hook close the
`EventSource`? Reading the current source, the effect's cleanup already calls
`es.close()` (`useRuns.ts:56`) — so no leak is expected, but nothing proves it, and the
task explicitly asks us to prove the test bites by deleting that cleanup and watching
the test go red.

## Approach

1. **DOM test utility: `happy-dom` + `@testing-library/react`.** Both jsdom and happy-dom
   work for this use; happy-dom is the smaller footprint (no bundled native
   dependencies, faster startup), so it wins per the task's tie-break instruction. We
   install a fake `EventSource` regardless of which DOM library is picked — even if the
   installed happy-dom version ships a native one, the test needs deterministic control
   over connection lifecycle and event delivery that only a fake gives, so don't repeat
   an "neither has EventSource" claim in the README/PR body without checking.
   `@testing-library/react` gives `render`/`act`/`cleanup` for hook lifecycle testing
   without hand-rolling a test harness component.
2. **Add dependencies first.** Add `happy-dom` and `@testing-library/react` to
   `ui/package.json` devDependencies (not `@testing-library/dom` explicitly — it's a
   `peerDependency` of `@testing-library/react`, not a direct dependency; npm installs
   it automatically, and adding an unused explicit devDep would contradict the "smaller
   footprint" rationale — add it explicitly only if `npm install` or a lint rule
   complains it's missing). Do not add `jsdom`. Run `npm install` in `ui/` now, before
   writing any code that imports these packages (steps 4–6 below need them on disk to
   typecheck/run) — `ui/package-lock.json` regenerates; it gets committed in step 9.
3. **Wire into vitest.** `ui/vite.config.ts` is type-checked as part of
   `tsconfig.node.json` (`"include": ["vite.config.ts"]`, `"types": ["node"]`, strict) —
   Vite's `UserConfig` has no `test` property, so adding one under the current
   `import { defineConfig } from 'vite'` fails `npx tsc -b` with an excess-property
   error. Change the import to `import { defineConfig } from 'vitest/config'` (it
   re-exports Vite's `defineConfig` with the `test` property merged in via module
   augmentation) before adding the `test` block: `environment: 'happy-dom'`,
   `globals: false` (keep explicit imports, matching existing test style). No
   `setupFiles`/global `afterEach` — see step 5's explicit `cleanup` call instead. This
   applies `happy-dom` globally, including to the two existing pure-logic suites
   (`staleness.test.ts`, `useRuns.test.ts`); harmless, so don't scope it per-file.
4. **Reusable fake `EventSource` helper** — new file `ui/src/testing/fakeEventSource.ts`.
   Not inline in the test file per the task's explicit instruction (the future run-detail
   view will reuse it for its own SSE hook). Shape:
   - A class implementing enough of the `EventSource` surface for `useRuns()`:
     `addEventListener`, `removeEventListener`, `close`, `onerror`, `readyState`, a
     public `url` field captured from the constructor arg (needed for the "opened URL
     is `/api/runs/stream`" assertion below), and a public `closed` boolean flipped by
     `close()`. `erasableSyntaxOnly` (tsconfig.app.json) forbids constructor parameter
     properties, so declare fields explicitly and assign in the constructor body;
     `noUnusedParameters` means `removeEventListener(type, listener)` must actually
     consult its params (a real per-type listener map, not a stub).
   - A registry returned fresh from each `installFakeEventSource()` call (not a shared
     module-level array) — otherwise `instances.length === 1`/`=== 2` assertions leak
     state across tests. It records every instance constructed, so a test can assert
     "exactly one EventSource was opened" and grab a handle to drive it:
     - `instance.emit(type, payload)` — dispatches a `MessageEvent` to listeners
       registered for `type` via `addEventListener`; `payload` is a plain JS value that
       `emit` `JSON.stringify`s into `.data` itself (the hook does `JSON.parse(ev.data)`,
       so `.data` must be a string — callers pass the object, not a pre-serialised
       string).
     - `instance.emitRaw(type, data)` — dispatches a `MessageEvent` with `.data` set to
       the given string verbatim, no stringification. Needed for the malformed-JSON
       test: `emit('update', 'not json')` would `JSON.stringify` to `'"not json"'`,
       which **is** valid JSON and would parse without exercising the `catch` path at
       all — `emitRaw('update', '{')` (or any non-JSON string) is what actually produces
       an unparseable `.data`.
     - `instance.open()` — dispatches the `open` event (`useRuns.ts:33` subscribes to
       it; the fake's required surface must include it, not just `snapshot`/`update`).
     - `instance.error()` — invokes `onerror` if set.
     - `instance.close()` — sets `closed = true` (also called by real code via the
       hook's cleanup).
   - `installFakeEventSource()` returns `{ instances, uninstall }`; sets
     `globalThis.EventSource = FakeEventSource as unknown as typeof EventSource` and
     restores the original on `uninstall()`. Call `installFakeEventSource()` in
     `beforeEach`; in `afterEach`, call `@testing-library/react`'s `cleanup()` (which
     unmounts and runs the hook's real `close()` on the fake) **before** `uninstall()`,
     so unmount-driven `close()` still lands on the fake instance and not the restored
     original.
5. **Lifecycle tests** — new file `ui/src/hooks/useRuns.lifecycle.test.ts` (kept separate
   from the existing pure-logic `useRuns.test.ts` so the DOM-dependent suite is
   distinguishable at a glance). Import `cleanup` from `@testing-library/react` and call
   it explicitly in `afterEach` (RTL only auto-registers `afterEach(cleanup)` when a
   global `afterEach` exists, which `globals: false` does not provide). Use a named
   wrapper function for `renderHook`, not an anonymous arrow, to avoid tripping
   `react-hooks/rules-of-hooks` under `eslint-plugin-react-hooks` (`ui/eslint.config.js`
   extends its flat recommended config repo-wide): `renderHook(function useRunsProbe() {
   return useRuns() })`.
   - Mount opens exactly one `EventSource` (assert `instances.length === 1` after
     `renderHook(...)`, and assert the URL is `/api/runs/stream`).
   - Unmount closes it: capture the instance, `unmount()`, assert `instance.closed`.
   - Remount after unmount opens a fresh connection: unmount, `renderHook` again,
     assert `instances.length === 2` and the second instance is distinct from
     (`!==`) and not closed like the first.
   - Runs arriving over SSE land in the store and re-render consumers: inside `act()`,
     call `instance.emit('snapshot', [run])`; after the `act()` call, assert
     `result.current.runs` reflects it; inside another `act()`, `instance.emit('update',
     run2)`; assert both present.
   - SSE error/disconnect does not wedge the store: inside `act()`, `instance.open()`
     (or emit a `snapshot`) and assert `connected === true` first — the error assertion
     must observe a real true→false transition, not vacuously pass from `connected`'s
     `false` initial state; then inside `act()`, `instance.error()` and assert
     `connected === false`; then inside `act()`, emit a further `snapshot` (not
     `update` — only the `snapshot` listener calls `setConnected(true)`, at
     `useRuns.ts:39`) and assert both that `runs` updates *and* `connected` returns to
     `true` (proves the listeners are still live post-error, not torn down).
   - Malformed JSON on `snapshot`/`update` does not wedge the store either: use
     `instance.emitRaw('update', '{')` (genuinely unparseable, not the `emit`
     convenience — see step 4), assert the hook doesn't throw and a subsequent
     well-formed `emit('update', run)` still updates `runs` (exercises the `catch`
     blocks at `useRuns.ts:40,50`, otherwise dead code as far as tests are concerned).
6. **Prove the tests bite** — one concrete break per test, run `npx vitest run`,
   confirm red, revert, before moving to the next:
   - mount-opens-one → temporarily duplicate the `new EventSource(...)` call inside the
     effect at `useRuns.ts:31` so two instances open on a single mount. (Dropping the
     `[]` dep array alone will *not* trip this: `renderHook` renders once and RTL
     doesn't wrap in `StrictMode`, so the effect still only runs once either way — lead
     with the duplicate-call break, skip the dep-array variant.)
   - unmount-closes → delete `return () => es.close()` (line 56).
   - remount-opens-fresh → hoist `es` to a **lazily-created** module-level `let es`
     (assigned inside the effect on first use, reused across mounts) rather than one
     constructed at module load — a singleton built at import time would call `new
     EventSource(...)` the moment `useRuns.ts` is imported, including by
     `useRuns.test.ts` (which installs no fake), turning that unrelated suite red too
     and muddying the signal.
   - SSE-events-land → make the `update` listener body a no-op (drop the `setRuns` call
     at line 48).
   - error-doesn't-wedge → delete `es.onerror = () => setConnected(false)` (line 54).
   - malformed-JSON-doesn't-wedge → remove the `try/catch` around `JSON.parse` in the
     `snapshot` or `update` listener so a malformed payload throws uncaught.
   Record each break→red→revert cycle's outcome in the PR body.
7. **Leak check.** Given the Context section's up-front read, no leak is expected — the existing
   cleanup already closes the connection. If the lifecycle test *does* reveal a leak
   once written (e.g. a race between rapid mount/unmount), fix `useRuns.ts` and call it
   out prominently in the PR body per the task's instruction. This step is conditional
   on what the test actually finds, not assumed work.
8. **`ui/README.md`** — append a short "## Testing" section with one line documenting
   the DOM test utility choice: "DOM tests use `happy-dom` + `@testing-library/react`
   (see `ui/src/testing/fakeEventSource.ts` for a reusable fake `EventSource`)." Leave
   the existing Vite boilerplate content untouched — out of scope.
9. **Commit the regenerated lockfile.** `ui/package-lock.json` was regenerated by
   step 2's `npm install`; stage and commit it alongside `ui/package.json`.
10. **Update the pinned Nix hash.** `flake.nix:23` pins `npmDepsHash` for the
   `buildNpmPackage` UI derivation (`let ui = pkgs.buildNpmPackage { ...; src = ./ui;
   npmDepsHash = "..."; ... }` inside `perSystem`) against the lockfile; step 2's
   dependency change invalidates it. Run `nix build .#default` (the only package
   output — `packages.default`, per `flake.nix`'s `perSystem`; there is no separate
   `.#ui` attribute), take the
   `got: sha256-...` hash from the resulting mismatch error, and update `npmDepsHash` at
   `flake.nix:23` to that value. This file is not on the task's do-not-touch list.
11. **Verify acceptance gate.** Run `npx vitest run`, `npx tsc -b`, `npx eslint .`, and
    `npm run build` (production build) in `ui/` — `npm run build` already runs `tsc -b`
    per `ui/package.json`, so this also re-confirms typecheck. Additionally run
    `nix build .#default` to confirm the updated `npmDepsHash` is correct and the flake
    still builds clean from this branch.

## Files touched

- `ui/package.json` (devDependencies only)
- `ui/package-lock.json` (regenerated by `npm install`)
- `ui/vite.config.ts` (switch import to `vitest/config`, add `test` block)
- `ui/src/testing/fakeEventSource.ts` (new)
- `ui/src/hooks/useRuns.lifecycle.test.ts` (new)
- `ui/README.md` (one line, new "## Testing" section)
- `flake.nix` (`npmDepsHash` update, line 23)
- `ui/src/hooks/useRuns.ts` (only if step 7's leak check finds something to fix)

## Explicitly out of scope

`.github/workflows/`, `ui/src/fleet/`, `ui/src/theme/`, `server/`, `runs/`, `tmux/`, the
run detail view, the mobile terminal. `useRuns.test.ts` (existing pure-logic file)
stays untouched — new DOM tests go in a separate file.

## Acceptance criteria

- [ ] `happy-dom` + `@testing-library/react` wired into vitest config.
- [ ] One line in `ui/README.md` documenting the DOM test utility.
- [ ] `ui/src/testing/fakeEventSource.ts` exists as a reusable fake, used by the new
      lifecycle tests (and ready for future SSE-hook tests).
- [ ] Lifecycle tests cover: single EventSource on mount, close on unmount, fresh
      connection on remount, SSE events updating the store/re-rendering, error not
      wedging the store, malformed JSON not wedging the store.
- [ ] Each significant test's break→red→revert cycle documented in the PR body.
- [ ] Any leak found is fixed and called out prominently in the PR body (conditional).
- [ ] `npx vitest run`, `npx tsc -b`, `npx eslint .`, and the production build all pass.
- [ ] `nix build .#default` passes with the updated `npmDepsHash`.
