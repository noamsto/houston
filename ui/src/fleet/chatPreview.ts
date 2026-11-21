const MAX_LINES = 8
const MAX_CHARS = 600
const PREVIEW_LINES = 6
const PREVIEW_CHARS = 400

export function previewText(text: string): { preview: string; truncated: boolean } {
  const lines = text.split('\n')
  if (lines.length <= MAX_LINES && text.length <= MAX_CHARS) return { preview: text, truncated: false }
  let head = lines.slice(0, PREVIEW_LINES).join('\n')
  if (head.length > PREVIEW_CHARS) {
    head = head.slice(0, PREVIEW_CHARS + 1)
    const cut = head.search(/\s\S*$/)
    head = cut > 0 ? head.slice(0, cut) : head.slice(0, PREVIEW_CHARS).replace(/[\uD800-\uDBFF]$/, '')
  }
  return { preview: head.trimEnd() + '…', truncated: true }
}
