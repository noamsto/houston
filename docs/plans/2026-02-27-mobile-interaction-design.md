# Mobile Interaction Improvements

## Problems

1. **No quick actions** — can't send Ctrl+C, Enter, Y/N, numbers, arrows without typing
2. **Zoom on input focus** — iOS auto-zooms because input font-size is 14px (< 16px threshold)
3. **No multiline input** — single-line `<input>` makes composing longer messages awkward
4. **Touch scrolling broken** — terminal content can't be scrolled by touch drag
5. **Sidebar bleeds through** — mobile overlay doesn't fully cover the terminal content

## Changes

### 1. Viewport meta (`ui/index.html`)

```html
<meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1, user-scalable=no" />
```

Prevents pinch-to-zoom and auto-zoom on input focus entirely.

### 2. Quick action bar (`MobileInputBar.tsx`)

Horizontal scrollable row of pill buttons above the text input:

```
[1] [2] [3] [4] [5] [Ctrl+C] [Enter] [Y] [N] [↑] [↓]
```

- Numbers 1-5: send literal digit + Enter via REST (`/api/pane/{target}/send`)
- `Y` / `N`: send literal + Enter
- `Enter`: send bare Enter (special key, no text)
- `Ctrl+C`: send via `special=true` param as `C-c`
- `↑` / `↓`: send via `special=true` as `Up` / `Down`
- Compact pill styling, horizontally scrollable with `-webkit-overflow-scrolling: touch`
- Agent choice buttons appear ABOVE the quick action bar (existing behavior preserved)

### 3. Multiline textarea (`MobileInputBar.tsx`)

Replace `<input type="text">` with `<textarea>`:

- Starts at 1 row, auto-grows to max 4 rows based on content
- `fontSize: 16` (prevents iOS auto-zoom)
- **Enter** sends the message (existing behavior)
- **Shift+Enter** inserts a newline
- Reset height to 1 row after sending

### 4. Touch scrolling (`TerminalPane.tsx`)

xterm.js supports touch scrolling when `disableStdin: true` (mobile mode).
The existing scroll-deferral logic (pause writes when scrolled up) already
handles wheel events via the viewport scroll listener — this should also
work for touch scrolling since both trigger the same DOM scroll event.

Verify and fix if needed: ensure `touch-action: pan-y` is set on the
xterm container so the browser doesn't intercept touch events for zoom.

### 5. Sidebar full-cover (`Sidebar.tsx`)

Add `top: 0, left: 0` to the fixed-position mobile sidebar so it anchors
to the viewport edge and doesn't leave gaps.

## Layout (mobile)

```
┌─────────────────────────────────────┐
│ ☰  houston                          │  header
├─────────────────────────────────────┤
│                                     │
│  [terminal output — touch scroll]   │  xterm.js
│                                     │
├─────────────────────────────────────┤
│ [Yes] [No] [Retry]                  │  agent choices (when present)
├─────────────────────────────────────┤
│ [1][2][3][4][5][^C][⏎][Y][N][↑][↓] │  quick actions (scrollable)
├─────────────────────────────────────┤
│ [textarea...              ] [🎤] [↵]│  input bar
└─────────────────────────────────────┘
```

## Files to modify

| File | Change |
|------|--------|
| `ui/index.html` | viewport meta |
| `ui/src/components/MobileInputBar.tsx` | quick actions, textarea, font-size |
| `ui/src/components/TerminalPane.tsx` | touch-action CSS on xterm container |
| `ui/src/components/Sidebar.tsx` | top/left on mobile fixed position |
