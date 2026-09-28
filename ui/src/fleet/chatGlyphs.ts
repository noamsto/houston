// Monochrome text glyphs only: none of these codepoints has an emoji
// presentation, and none needs a Nerd Font the phone won't have.

export const TOOL_KINDS = ['read', 'edit', 'delete', 'move', 'search', 'execute', 'think', 'fetch', 'other'] as const
export type ToolKind = (typeof TOOL_KINDS)[number]

export const KIND_GLYPH: Record<ToolKind, string> = {
  read: '▤',
  edit: '⎀',
  delete: '⌫',
  move: '⇄',
  search: '⌕',
  execute: '❯',
  think: '∴',
  fetch: '⇣',
  other: '◇',
}

export function kindGlyph(kind: string | undefined): string {
  return KIND_GLYPH[TOOL_KINDS.find((k) => k === kind) ?? 'other']
}

export function statusGlyph(status: string | undefined): string | undefined {
  if (status === 'completed') return '✓'
  if (status === 'failed') return '✗'
  if (status === 'pending' || status === 'in_progress') return '◌'
  return undefined
}
