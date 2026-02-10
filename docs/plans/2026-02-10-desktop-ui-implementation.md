# Desktop UI Redesign — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Transform the desktop dashboard (1024px+) from a stretched mobile layout into a proper desktop experience with full-width card grid, inline quick actions, and agent strip navigation.

**Architecture:** CSS-first responsive redesign. The same HTML serves mobile and desktop — CSS media queries morph the layout. New HTML elements (card action zone, agent strip) are added and hidden on mobile. The expanded view navigates to the pane page with an agent strip bar, avoiding duplication of pane page JS.

**Tech Stack:** Go templ templates, CSS media queries, vanilla JS

**Design doc:** `docs/plans/2026-02-10-desktop-ui-redesign.md`

---

## Task 1: Desktop typography and base layout

**Files:**
- Modify: `views/styles.templ` (indexPageStyles, desktop media query block starting ~line 857)

### Step 1: Add sans-serif font for desktop UI

In `views/styles.templ`, inside the `@media (min-width: 1024px)` block for indexPageStyles, add a font-family override for body and key UI elements. Terminal preview zones keep monospace.

```css
/* Inside @media (min-width: 1024px) */
body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
}
/* Terminal zones keep monospace */
.window-preview, .action-preview, .action-input {
    font-family: 'IBM Plex Mono', 'Symbols Nerd Font Mono', ui-monospace, monospace;
}
```

### Step 2: Remove max-width constraints and scan lines on desktop

Update the desktop media query to use full-width layout with comfortable padding:

```css
@media (min-width: 1024px) {
    /* Remove scan lines on desktop */
    body::before {
        display: none;
    }
    .header-content {
        max-width: none;
        padding: 0 2rem;
    }
    .search-bar {
        max-width: none;
        padding: 0 2rem 0.75rem;
    }
    .main {
        max-width: none;
        padding: 1.5rem 2rem;
    }
}
```

Update the wide desktop (1400px+) similarly:

```css
@media (min-width: 1400px) {
    .header-content,
    .search-bar {
        padding: 0 3rem;
    }
    .search-bar {
        padding: 0 3rem 0.75rem;
    }
    .main {
        padding: 2rem 3rem;
    }
}
```

### Step 3: Run templ generate and verify

Run: `templ generate`
Expected: Clean generation, no errors

Run: `go build .`
Expected: Clean build

### Step 4: Commit

```bash
git add views/styles.templ views/styles_templ.go
git commit -m "feat(desktop): sans-serif typography, full-width layout, no scan lines"
```

---

## Task 2: Flat card grid layout

**Files:**
- Modify: `views/styles.templ` (indexPageStyles, desktop media queries)

### Step 1: Make sessions render as flat cards on desktop

On desktop, the session accordion wrapper becomes transparent and the windows grid becomes the primary visual element. Section labels are hidden — sorting handles priority.

Add to the desktop media query in indexPageStyles:

```css
@media (min-width: 1024px) {
    /* ... existing typography/layout from Task 1 ... */

    /* Hide section labels on desktop - sort order communicates priority */
    .section-label {
        display: none;
    }

    /* Session wrapper becomes transparent — repo label only */
    .session {
        background: none;
        border: none;
        border-radius: 0;
        box-shadow: none;
        margin-bottom: 0;
        overflow: visible;
    }
    .session.has-attention {
        border-color: transparent;
        box-shadow: none;
    }

    /* Session header becomes flat repo label */
    .session-header {
        padding: 0 0 0.5rem 0;
        cursor: default;
    }
    .session-header:hover {
        background: none;
    }
    .session-name {
        font-size: 11px;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        color: var(--text-dim);
    }
    .session-meta {
        display: none;
    }
    .session-indicator {
        display: none;
    }
    .session-chevron {
        display: none;
    }
    .session-badge {
        display: none;
    }
    .dismiss-btn {
        display: none;
    }

    /* Windows always visible, in grid layout */
    .windows {
        display: grid !important;
        grid-template-columns: repeat(auto-fill, minmax(420px, 1fr));
        gap: 1rem;
        border-top: none;
        background: none;
    }
    .windows.hidden {
        display: grid !important;
    }

    /* Sessions grid becomes simple flex column with spacing */
    .sessions-grid {
        display: flex;
        flex-direction: column;
        gap: 1.5rem;
    }
}

@media (min-width: 1400px) {
    .windows {
        grid-template-columns: repeat(auto-fill, minmax(460px, 1fr));
        gap: 1.5rem;
    }
}
```

