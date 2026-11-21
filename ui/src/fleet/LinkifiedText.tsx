import { Fragment } from 'react'

const URL_RE = /https?:\/\/[^\s<>]+/gi
const OPENERS: Record<string, string> = { ')': '(', ']': '[' }

/** Trailing punctuation and unbalanced closers are prose, not URL. */
function trimUrl(raw: string): string {
  let url = raw
  for (;;) {
    const last = url[url.length - 1]
    if (/[.,;:!?'"]/.test(last)) {
      url = url.slice(0, -1)
      continue
    }
    const opener = OPENERS[last]
    if (opener) {
      const count = (c: string) => url.split(c).length - 1
      if (count(last) > count(opener)) {
        url = url.slice(0, -1)
        continue
      }
    }
    return url
  }
}

function isHttp(url: string): boolean {
  try {
    const { protocol } = new URL(url)
    return protocol === 'http:' || protocol === 'https:'
  } catch {
    return false
  }
}

/** Plain text with bare http(s) URLs as links. Never use inside a button or
 *  other interactive control. */
export function LinkifiedText({ text }: { text: string }) {
  const parts: React.ReactNode[] = []
  let last = 0
  for (const m of text.matchAll(URL_RE)) {
    const url = trimUrl(m[0])
    if (!isHttp(url)) continue
    if (m.index > last) parts.push(text.slice(last, m.index))
    parts.push(
      <a key={m.index} className="chat-link" href={url} target="_blank" rel="noopener noreferrer">
        {url}
      </a>,
    )
    last = m.index + url.length
  }
  if (last === 0) return <>{text}</>
  if (last < text.length) parts.push(text.slice(last))
  return (
    <>
      {parts.map((p, i) => (
        <Fragment key={i}>{p}</Fragment>
      ))}
    </>
  )
}
