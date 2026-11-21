import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { LinkifiedText } from './LinkifiedText'
import { PickerBlock, QuestionBlock } from './questionParts'
import { emptyStage } from './questionStage'

afterEach(cleanup)

function links(text: string): HTMLAnchorElement[] {
  const { container } = render(<LinkifiedText text={text} />)
  return Array.from(container.querySelectorAll('a'))
}
const hrefs = (text: string) => links(text).map((a) => a.getAttribute('href'))

describe('LinkifiedText', () => {
  it('leaves plain text unchanged', () => {
    const { container } = render(<LinkifiedText text="nothing to see here" />)
    expect(container.textContent).toBe('nothing to see here')
    expect(container.querySelector('a')).toBeNull()
  })

  it('links one URL mid-sentence and keeps the surrounding text', () => {
    const { container } = render(<LinkifiedText text="see https://x.y/a now" />)
    expect(container.textContent).toBe('see https://x.y/a now')
    expect(Array.from(container.querySelectorAll('a')).map((a) => a.getAttribute('href'))).toEqual(['https://x.y/a'])
  })

  it('excludes trailing punctuation', () => {
    expect(hrefs('go https://x.y/a.')).toEqual(['https://x.y/a'])
    expect(hrefs('go https://x.y/a, then')).toEqual(['https://x.y/a'])
    expect(hrefs('(see https://x.y/a)')).toEqual(['https://x.y/a'])
    expect(hrefs('"https://x.y/a"?')).toEqual(['https://x.y/a'])
  })

  it('excludes markdown emphasis and an ellipsis after the URL', () => {
    expect(hrefs('`https://x.y/a`')).toEqual(['https://x.y/a'])
    expect(hrefs('**https://x.y/a**')).toEqual(['https://x.y/a'])
    expect(hrefs('https://x.y/a…')).toEqual(['https://x.y/a'])
  })

  it('keeps a balanced closing paren', () => {
    expect(hrefs('(https://x.y/a_(b))')).toEqual(['https://x.y/a_(b)'])
  })

  it('does not link javascript: or data:', () => {
    expect(hrefs('javascript:alert(1) data:text/html,hi')).toEqual([])
  })

  it('links two URLs in one string', () => {
    expect(hrefs('a http://one.example b https://two.example')).toEqual(['http://one.example', 'https://two.example'])
  })

  it('opens every link in a new tab without opener access', () => {
    for (const a of links('a http://one.example b https://two.example')) {
      expect(a.getAttribute('target')).toBe('_blank')
      expect(a.getAttribute('rel')).toBe('noopener noreferrer')
    }
  })
})

describe('question surfaces', () => {
  const q = {
    question: 'Review page: https://claude.ai/artifact/abc.',
    multiSelect: false,
    preview: false,
    options: [{ label: 'Open', description: 'see https://x.y/doc' }],
  }

  it('links the question text and read-only option description', () => {
    const { container } = render(<QuestionBlock q={q} />)
    expect(container.querySelector('.chat-question-text a')?.getAttribute('href')).toBe('https://claude.ai/artifact/abc')
    expect(container.querySelector('.chat-question-option-desc a')?.getAttribute('href')).toBe('https://x.y/doc')
  })

  it('links the picker question but never inside a pick button', () => {
    const { container } = render(
      <PickerBlock q={q} stage={emptyStage()} disabled={false} onOption={() => {}} onOther={() => {}} onText={() => {}} />,
    )
    expect(container.querySelector('.chat-question-text a')).not.toBeNull()
    expect(screen.getAllByRole('radio').length).toBeGreaterThan(0)
    expect(container.querySelector('button a')).toBeNull()
  })
})
