# Navigation

Read when changing hash routes, the history stack, Back or edge-swipe behaviour.

Moved verbatim from the root `CLAUDE.md`; section headings are unchanged.

## Navigation

Every shell tab is a hash route — `#/fleet`, `#/workspace`, `#/dispatch` (`ui/src/fleet/routes.ts`, `useShellTab`) — so Back/Forward move between tabs and reload restores the tab. A run detail is `#/fleet/<id>/<tab>`, tab `chat`, `terminal` or `crew`. `#/fleet/<id>/crew` is the dispatcher's Crew tab: offered only on a dispatcher run (local, or a peer's) that carries `crew.name` in dispatcher mode (`offeredTabs`, `ui/src/fleet/runTabs.ts`), and that run's default tab when the route names none; on any other run a `crew` route degrades like an unknown tab. `#/dispatch` exists only in dispatcher mode; tmux mode rewrites it to `#/fleet`. It takes `host=`, `repo=` and `crew=` (see "Dispatch"). `#/crews` (any query) is a retired alias rewritten to `#/fleet` in every mode, including while the mode is unknown (#248).

Navigation is a stack kept in `history.state` (`ui/src/fleet/nav.ts`), so houston's Back, the edge swipe and the browser's native back all land on the same entry:

- **Levels:** a shell tab is the root; a run detail pushes on top of it; a run opened from inside another run's detail (a worker from its dispatcher's Crew roster or activity feed) pushes again. Each pushed entry carries `{houston: {depth, from, root}}`: `depth` counts houston's run entries above the first one it did not push, `from` is the hash it was opened from, `root` the shell-tab hash at the bottom (null when the stack starts on a deep-linked run).
- `openRun(id, tab?)` pushes, except that re-opening the run already shown replaces in place. `selectRun(id)` (desktop list and Workspace picks) replaces laterally when a detail is shown, since the detail is a persistent pane beside the list; choosing the run the current entry's `from` names (the run on screen's parent) collapses the stack with `back()` instead, so `from` never names the run now shown. Crew roster/feed links keep `openRun`. `switchRunTab(id, tab)` replaces: switching a run's Chat/Terminal/Crew tabs (bottom bar, top row, "Reply in Terminal", the question card's Terminal links) adds no entry, on desktop too.
- `back(rootHash)`: `history.back()` when `depth ≥ 1` (a real pop); otherwise (deep link, reload with nothing of houston's underneath) `replaceState` to `from`, else the shell root. It never pushes, so a native back afterwards cannot walk forward into the run just left.
- `popToRoot(rootHash)`: `history.go(-depth)` when the stack sits on a shell root, else `replaceState` to the root. Mobile reaches it by holding the bottom-bar Back, or the header's root button (shown at depth ≥ 2).
- `back` and `popToRoot` are async, so a second call while a pop is in flight is ignored until the guard clears: `popstate` (the usual case), else `pageshow`/`hashchange`, or a ~1 s fallback timer — a traversal that never lands must not wedge every later Back (a double tap would otherwise skip the dispatcher or leave the app).
- Back labels name the destination (`backLabel`): the parent run's codename (else branch), or the shell tab.
- Shell tabs: a tab tap pushes the tab hash (on mobile also closing the detail overlay); re-tapping the tab already shown adds no entry in either shell, and on mobile also scrolls that list to the top. On desktop the detail is a persistent pane, so a rail switch while a run is selected is state-only (no history entry, not restored on reload).
- `pushState`/`replaceState` fire no `hashchange`, so `navigateHash` dispatches one itself; every route hook (`useDetailRoute`, `useShellTab`, `useDispatchRoute`'s `seq`) keeps listening to `hashchange`, which a real pop also fires. In-app links keep their `href` (long-press, copy, new tab) and route a plain click through `openRun`/`switchRunTab` (`onAppLink`).


