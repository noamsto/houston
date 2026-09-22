import { describe, expect, it } from 'vitest'
import { desktopFillScale } from './desktopFillScale'

describe('desktopFillScale', () => {
  it('fills exactly when aspect ratios match', () => {
    expect(desktopFillScale(800, 600, 400, 300)).toBe(2)
  })

  it('picks the tighter ratio when aspect ratios mismatch, avoiding distortion', () => {
    expect(desktopFillScale(800, 200, 400, 400)).toBe(0.5)
  })

  it('scales below 1 when the container is smaller than the terminal', () => {
    expect(desktopFillScale(200, 200, 400, 400)).toBe(0.5)
  })

  it('falls back to 1 when screenW is 0', () => {
    expect(desktopFillScale(800, 600, 0, 300)).toBe(1)
  })

  it('falls back to 1 when screenH is 0', () => {
    expect(desktopFillScale(800, 600, 400, 0)).toBe(1)
  })

  it('falls back to 1 (never negative) when outerW is negative', () => {
    const result = desktopFillScale(-12, 600, 400, 300)
    expect(result).toBe(1)
    expect(result).toBeGreaterThanOrEqual(0)
  })

  it('falls back to 1 (never negative) when outerH is negative', () => {
    const result = desktopFillScale(800, -12, 400, 300)
    expect(result).toBe(1)
    expect(result).toBeGreaterThanOrEqual(0)
  })
})
