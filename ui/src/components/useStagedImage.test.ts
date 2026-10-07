import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { readBase64, useStagedImage } from './useStagedImage'

const png = (name: string) => new File(['x'], name, { type: 'image/png' })

let create: ReturnType<typeof vi.fn>
let revoke: ReturnType<typeof vi.fn>

beforeEach(() => {
  let n = 0
  create = vi.fn(() => `blob:preview-${++n}`)
  revoke = vi.fn()
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke }))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
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
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: undefined, revokeObjectURL: undefined }))
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
