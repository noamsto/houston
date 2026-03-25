# Sidebar Redesign: Compact Eye-Catching Mobile-First Layout

## Problem

The sidebar is not informative enough on mobile. Specific issues:

1. **Raw tmux escape codes leak into window names** — `#[fg=#94e2d5]▊#[fg=default]` rendered as literal text
2. **Too many redundant lines per window** — branch, window name, and directory often repeat the same info across 3-4 lines
3. **Visual hierarchy is weak** — tiny 6px dots and 10px agent icons don't catch the eye on a phone screen
4. **Status is easy to miss** — small colored text below the window name

## Design

### Window Row — Compact Two-Line with Colored Accent Bar

Each window is a two-line row with a 3px colored left border:

```
┌──╴colored 3px left border
│ pl-642-run-sdk-tests-against-bo...     ← branch name, 13px, primary color
│ mono  ⌁ Waiting for choice             ← dir (muted) + status pill (colored)
└────────────────────────────────────
```

**Line 1 — Identity:**
- Branch name as primary text (white/primary, 13px, single-line ellipsis)
- Falls back to window name when branch is `main`, `master`, or empty (non-git directory)

**Line 2 — Context:**
- Project directory name (muted, 11px) — extracted from path, shown only when different from session name
- Status pill (colored background, rounded, 10px bold text) — shown only when there's a meaningful status. Truncated with ellipsis if activity text is long.

**Left accent border:**
- 3px wide, colored by status
- Yellow (`--accent-attention`) for attention/question/choice
- Blue (`--accent-working`) for working
- Green (`--accent-done`) for done
- Gray (`var(--border)`) for idle

**Attention rows:**
- Subtle yellow background tint: `rgba(245, 158, 11, 0.06)` (matches `--accent-attention`)
- Keeps existing attention pulse animation on the session group

**Removed elements:**
- Agent icons (`✦`, `⚡`) — noise on mobile, not in user's top 3 priorities
- Status dots — replaced by the much more visible left border
- Redundant sub-lines for window name, branch, directory — consolidated into 2 lines
- Expand/collapse arrows for single-window sessions

### Session Headers

- **Multi-window sessions**: Session name + window count as a section divider: `mono (4)`
- **Single-window sessions**: Still show session name as header (for attention pulse + badge), but no expand/collapse arrow
- **Attention badge**: Orange count pill stays on multi-window session headers

### Status Pills

| Status | Background | Text Color | Label |
|--------|-----------|------------|-------|
| question | `--accent-attention` | `#000` | `Waiting for input` |
| choice | `--accent-attention` | `#000` | `Waiting for choice` |
| error | `--accent-error` | `#fff` | `Error` |
| working | `--accent-working` | `#fff` | Activity text or `Working...` |
| done | transparent | `--accent-done` | `Done` |
| idle | — | — | No pill shown |

### Tmux Escape Code Stripping (Backend)

Strip `#[...]` sequences from all tmux text fields — window names, session names, and pane titles.

Regex: `#\[[^\]]*\]`

Apply via a `StripTmuxEscapes()` helper used in `ListWindows`, `ListSessions`, and `ListPanes`.

## Files to Change

### Backend
- `tmux/client.go` — Add `stripTmuxEscapes()` function, apply to window name in `ListWindows`

### Frontend
- `ui/src/components/SessionTree.tsx` — Rewrite `WindowRow` and `SessionRow` with new layout
- `ui/src/theme/tokens.css` — Add CSS variables if needed for pill styles

## Out of Scope

- Desktop-specific layout changes (compact layout applies everywhere — shared component)
- Ctrl/Cmd+click to split pane behavior is preserved, just not documented here
- Agent type display (removed from sidebar, still shown in pane header)
- Filter input changes
- Session grouping logic (ATTENTION/ACTIVE/IDLE stays as-is)