### Step 2: Style window cards as desktop agent cards

```css
@media (min-width: 1024px) {
    /* Window becomes a proper card on desktop */
    .window {
        display: flex;
        flex-direction: column;
        align-items: stretch;
        padding: 0;
        border: 1px solid var(--border);
        border-radius: 10px;
        background: var(--bg-panel);
        box-shadow: 0 1px 3px rgba(0, 0, 0, 0.3);
        cursor: default;
        overflow: hidden;
        transition: border-color 0.2s, box-shadow 0.2s;
    }
    .window:hover {
        border-color: color-mix(in srgb, var(--border) 100%, white 20%);
        box-shadow: 0 2px 8px rgba(0, 0, 0, 0.4);
    }
    .window:last-child {
        border-bottom: 1px solid var(--border);
    }
    .window.attention {
        border-color: var(--attention);
        box-shadow: 0 0 0 1px var(--attention-glow), 0 2px 8px rgba(0, 0, 0, 0.3);
    }
    .window.working {
        border-color: var(--working);
        box-shadow: 0 0 0 1px var(--working-glow), 0 1px 3px rgba(0, 0, 0, 0.3);
    }

    /* Card header zone */
    .window-indicator {
        width: 8px;
        height: 8px;
        margin-right: 0.5rem;
        margin-top: 0;
    }
    .window-info {
        flex: none;
        width: 100%;
        padding: 1rem 1.25rem 0.5rem;
        min-width: 0;
    }
    .window-name {
        font-size: 14px;
        font-weight: 500;
        display: flex;
        align-items: center;
        gap: 0.375rem;
    }
    .window-status {
        font-size: 12px;
        margin-top: 0.25rem;
    }

    /* Card preview zone — recessed terminal area */
    .window-preview {
        max-height: 15em;
        margin: 0 0.75rem;
        padding: 0.75rem;
        background: var(--bg-deep);
        border: 1px solid var(--border);
        border-radius: 6px;
        font-family: 'IBM Plex Mono', 'Symbols Nerd Font Mono', ui-monospace, monospace;
        font-size: 11.5px;
        line-height: 1.5;
    }

    /* Actions zone — hide by default, shown via JS or new HTML */
    .window-actions {
        flex-direction: row;
        padding: 0.75rem 1.25rem;
        border-top: 1px solid var(--border);
        gap: 0.5rem;
        width: 100%;
    }
    .window-arrow {
        display: none;
    }
    .window-view-btn {
        margin-left: auto;
    }
}
```

### Step 3: Run templ generate and verify

Run: `templ generate`
Run: `go build .`

### Step 4: Test in browser

Open houston in a desktop browser (1024px+ wide). Verify:
- Sessions show as flat repo labels
- Windows display as cards in a grid
- Cards have distinct background/border/shadow
- Preview shows more lines
- Mobile layout (resize to narrow) still works as before

### Step 5: Commit

```bash
git add views/styles.templ views/styles_templ.go
git commit -m "feat(desktop): flat card grid layout with repo headers"
```

---

## Task 3: Card inline quick actions

**Files:**
- Modify: `views/components.templ` (windowCard component)
- Modify: `views/styles.templ` (new CSS for card actions)
- Modify: `views/pages.templ` (JS for inline send)

### Step 1: Add action zone HTML to windowCard

In `views/components.templ`, add a `card-actions` div after the existing `window-actions` div inside `windowCard`. This new div is hidden on mobile via CSS.

```go
// After the existing window-actions div, add:
<div class="card-actions">
    <input type="text" class="card-input"
        placeholder="Send input..."
        autocapitalize="off" autocorrect="off" spellcheck="false"
        data-target={ fmt.Sprintf("%s:%d.%d", sessionName, win.Window.Index, win.Pane.Index) }
        onkeydown="handleCardInput(event, this)" />
    <button class="card-btn card-send"
        onclick={ templ.ComponentScript{Call: fmt.Sprintf("sendCardInput(this, '%s:%d.%d')", sessionName, win.Window.Index, win.Pane.Index)} }
        title="Send">
        <svg width="14" height="14" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 12h14M12 5l7 7-7 7"></path>
        </svg>
    </button>
    <button class="card-btn card-stop"
        onclick={ templ.ComponentScript{Call: fmt.Sprintf("sendCardStop('%s:%d.%d')", sessionName, win.Window.Index, win.Pane.Index)} }
        title="Stop (Escape)">
        <svg width="14" height="14" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path>
        </svg>
    </button>
</div>
```

