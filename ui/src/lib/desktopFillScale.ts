// Desktop scale for the terminal's CSS transform: the largest uniform scale
// that keeps the terminal's fixed native pixel size (cols/rows locked to the
// real tmux pane — houston never resizes a pane a human may be attached to)
// entirely within the outer container. Uniform (not per-axis) so monospace
// glyphs never distort; the tighter axis may still leave a margin when the
// container's aspect ratio doesn't match the terminal's.
export function desktopFillScale(outerW: number, outerH: number, screenW: number, screenH: number): number {
  if (outerW <= 0 || outerH <= 0 || screenW <= 0 || screenH <= 0) return 1
  return Math.min(outerW / screenW, outerH / screenH)
}
