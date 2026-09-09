import { useRef } from 'react'
import { computeScrubberGeometry } from './columnScrubberGeometry'

interface ColumnScrubberProps {
  termWidthPx: number
  viewportWidthPx: number
  translateX: number
  scale: number
  onScrub: (translateX: number) => void
}

/** Thin bar below the mobile terminal viewport showing what horizontal slice
 *  of the full pane width is visible, draggable to jump the pan directly. */
export function ColumnScrubber({ termWidthPx, viewportWidthPx, translateX, scale, onScrub }: ColumnScrubberProps) {
  const trackRef = useRef<HTMLDivElement>(null)
  const { visibleFraction, offsetFraction } = computeScrubberGeometry(termWidthPx, viewportWidthPx, translateX, scale)

  const dragTo = (clientX: number) => {
    const track = trackRef.current
    const totalPx = termWidthPx * scale
    if (!track || totalPx <= 0) return
    const rect = track.getBoundingClientRect()
    const trackW = rect.width || 1
    const pointerFraction = (clientX - rect.left) / trackW
    const targetOffsetFraction = Math.min(
      1 - visibleFraction,
      Math.max(0, pointerFraction - visibleFraction / 2),
    )
    onScrub(-targetOffsetFraction * totalPx)
  }

  return (
    <div
      ref={trackRef}
      onPointerDown={(e) => {
        e.currentTarget.setPointerCapture(e.pointerId)
        dragTo(e.clientX)
      }}
      onPointerMove={(e) => {
        if (e.buttons !== 1) return
        dragTo(e.clientX)
      }}
      style={{
        position: 'relative',
        flexShrink: 0,
        height: 6,
        margin: '0 6px 6px',
        borderRadius: 3,
        background: 'var(--bg-surface)',
        touchAction: 'none',
      }}
    >
      <div
        data-testid="column-scrubber-thumb"
        style={{
          position: 'absolute',
          top: 0,
          bottom: 0,
          left: `${offsetFraction * 100}%`,
          width: `${visibleFraction * 100}%`,
          borderRadius: 3,
          background: 'var(--text-secondary)',
        }}
      />
    </div>
  )
}
