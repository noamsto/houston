const COMPOSER_MAX_VIEWPORT_FRACTION = 0.4

// visualViewport already excludes the on-screen keyboard, so the cap tracks it.
export function composerMaxHeight(viewportHeight: number): number {
  return Math.round(viewportHeight * COMPOSER_MAX_VIEWPORT_FRACTION)
}
