import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { ChatMarkdownLazy, ChatPlainText } from './chatMarkdownLazy'

afterEach(() => {
  cleanup()
})

describe('ChatPlainText', () => {
  it('renders text as plain text, preserving line breaks and whitespace', () => {
    const { container } = render(<ChatPlainText text={'line one\n  indented'} />)
    const span = container.querySelector('.chat-plain')!
    expect(span.textContent).toBe('line one\n  indented')
    expect(span.querySelector('p')).toBeNull()
  })
})

describe('ChatMarkdownLazy', () => {
  it('renders markdown once the chunk resolves', async () => {
    const { container } = render(<ChatMarkdownLazy text={'- one\n- two'} />)
    expect(container.querySelector('.chat-plain')?.textContent).toBe('- one\n- two')
    const list = await screen.findByRole('list')
    expect(list.querySelectorAll('li')).toHaveLength(2)
    expect(container.querySelector('.chat-md')).toBeTruthy()
  })

  it('falls back to plain text when the chunk fails to load', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      vi.resetModules()
      vi.doMock('./chatMarkdown', () => {
        throw new Error('chunk load failed')
      })
      const mod = await import('./chatMarkdownLazy')
      const { container } = render(<mod.ChatMarkdownLazy text={'- one'} />)
      await waitFor(() => expect(spy.mock.calls.flat().some((a) => String(a).includes('ChunkErrorBoundary'))).toBe(true))
      expect(container.querySelector('.chat-plain')?.textContent).toBe('- one')
      expect(container.querySelector('.chat-md')).toBeNull()
    } finally {
      spy.mockRestore()
      vi.doUnmock('./chatMarkdown')
    }
  })
})
