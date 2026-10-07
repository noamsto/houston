import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { readBase64, useStagedImage } from './useStagedImage'

const png = (name: string) => new File(['x'], name, { type: 'image/png' })

type UrlFn = 'createObjectURL' | 'revokeObjectURL'
const urlFns: UrlFn[] = ['createObjectURL', 'revokeObjectURL']

let revoke: ReturnType<typeof vi.fn>
let added: UrlFn[]

beforeEach(() => {
  let n = 0
  // happy-dom may lack these; spyOn needs an existing property.
  added = urlFns.filter((name) => typeof URL[name] !== 'function')
  for (const name of added) Object.defineProperty(URL, name, { value: () => '', configurable: true, writable: true })
  vi.spyOn(URL, 'createObjectURL').mockImplementation(() => `blob:preview-${++n}`)
  revoke = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  for (const name of added) delete (URL as Partial<typeof URL>)[name]
})

describe('useStagedImage', () => {
  it('starts with nothing staged', () => {
    const { result } = renderHook(() => useStagedImage())
    expect(result.current.staged).toBeNull()
  })

  it('stage exposes the file and a preview url', () => {
    const { result } = renderHook(() => useStagedImage())
    const file = png('a.png')
    act(() => result.current.stage(file))
    expect(result.current.staged).toEqual({ file, previewUrl: 'blob:preview-1' })
  })

  it('clear drops the stage and revokes the url', () => {
    const { result } = renderHook(() => useStagedImage())
    act(() => result.current.stage(png('a.png')))
    act(() => result.current.clear())
    expect(result.current.staged).toBeNull()
    expect(revoke).toHaveBeenCalledWith('blob:preview-1')
  })

  it('staging again revokes the previous url', () => {
    const { result } = renderHook(() => useStagedImage())
    act(() => result.current.stage(png('a.png')))
    act(() => result.current.stage(png('b.png')))
    expect(revoke).toHaveBeenCalledWith('blob:preview-1')
    expect(result.current.staged?.previewUrl).toBe('blob:preview-2')
    expect(revoke).not.toHaveBeenCalledWith('blob:preview-2')
  })

  it('revokes the url on unmount', () => {
    const { result, unmount } = renderHook(() => useStagedImage())
    act(() => result.current.stage(png('a.png')))
    unmount()
    expect(revoke).toHaveBeenCalledWith('blob:preview-1')
  })

  it('uses a null preview when createObjectURL is unavailable', () => {
    Object.defineProperty(URL, 'createObjectURL', { value: undefined, configurable: true, writable: true })
    const { result, unmount } = renderHook(() => useStagedImage())
    const file = png('a.png')
    act(() => result.current.stage(file))
    expect(result.current.staged).toEqual({ file, previewUrl: null })
    act(() => result.current.clear())
    unmount()
  })
})

describe('readBase64', () => {
  it('returns the file bytes as bare base64', async () => {
    expect(await readBase64(new File(['hello'], 'h.txt', { type: 'text/plain' }))).toBe('aGVsbG8=')
  })
})
