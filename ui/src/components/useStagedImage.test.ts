import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, renderHook } from '@testing-library/react'
import { MAX_STAGED_BYTES, readBase64, skippedMessage, useStagedImage } from './useStagedImage'

const png = (name: string) => new File(['x'], name, { type: 'image/png' })
// A File whose reported size is what the cap check reads, without allocating it.
const sized = (name: string, size: number) => {
  const f = png(name)
  Object.defineProperty(f, 'size', { value: size })
  return f
}

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
    expect(result.current.staged).toEqual([])
  })

  it('stage exposes each file with a preview url and an id', () => {
    const { result } = renderHook(() => useStagedImage())
    const a = png('a.png')
    const b = png('b.png')
    act(() => {
      result.current.stage([a, b])
    })
    expect(result.current.staged).toEqual([
      { id: 0, file: a, previewUrl: 'blob:preview-1' },
      { id: 1, file: b, previewUrl: 'blob:preview-2' },
    ])
  })

  it('staging twice accumulates and keeps every url alive', () => {
    const { result } = renderHook(() => useStagedImage())
    act(() => {
      result.current.stage([png('a.png')])
    })
    act(() => {
      result.current.stage([png('b.png')])
    })
    expect(result.current.staged.map((s) => s.file.name)).toEqual(['a.png', 'b.png'])
    expect(revoke).not.toHaveBeenCalled()
  })

  it('remove drops one image and revokes only its url', () => {
    const { result } = renderHook(() => useStagedImage())
    act(() => {
      result.current.stage([png('a.png'), png('b.png')])
    })
    act(() => result.current.remove(result.current.staged[0].id))
    expect(result.current.staged.map((s) => s.file.name)).toEqual(['b.png'])
    expect(revoke).toHaveBeenCalledTimes(1)
    expect(revoke).toHaveBeenCalledWith('blob:preview-1')
  })

  it('clear drops everything and revokes every url', () => {
    const { result } = renderHook(() => useStagedImage())
    act(() => {
      result.current.stage([png('a.png'), png('b.png')])
    })
    act(() => result.current.clear())
    expect(result.current.staged).toEqual([])
    expect(revoke).toHaveBeenCalledWith('blob:preview-1')
    expect(revoke).toHaveBeenCalledWith('blob:preview-2')
  })

  it('revokes the remaining urls on unmount', () => {
    const { result, unmount } = renderHook(() => useStagedImage())
    act(() => {
      result.current.stage([png('a.png'), png('b.png')])
    })
    act(() => result.current.remove(result.current.staged[0].id))
    revoke.mockClear()
    unmount()
    expect(revoke).toHaveBeenCalledTimes(1)
    expect(revoke).toHaveBeenCalledWith('blob:preview-2')
  })

  it('refuses a file past the running total and keeps the earlier ones', () => {
    const { result } = renderHook(() => useStagedImage())
    const half = MAX_STAGED_BYTES / 2
    let skipped = -1
    act(() => {
      skipped = result.current.stage([sized('a.png', half), sized('b.png', half), sized('c.png', 1)])
    })
    expect(skipped).toBe(1)
    expect(result.current.staged.map((s) => s.file.name)).toEqual(['a.png', 'b.png'])
    act(() => {
      skipped = result.current.stage([sized('d.png', 1)])
    })
    expect(skipped).toBe(1)
    expect(result.current.staged).toHaveLength(2)
  })

  it('counts earlier picks toward the cap', () => {
    const { result } = renderHook(() => useStagedImage())
    act(() => {
      result.current.stage([sized('a.png', MAX_STAGED_BYTES)])
    })
    let skipped = -1
    act(() => {
      skipped = result.current.stage([png('b.png')])
    })
    expect(skipped).toBe(1)
  })

  it('uses a null preview when createObjectURL is unavailable', () => {
    Object.defineProperty(URL, 'createObjectURL', { value: undefined, configurable: true, writable: true })
    const { result, unmount } = renderHook(() => useStagedImage())
    const file = png('a.png')
    act(() => {
      result.current.stage([file])
    })
    expect(result.current.staged).toEqual([{ id: 0, file, previewUrl: null }])
    act(() => result.current.clear())
    unmount()
  })
})

describe('skippedMessage', () => {
  it('names the count and the limit', () => {
    expect(skippedMessage(1)).toBe('1 file skipped — attachments are limited to 36 MiB in total')
    expect(skippedMessage(3)).toBe('3 files skipped — attachments are limited to 36 MiB in total')
  })
})

describe('readBase64', () => {
  it('returns the file bytes as bare base64', async () => {
    expect(await readBase64(new File(['hello'], 'h.txt', { type: 'text/plain' }))).toBe('aGVsbG8=')
  })
})
