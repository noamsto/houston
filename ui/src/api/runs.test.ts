import { describe, expect, it } from 'vitest'
import { paneWsTarget } from './runs'

describe('paneWsTarget', () => {
  it('double-encodes a percent-prefixed pane id', () => {
    expect(paneWsTarget('%307')).toBe('%2525307')
  })
})
