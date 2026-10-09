import type { WSMeta } from '../api/types'

// An app on the alternate screen (pi, less, vim) has no tmux scrollback, so
// xterm has nothing to scroll; the app scrolls itself from wheel events (mouse
// tracking on) or PageUp/PageDown.
export type AltScrollMode = 'wheel' | 'page'

/** Screen pixels of drag or wheel travel per wheel notch sent to the app. */
export const PX_PER_NOTCH = 24
/** Screen pixels of travel per PageUp/PageDown. */
export const PX_PER_PAGE = 160

const WHEEL_LINE_PX = 40

/** Most steps one event may send, so a fast flick can't flood the pane. */
export const MAX_STEPS_PER_EVENT = 20

export function clampSteps(steps: number): number {
  return Math.sign(steps) * Math.min(Math.abs(steps), MAX_STEPS_PER_EVENT)
}

export function altScrollMode(
  meta: Pick<WSMeta, 'alternate_on' | 'mouse_on'> | null | undefined,
): AltScrollMode | null {
  if (!meta?.alternate_on) return null
  return meta.mouse_on ? 'wheel' : 'page'
}

export function pxPerStep(mode: AltScrollMode): number {
  return mode === 'wheel' ? PX_PER_NOTCH : PX_PER_PAGE
}

/** Folds `deltaPx` into `acc`; returns whole steps (positive = scroll down,
 *  toward newer content) and the remainder to carry. */
export function takeSteps(acc: number, deltaPx: number, stepPx: number): { steps: number; acc: number } {
  const total = acc + deltaPx
  const steps = Math.trunc(total / stepPx)
  return { steps, acc: total - steps * stepPx }
}

/** 1-based cell under a client point inside `rect`, clamped to the grid. */
export function cellAt(
  clientX: number,
  clientY: number,
  rect: { left: number; top: number; width: number; height: number },
  cols: number,
  rows: number,
): { col: number; row: number } {
  const fx = rect.width > 0 ? (clientX - rect.left) / rect.width : 0
  const fy = rect.height > 0 ? (clientY - rect.top) / rect.height : 0
  return {
    col: Math.min(cols, Math.max(1, Math.floor(fx * cols) + 1)),
    row: Math.min(rows, Math.max(1, Math.floor(fy * rows) + 1)),
  }
}

/** Bytes that scroll the app by `steps` (positive = down). */
export function scrollInput(
  mode: AltScrollMode,
  steps: number,
  cell: { col: number; row: number },
): string {
  if (steps === 0) return ''
  const n = Math.abs(steps)
  const unit =
    mode === 'wheel'
      ? `\x1b[<${steps < 0 ? 64 : 65};${cell.col};${cell.row}M`
      : steps < 0
        ? '\x1b[5~'
        : '\x1b[6~'
  return unit.repeat(n)
}

/** Pixel size of a wheel event, whatever its deltaMode. */
export function wheelDeltaPx(e: { deltaY: number; deltaMode: number }, pageHeightPx: number): number {
  if (e.deltaMode === 1) return e.deltaY * WHEEL_LINE_PX
  if (e.deltaMode === 2) return e.deltaY * pageHeightPx
  return e.deltaY
}
