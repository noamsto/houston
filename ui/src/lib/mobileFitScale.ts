// Mobile scale/floor for the terminal's CSS transform. `floor` is the
// pinch-zoom-out lower bound; `scale` is the default displayed scale.
// In landscape (short viewport), shrinks below 1x as needed so the full
// pane height fits without cropping; otherwise reproduces the existing
// "1x readable, floor at width-fit ratio" behavior exactly.
export function mobileFitScale(outerW: number, outerH: number, screenW: number, screenH: number) {
  const widthFloor = screenW > 0 ? outerW / screenW : 1
  const heightFit = screenH > 0 ? outerH / screenH : 1
  const landscape = window.matchMedia('(orientation: landscape) and (max-width: 1023px)').matches
  const floor = landscape ? Math.min(widthFloor, heightFit) : widthFloor
  const scale = landscape ? Math.max(floor, Math.min(1.0, heightFit)) : Math.max(1.0, widthFloor)
  return { floor, scale }
}
