import { Fragment, type ReactNode } from 'react'

type Block =
  | { type: 'code'; text: string }
  | { type: 'list'; ordered: boolean; items: string[] }
  | { type: 'paragraph'; lines: string[] }

const FENCE = /^```/
const UNORDERED = /^[-*] /
const ORDERED = /^\d+\. /

function parseBlocks(text: string): Block[] {
  const lines = text.split('\n')
  const blocks: Block[] = []
  let i = 0

  while (i < lines.length) {
    const line = lines[i]

    if (line.trim() === '') {
      i++
      continue
    }

    if (FENCE.test(line)) {
      i++ // the fence line itself (language tag, if any) is never shown
      const codeLines: string[] = []
      while (i < lines.length && !FENCE.test(lines[i])) {
        codeLines.push(lines[i])
        i++
      }
      if (i < lines.length) i++ // skip the closing fence, if one was found
      blocks.push({ type: 'code', text: codeLines.join('\n') })
      continue
    }

    if (UNORDERED.test(line)) {
      const items: string[] = []
      while (i < lines.length && UNORDERED.test(lines[i])) {
        items.push(lines[i].replace(UNORDERED, ''))
        i++
      }
      blocks.push({ type: 'list', ordered: false, items })
      continue
    }

    if (ORDERED.test(line)) {
      const items: string[] = []
      while (i < lines.length && ORDERED.test(lines[i])) {
        items.push(lines[i].replace(ORDERED, ''))
        i++
      }
      blocks.push({ type: 'list', ordered: true, items })
      continue
    }

    const paraLines: string[] = []
    while (i < lines.length && lines[i].trim() !== '' && !FENCE.test(lines[i]) && !UNORDERED.test(lines[i]) && !ORDERED.test(lines[i])) {
      paraLines.push(lines[i])
      i++
    }
    blocks.push({ type: 'paragraph', lines: paraLines })
  }

  return blocks
}

// Inline `code` spans within a line; an unmatched backtick is literal text,
// not code. Text always reaches JSX as plain string children (never raw
// HTML injection), so e.g. a literal <script> tag renders as an escaped
// text node.
function renderInline(text: string, keyPrefix: string): ReactNode[] {
  const nodes: ReactNode[] = []
  let buffer = ''
  let key = 0
  let i = 0

  const flush = () => {
    if (buffer) {
      nodes.push(buffer)
      buffer = ''
    }
  }

  while (i < text.length) {
    if (text[i] === '`') {
      const end = text.indexOf('`', i + 1)
      if (end === -1) {
        buffer += text.slice(i)
        break
      }
      flush()
      nodes.push(<code key={`${keyPrefix}-c${key++}`}>{text.slice(i + 1, end)}</code>)
      i = end + 1
      continue
    }
    buffer += text[i]
    i++
  }
  flush()

  return nodes
}

function Paragraph({ lines, keyPrefix }: { lines: string[]; keyPrefix: string }) {
  return (
    <p>
      {lines.map((line, idx) => (
        <Fragment key={idx}>
          {idx > 0 && <br />}
          {renderInline(line, `${keyPrefix}-${idx}`)}
        </Fragment>
      ))}
    </p>
  )
}

export function ChatMarkdown({ text }: { text: string }) {
  const blocks = parseBlocks(text)

  return (
    <>
      {blocks.map((block, idx) => {
        const key = `b${idx}`
        if (block.type === 'code') {
          return (
            <pre key={key}>
              <code>{block.text}</code>
            </pre>
          )
        }
        if (block.type === 'list') {
          const items = block.items.map((item, i) => <li key={i}>{renderInline(item, `${key}-${i}`)}</li>)
          return block.ordered ? <ol key={key}>{items}</ol> : <ul key={key}>{items}</ul>
        }
        return <Paragraph key={key} lines={block.lines} keyPrefix={key} />
      })}
    </>
  )
}