Also add choice buttons inside the card when choices are available:

```go
// Before the card-actions div, add:
if len(win.ParseResult.Choices) > 0 {
    <div class="card-choices"
        data-target={ fmt.Sprintf("%s:%d.%d", sessionName, win.Window.Index, win.Pane.Index) }>
        for i, choice := range win.ParseResult.Choices {
            <button class="choice-btn"
                onclick={ templ.ComponentScript{Call: fmt.Sprintf("sendCardChoice('%s:%d.%d', %d)", sessionName, win.Window.Index, win.Pane.Index, i+1)} }
                title={ choice }>
                <span class="choice-num">{ fmt.Sprintf("%d", i+1) }</span>
                <span class="choice-text">{ truncate(choice, 25) }</span>
            </button>
        }
    </div>
}
```

### Step 2: Add CSS for card actions (hidden on mobile, visible on desktop)

In `views/styles.templ`, add to the base styles (mobile):

```css
/* Card actions — hidden on mobile */
.card-actions {
    display: none;
}
.card-choices {
    display: none;
}
```

In the desktop media query:

```css
@media (min-width: 1024px) {
    /* Card action zone */
    .card-actions {
        display: flex;
        align-items: center;
        gap: 0.375rem;
        padding: 0.75rem 1.25rem;
        border-top: 1px solid var(--border);
    }
    .card-input {
        flex: 1;
        min-width: 0;
        background: var(--bg-deep);
        border: 1px solid var(--border);
        border-radius: 6px;
        padding: 0.5rem 0.75rem;
        font-family: 'IBM Plex Mono', ui-monospace, monospace;
        font-size: 13px;
        color: var(--text-primary);
        outline: none;
    }
    .card-input:focus {
        border-color: var(--accent);
        box-shadow: 0 0 0 2px var(--accent-dim);
    }
    .card-input::placeholder {
        color: var(--text-dim);
    }
    .card-btn {
        background: var(--bg-elevated);
        border: 1px solid var(--border);
        border-radius: 6px;
        color: var(--text-secondary);
        cursor: pointer;
        padding: 0.5rem;
        display: flex;
        align-items: center;
        justify-content: center;
        transition: all 0.15s;
        flex-shrink: 0;
    }
    .card-btn:hover {
        background: var(--accent-dim);
        border-color: var(--accent);
        color: var(--text-primary);
    }
    .card-btn.card-stop:hover {
        background: rgba(239, 68, 68, 0.15);
        border-color: var(--error);
        color: var(--error);
    }

    /* Card choices */
    .card-choices {
        display: flex;
        gap: 0.5rem;
        padding: 0.5rem 1.25rem;
        border-top: 1px solid var(--border);
    }

    /* On desktop, don't open action bar on window click */
    .window {
        cursor: default;
    }
}
```

### Step 3: Add JS for card inline interactions

In `views/pages.templ`, add these functions in the IndexPage script block (before the existing action bar functions):

```javascript
// Desktop card inline actions
function handleCardInput(event, input) {
    if (event.key === 'Enter') {
        event.preventDefault();
        const target = input.dataset.target;
        sendCardInput(input, target);
    }
}

async function sendCardInput(el, target) {
    const input = el.closest('.card-actions')?.querySelector('.card-input') || el;
    const value = input.value?.trim();
    if (!value || !target) return;

    try {
        const response = await fetch(`/pane/${encodeURIComponent(target)}/send`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
            body: `input=${encodeURIComponent(value)}`
        });
        if (response.ok) {
            input.value = '';
        }
    } catch (err) {
        console.error('Card send error:', err);
    }
}

function sendCardStop(target) {
    fetch(`/pane/${encodeURIComponent(target)}/send`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: 'input=Escape&special=true'
    });
}

function sendCardChoice(target, num) {
    fetch(`/pane/${encodeURIComponent(target)}/send`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: `input=${num}&noenter=true`
    });
}

// On desktop, prevent window click from opening action bar
function selectWindow(el) {
    if (window.innerWidth >= 1024) return; // Desktop: no action bar
    selectWindowMobile(el);
}
```

Rename the existing `selectWindow` function to `selectWindowMobile` and have the new `selectWindow` dispatch based on viewport width.

