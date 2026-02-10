# Desktop UI Redesign

## Problem

The current desktop UI is a mobile-first design stretched to wider screens. Narrow max-width containers, accordion session lists, and a popup action bar feel like a phone app on a monitor.

## Goals

- Full picture of all working agents at a glance
- Quick actions without leaving the overview
- In-depth terminal view when needed, without losing context
- Desktop-native feel: proper typography, spacing, surfaces

## User Mental Model

- Session = repository
- Window = worktree/branch
- Typical usage: 4-5 agents running simultaneously

## Design

### Layout: Panel Grid with Inline Expansion

Full-width layout (no narrow max-width on desktop). Agent cards in a responsive grid:

- 2 columns on desktop (1024-1600px)
- 3 columns on wide screens (1600px+)
- 1 column on mobile/tablet (unchanged)

Cards are grouped by session/repo via subtle section headers (dim uppercase label, no accordion). All cards always visible — no collapse/expand for sessions.

Sort order: attention first, working next, idle last. No labeled section dividers ("Needs attention" / "Active" / "Idle") — status dots and colors communicate priority.

### Card Anatomy

Each card has three zones:

**Header:** Status dot (color + animation), worktree/branch name, agent icon (Claude/Amp), window index in dim text.

**Output:** 8-10 lines of terminal preview with ANSI colors. Monospace font, recessed dark background. Status text above preview (e.g. "Reading file...", "Waiting for input"). Attention state shown via border/glow shift.

**Actions:** Compact single-line input field, send button, stop button, expand button. When choices are available, choice buttons replace the input field temporarily.

Quick interactions happen directly on the card — no popup or modal.

### Inline Expansion (Deep Dive)

Clicking "Expand" (or double-click, or pressing 1-9 keys) transitions a card to full-width expanded state:

- Card spans all grid columns
- Other cards collapse to a **compact strip** across the top — one pill per agent showing: status dot, branch name, one-word status
- The strip is ~40px tall; expanded terminal gets the rest of the viewport
- Click any pill to switch agents (current collapses, clicked one expands)

Expanded state includes all current pane page features:

- Scrollable terminal output (~70-80vh)
- Full input bar: multi-line textarea, image attachments, mode indicators
- Status bar (Claude status line or Amp ctx/cost/mode)
- Suggestion bar, pending input bar, choices bar

Press Escape or click "Collapse" to return to grid.

The `/pane/` route still works as a standalone page for direct links.

### SSE Connections

Each card maintains a lightweight SSE connection for preview. On expand, it upgrades to the full SSE stream with mode/choices/status parsing. On collapse, it drops back to lightweight.

### Visual Design

**Typography split:**
- UI elements (headers, labels, buttons): system sans-serif (`-apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif`)
- Terminal output only: monospace (`'SF Mono', ui-monospace, monospace`)

**Surfaces:** Increase contrast between background and cards. Cards get subtle shadow (`0 1px 3px rgba(0,0,0,0.4)`) plus border. Terminal preview inset gets recessed background with 1px inset border.

**Spacing:** Generous padding on desktop — `1.25-1.5rem` card internal padding, `1.5-2rem` grid gap.

**Status colors:** Keep current palette (orange/blue/green/gray). Attention state gets more pronounced glow/gradient on card edge for peripheral visibility.

**Scan lines:** Remove on desktop, keep on mobile if desired.

### Keyboard Navigation

**Grid view:**
- `1-9` — expand agent by position
- `/` — focus search input
- `?` — shortcut help overlay

**Expanded view:**
- `Escape` — collapse to grid
- `[` / `]` — switch to prev/next agent
- Focus auto-lands in input field
- Existing pane shortcuts carry over (Cmd+Enter, Shift+Tab, etc.)

### Session/Repo Display

Grid view: repo name as lightweight section header (dim uppercase text with subtle line). Cards from same session sit adjacent.

Collapsed strip: flat pills, no grouping. Branch name sufficient to identify each agent. Repo name shown in expanded card header (e.g. `houston / feat/desktop-ui`).

## Scope

**Changes:**
- `views/styles.templ` — desktop CSS: full-width layout, grid, card zones, expansion states, strip bar, typography split, surface contrast
- `views/pages.templ` / `views/components.templ` — new card component, collapsed strip, inline expansion, flat session headers
- `views/scripts.templ` — keyboard shortcuts, expand/collapse logic, SSE upgrade/downgrade, card reordering

**Unchanged:**
- Mobile layout (below 1024px)
- Pane page (`/pane/` route)
- SSE/streaming infrastructure
- ANSI processing, agent detection, choice/suggestion/pending input logic
- Server routes, Go backend

## Principles

- KISS: minimal new abstractions, CSS-driven where possible
- DRY: shared styles/components between card and expanded states
- Idiomatic Go: templ components with clear props, no over-abstraction
- Maintainability: desktop styles in clearly separated media query blocks
