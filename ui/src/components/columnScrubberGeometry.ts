/** What fraction of the pane's full width is visible, and at what offset
 *  fraction, given its real pixel width, the viewport width, and the current
 *  pan/zoom transform. Pure so it's testable without mounting anything. */
export function computeScrubberGeometry(
  termWidthPx: number,
  viewportWidthPx: number,
  translateX: number,
  scale: number,
) {
  const totalPx = termWidthPx * scale
  if (totalPx <= 0) return { visibleFraction: 1, offsetFraction: 0 }
  const visibleFraction = Math.min(1, viewportWidthPx / totalPx)
  const offsetFraction = Math.min(1 - visibleFraction, Math.max(0, -translateX / totalPx))
  return { visibleFraction, offsetFraction }
}