### Step 4: Run templ generate and verify

Run: `templ generate`
Run: `go build .`

### Step 5: Test in browser

Desktop: verify card input fields are visible, typing + Enter sends input, stop button works, choice buttons appear when agent has choices.
Mobile: verify card actions are hidden, clicking window still opens action bar.

### Step 6: Commit

```bash
git add views/components.templ views/components_templ.go views/styles.templ views/styles_templ.go views/pages.templ views/pages_templ.go
git commit -m "feat(desktop): inline card actions with send, stop, and choice buttons"
```

---

## Task 4: Agent strip bar on pane page

**Files:**
- Modify: `views/components.templ` (new agentStrip component)
- Modify: `views/types.go` (add AgentStripData type)
- Modify: `views/pages.templ` (add strip to PanePage)
- Modify: `views/styles.templ` (strip CSS in panePageStyles)
- Modify: `server/server.go` (pass strip data to PanePage)

### Step 1: Add data types

In `views/types.go`, add:

```go
// AgentStripItem represents one agent in the strip bar
type AgentStripItem struct {
    Session   string
    Window    int
    Pane      int
    Name      string // display name (branch or process)
    Indicator string // attention, working, done, idle
    AgentType agents.AgentType
    Active    bool   // is this the currently viewed pane
}
```

Update `PaneData` to include strip items:

```go
type PaneData struct {
    // ... existing fields ...
    StripItems []AgentStripItem // All agents for strip bar
}
```

### Step 2: Add agentStrip component

In `views/components.templ`, add:

```go
// agentStrip renders the compact agent navigation strip for desktop pane view
templ AgentStrip(items []AgentStripItem) {
    <div class="agent-strip">
        for _, item := range items {
            if item.Active {
                <span class={ "strip-pill", "active", item.Indicator }>
                    <span class={ "strip-dot", item.Indicator }></span>
                    <span class="strip-name">{ item.Name }</span>
                </span>
            } else {
                <a href={ templ.SafeURL(fmt.Sprintf("/pane/%s:%d.%d", item.Session, item.Window, item.Pane)) }
                    class={ "strip-pill", item.Indicator }>
                    <span class={ "strip-dot", item.Indicator }></span>
                    <span class="strip-name">{ item.Name }</span>
                </a>
            }
        }
        <a href="/" class="strip-back" title="Back to dashboard">
            <svg width="14" height="14" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 12l2-2m0 0l7-7 7 7M5 10v10a1 1 0 001 1h3m10-11l2 2m-2-2v10a1 1 0 01-1 1h-3m-4 0h4"></path>
            </svg>
        </a>
    </div>
}
```

### Step 3: Add strip CSS

In `views/styles.templ`, inside panePageStyles, add base styles (hidden on mobile):

```css
.agent-strip {
    display: none;
}
```

In the pane page desktop media query:

```css
@media (min-width: 1024px) {
    .agent-strip {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.5rem 2rem;
        background: var(--bg-panel);
        border-bottom: 1px solid var(--border);
        overflow-x: auto;
    }
    .strip-pill {
        display: flex;
        align-items: center;
        gap: 0.375rem;
        padding: 0.375rem 0.75rem;
        border-radius: 6px;
        font-size: 12px;
        font-weight: 500;
        text-decoration: none;
        color: var(--text-secondary);
        background: var(--bg-elevated);
        border: 1px solid transparent;
        white-space: nowrap;
        transition: all 0.15s;
    }
    .strip-pill:hover {
        border-color: var(--border);
        color: var(--text-primary);
    }
    .strip-pill.active {
        background: var(--accent-dim);
        border-color: var(--accent);
        color: var(--accent);
    }
    .strip-pill.attention {
        border-color: var(--attention);
    }
    .strip-pill.attention:not(.active) {
        animation: pulse 2s ease-in-out infinite;
    }
    .strip-dot {
        width: 6px;
        height: 6px;
        border-radius: 50%;
        flex-shrink: 0;
    }
    .strip-dot.attention {
        background: var(--attention);
        box-shadow: 0 0 6px var(--attention-glow);
    }
    .strip-dot.working {
        background: var(--working);
        box-shadow: 0 0 4px var(--working-glow);
    }
    .strip-dot.done {
        background: var(--done);
    }
    .strip-dot.idle {
        background: var(--idle);
    }
    .strip-back {
        margin-left: auto;
        display: flex;
        align-items: center;
        padding: 0.375rem;
        border-radius: 6px;
        color: var(--text-dim);
        text-decoration: none;
        transition: all 0.15s;
    }
    .strip-back:hover {
        background: var(--bg-elevated);
        color: var(--text-primary);
    }
}
```

