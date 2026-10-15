import { describe, expect, it } from 'vitest'
import { writeSnapshot } from './terminalSnapshot'

// Like xterm, write() only queues: a chunk is parsed (and its callback run)
// later, in order, on flush(), while clear() acts on the buffer at once.
function fakeTerm() {
  const ops: string[] = []
  const eolWhenParsed: Array<[string, boolean]> = []
  const queue: Array<() => void> = []
  const term = {
    ops,
    eolWhenParsed,
    options: { convertEol: false },
    write(data: string | Uint8Array, cb?: () => void) {
      queue.push(() => {
        ops.push(`write:${data}`)
        eolWhenParsed.push([String(data), term.options.convertEol])
        cb?.()
      })
    },
    clear() {
      ops.push('clear')
    },
    flush() {
      while (queue.length) queue.shift()?.()
    },
  }
  return term
}

describe('writeSnapshot', () => {
  it('drops the scrollback of the previous seed before writing the new one', () => {
    // The seed carries the pane's own history, so a mid-session seed written on
    // top of the old scrollback shows every history line twice.
    const term = fakeTerm()
    writeSnapshot(term, 'a\nb\n', undefined)
    term.flush()
    const clear = term.ops.indexOf('clear')
    const seed = term.ops.findIndex((op) => op.startsWith('write:') && op.includes('a\nb'))
    expect(clear).toBeGreaterThanOrEqual(0)
    expect(seed).toBeGreaterThan(clear)
  })

  it('clears inside the write queue so earlier queued output cannot land after the clear', () => {
    const term = fakeTerm()
    term.write('queued output')
    writeSnapshot(term, 'seed', undefined)
    term.flush()
    expect(term.ops[0]).toBe('write:queued output')
    expect(term.ops.indexOf('clear')).toBeGreaterThan(0)
  })

  it('treats a bare newline as a line break only while a seed is parsed, even with two queued', () => {
    // xterm reads convertEol when it parses a chunk, so each seed's lines must
    // be parsed under it; a first seed's reset must not reach the second's.
    const term = fakeTerm()
    writeSnapshot(term, 'one', undefined)
    writeSnapshot(term, 'two', undefined)
    term.flush()
    expect(term.eolWhenParsed).toEqual([
      ['\x1b[?25l', false],
      ['\x1b[2J\x1b[Hone', true],
      ['\x1b[?25l', false],
      ['\x1b[2J\x1b[Htwo', true],
    ])
    expect(term.options.convertEol).toBe(false)
  })

  it("leaves the cursor where the pane's own cursor was", () => {
    // The lines are written one after another, which parks the cursor at the end
    // of the last one; a shell echoes keystrokes without positioning them.
    const term = fakeTerm()
    writeSnapshot(term, 'a\nb\n', { x: 6, y: 23 })
    term.flush()
    const seed = term.ops.find((op) => op.includes('a\nb'))
    expect(seed?.endsWith('b\x1b[24;7H')).toBe(true)
  })

  it('writes no cursor move when the server sent none', () => {
    const term = fakeTerm()
    writeSnapshot(term, 'a\nb\n', undefined)
    term.flush()
    expect(term.ops.find((op) => op.includes('a\nb'))).toBe('write:\x1b[2J\x1b[Ha\nb')
  })
})
