import { describe, expect, it } from 'vitest'
import {
  altScrollMode,
  cellAt,
  clampSteps,
  PX_PER_NOTCH,
  scrollInput,
  takeSteps,
  wheelDeltaPx,
} from './altScroll'

describe('altScrollMode', () => {
  it('leaves the normal screen alone', () => {
    expect(altScrollMode(null)).toBeNull()
    expect(altScrollMode({})).toBeNull()
    expect(altScrollMode({ mouse_on: true })).toBeNull()
  })
  it('forwards wheel events when the app tracks the mouse', () => {
    expect(altScrollMode({ alternate_on: true, mouse_on: true })).toBe('wheel')
  })
  it('falls back to paging without mouse tracking', () => {
    expect(altScrollMode({ alternate_on: true })).toBe('page')
  })
})

describe('takeSteps', () => {
  it('carries the remainder between moves', () => {
    let r = takeSteps(0, 10, PX_PER_NOTCH)
    expect(r.steps).toBe(0)
    r = takeSteps(r.acc, 20, PX_PER_NOTCH)
    expect(r.steps).toBe(1)
    expect(r.acc).toBe(6)
  })
  it('counts upward travel as negative steps', () => {
    expect(takeSteps(0, -55, 24).steps).toBe(-2)
  })
})

describe('cellAt', () => {
  const rect = { left: 10, top: 20, width: 800, height: 400 }
  it('is 1-based under the point', () => {
    expect(cellAt(10, 20, rect, 80, 20)).toEqual({ col: 1, row: 1 })
    expect(cellAt(410, 220, rect, 80, 20)).toEqual({ col: 41, row: 11 })
  })
  it('clamps points outside the grid', () => {
    expect(cellAt(5000, 5000, rect, 80, 20)).toEqual({ col: 80, row: 20 })
    expect(cellAt(0, 0, rect, 80, 20)).toEqual({ col: 1, row: 1 })
  })
})

describe('scrollInput', () => {
  const cell = { col: 12, row: 3 }
  it('emits SGR wheel down and up', () => {
    expect(scrollInput('wheel', 2, cell)).toBe('\x1b[<65;12;3M\x1b[<65;12;3M')
    expect(scrollInput('wheel', -1, cell)).toBe('\x1b[<64;12;3M')
  })
  it('emits PageDown/PageUp when paging', () => {
    expect(scrollInput('page', 1, cell)).toBe('\x1b[6~')
    expect(scrollInput('page', -2, cell)).toBe('\x1b[5~\x1b[5~')
  })
  it('emits nothing for zero steps', () => {
    expect(scrollInput('wheel', 0, cell)).toBe('')
  })
})

describe('wheelDeltaPx', () => {
  it('normalises line and page modes to pixels', () => {
    expect(wheelDeltaPx({ deltaY: 100, deltaMode: 0 }, 500)).toBe(100)
    expect(wheelDeltaPx({ deltaY: 3, deltaMode: 1 }, 500)).toBe(120)
    expect(wheelDeltaPx({ deltaY: 1, deltaMode: 2 }, 500)).toBe(500)
  })
})

describe('clampSteps', () => {
  it('caps a flick in either direction', () => {
    expect(clampSteps(500)).toBe(20)
    expect(clampSteps(-500)).toBe(-20)
    expect(clampSteps(3)).toBe(3)
  })
})