### Step 4: Add strip to PanePage template

In `views/pages.templ`, inside the PanePage templ, add the strip right after the opening `<body>` tag (before the header):

```go
if len(data.StripItems) > 0 {
    @AgentStrip(data.StripItems)
}
```

### Step 5: Populate strip data in server

In `server/server.go`, in the `handlePane` function, after building PaneData, iterate all sessions/windows to build StripItems:

```go
// Build agent strip items
stripItems := s.buildAgentStripItems(sessionName, windowIdx, paneIdx)
paneData.StripItems = stripItems
```

Add helper method to Server:

```go
func (s *Server) buildAgentStripItems(activeSession string, activeWindow, activePane int) []views.AgentStripItem {
    sessions := s.tmuxClient.ListSessions()
    var items []views.AgentStripItem

    for _, sess := range sessions {
        windows := s.tmuxClient.ListWindows(sess.Name)
        for _, win := range windows {
            pane := s.tmuxClient.GetPane(sess.Name, win.Index)
            agent := s.getAgent(pane)
            // Skip non-agent windows (no claude/amp process)
            if agent.Type() == agents.AgentGeneric {
                continue
            }
            output := s.tmuxClient.CapturePane(sess.Name, win.Index, pane.Index, 50)
            parseResult := agent.Parse(output)
            branch := s.getBranch(sess.Name, win.Index, pane)

            indicator := "idle"
            if parseResult.NeedsAttention() {
                indicator = "attention"
            } else if parseResult.Type == parser.TypeWorking {
                indicator = "working"
            } else if parseResult.Type == parser.TypeDone {
                indicator = "done"
            }

            displayName := branch
            if displayName == "" {
                displayName = pane.CurrentCommand
            }

            items = append(items, views.AgentStripItem{
                Session:   sess.Name,
                Window:    win.Index,
                Pane:      pane.Index,
                Name:      displayName,
                Indicator: indicator,
                AgentType: agent.Type(),
                Active:    sess.Name == activeSession && win.Index == activeWindow && pane.Index == activePane,
            })
        }
    }
    return items
}
```

Note: The exact method names for tmux client and agent detection may differ — check the existing `buildSessionsData` method in server.go and replicate its pattern for getting pane info, agent type, branch, and parse results.

### Step 6: Run templ generate and verify

Run: `templ generate`
Run: `go build .`

### Step 7: Test in browser

Navigate to a pane page on desktop. Verify:
- Agent strip appears below the header
- Current agent is highlighted
- Other agents show correct status dots
- Clicking another pill navigates to that pane
- Home icon returns to dashboard
- Strip is hidden on mobile

### Step 8: Commit

```bash
git add views/types.go views/components.templ views/components_templ.go views/pages.templ views/pages_templ.go views/styles.templ views/styles_templ.go server/server.go
git commit -m "feat(desktop): agent strip bar on pane page for quick navigation"
```

---

## Task 5: Keyboard shortcuts

**Files:**
- Modify: `views/pages.templ` (JS in IndexPage script block)

### Step 1: Add dashboard keyboard shortcuts

In the IndexPage script block in `views/pages.templ`, add a keydown handler:

```javascript
// Desktop keyboard shortcuts
document.addEventListener('keydown', function(e) {
    // Skip if typing in an input field
    if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') {
        if (e.key === 'Escape') {
            e.target.blur();
        }
        return;
    }

    // Only on desktop
    if (window.innerWidth < 1024) return;

    // 1-9: expand agent by position (navigate to pane view)
    if (e.key >= '1' && e.key <= '9') {
        const cards = document.querySelectorAll('.window');
        const idx = parseInt(e.key) - 1;
        if (idx < cards.length) {
            const card = cards[idx];
            const session = card.dataset.session;
            const win = card.dataset.window;
            const pane = card.dataset.pane || '0';
            window.location.href = `/pane/${encodeURIComponent(session)}:${win}.${pane}`;
        }
        return;
    }

    // / : focus search
    if (e.key === '/') {
        e.preventDefault();
        document.getElementById('searchInput')?.focus();
        return;
    }
});
```

### Step 2: Add pane page keyboard shortcuts

