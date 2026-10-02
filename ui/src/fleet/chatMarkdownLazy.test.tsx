import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
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
    const list = await screen.findByRole('list')
    expect(list.querySelectorAll('li')).toHaveLength(2)
    expect(container.querySelector('.chat-md')).toBeTruthy()
  })
})
