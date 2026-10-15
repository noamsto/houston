import type { Terminal } from '@xterm/xterm'

/** Write a capture-pane snapshot into xterm.js.
 *  Clears scrollback + viewport (NOT term.reset() which would destroy terminal
 *  modes that the program set up before we connected). Content flows
 *  sequentially — natural scrolling moves earlier lines into the scrollback
 *  buffer, leaving the last term.rows lines visible. */
export interface SnapshotCursor {
  x: number
  y: number
}

/** `cursor` is the pane's 0-based position within its visible screen. */
export function writeSnapshot(
  term: Pick<Terminal, 'write' | 'clear' | 'options'>,
  data: string,
  cursor: SnapshotCursor | undefined,
  onDone?: () => void,
) {
  const lines = data.split('\n')
  if (lines[lines.length - 1] === '') lines.pop()

  // \x1b[?25l — hide cursor (program output will restore it)
  // The seed repeats the pane's history, so the previous seed's scrollback must
  // go first or every history line shows twice. term.clear() acts at once and
  // would run ahead of output still queued, so it rides the write queue;
  // \x1b[3J would do the same but shifts the viewport offset, rendering the
  // content one line low.
  // convertEol is read while xterm parses, not when write() is called, so it
  // is flipped in queue order too: a second seed queued behind this one must
  // not see the first one's reset.
  term.write('\x1b[?25l', () => {
    term.clear()
    term.options.convertEol = true
  })

  // \x1b[2J — clear viewport (doesn't affect terminal modes)
  // \x1b[H  — cursor to (1,1)
  // Writing the lines leaves the cursor at the end of the last one, so move it
  // to where the pane's cursor is (1-based, within the viewport).
  const park = cursor ? `\x1b[${cursor.y + 1};${cursor.x + 1}H` : ''
  term.write('\x1b[2J\x1b[H' + lines.join('\n') + park, () => {
    term.options.convertEol = false
    onDone?.()
  })
}