In `views/scripts.templ` (paneScripts), add to the existing keydown handler:

```javascript
// Strip navigation shortcuts (desktop only)
if (window.innerWidth >= 1024) {
    if (e.key === '[' || e.key === ']') {
        const pills = document.querySelectorAll('.strip-pill:not(.strip-back)');
        if (pills.length < 2) return;
        let activeIdx = -1;
        pills.forEach((p, i) => { if (p.classList.contains('active')) activeIdx = i; });
        if (activeIdx === -1) return;

        let nextIdx;
        if (e.key === '[') {
            nextIdx = activeIdx > 0 ? activeIdx - 1 : pills.length - 1;
        } else {
            nextIdx = activeIdx < pills.length - 1 ? activeIdx + 1 : 0;
        }
        const nextPill = pills[nextIdx];
        if (nextPill.href) window.location.href = nextPill.href;
        return;
    }
}
```

### Step 3: Run templ generate and verify

Run: `templ generate`
Run: `go build .`

### Step 4: Test in browser

Dashboard: press 1-9 to navigate to agents, / to focus search, Escape to blur inputs.
Pane page: press [ and ] to switch between agents in the strip.

### Step 5: Commit

```bash
git add views/pages.templ views/pages_templ.go views/scripts.templ views/scripts_templ.go
git commit -m "feat(desktop): keyboard shortcuts for dashboard and pane navigation"
```

---

## Task 6: Visual polish

**Files:**
- Modify: `views/styles.templ` (both indexPageStyles and panePageStyles)

### Step 1: Surface contrast and card depth

In indexPageStyles desktop media query, enhance card appearance:

```css
@media (min-width: 1024px) {
    /* Enhanced card depth */
    .window {
        box-shadow: 0 1px 3px rgba(0, 0, 0, 0.3), 0 0 0 1px rgba(255, 255, 255, 0.03);
    }
    .window:hover {
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4), 0 0 0 1px rgba(255, 255, 255, 0.05);
    }

    /* Attention cards glow on edge */
    .window.attention {
        border-left: 3px solid var(--attention);
        box-shadow: 0 1px 3px rgba(0, 0, 0, 0.3), -2px 0 12px var(--attention-glow);
    }
    .window.working {
        border-left: 3px solid var(--working);
    }
}
```

### Step 2: Light theme adjustments for desktop

```css
@media (min-width: 1024px) {
    [data-theme="light"] .window {
        box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08), 0 0 0 1px rgba(0, 0, 0, 0.05);
    }
    [data-theme="light"] .window:hover {
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.12), 0 0 0 1px rgba(0, 0, 0, 0.08);
    }
}
```

### Step 3: Run templ generate, build, test

Run: `templ generate`
Run: `go build .`
Test both dark and light themes on desktop.

### Step 4: Commit

```bash
git add views/styles.templ views/styles_templ.go
git commit -m "feat(desktop): visual polish — card depth, attention glow, light theme"
```

---

## Task 7: Final integration test and cleanup

### Step 1: Full browser test matrix

Test in desktop browser (1024px+):
- [ ] Dashboard: full-width layout, no narrow max-width
- [ ] Dashboard: sans-serif fonts for UI, monospace for terminal
- [ ] Dashboard: cards in grid, grouped by repo label
- [ ] Dashboard: card shows 8-10 lines of preview
- [ ] Dashboard: inline input/send/stop works on each card
- [ ] Dashboard: choice buttons appear and work
- [ ] Dashboard: keyboard shortcuts (1-9, /)
- [ ] Pane page: agent strip visible with all agents
- [ ] Pane page: current agent highlighted in strip
- [ ] Pane page: click pill navigates to other agent
- [ ] Pane page: [ ] shortcuts switch agents
- [ ] Pane page: home icon returns to dashboard

Test at mobile width (< 768px):
- [ ] Dashboard: unchanged mobile layout
- [ ] Dashboard: accordion sessions work
- [ ] Dashboard: action bar popup works
- [ ] Dashboard: card-actions hidden
- [ ] Pane page: agent strip hidden
- [ ] Pane page: back button works

### Step 2: Clean up any unused CSS

Check for CSS rules that are no longer used after the refactor (e.g., desktop action bar panel styles if replaced by inline cards). Remove dead code.

### Step 3: Final commit

```bash
git add -A
git commit -m "chore: clean up unused CSS from desktop UI redesign"
```
