import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render } from '@testing-library/react'
import { ChatMarkdown } from './chatMarkdown'

afterEach(() => {
  cleanup()
})

describe('ChatMarkdown', () => {
  it('renders nothing for empty input', () => {
    const { container } = render(<ChatMarkdown text="" />)
    expect(container.innerHTML).toBe('')
  })

  it('renders a single line as one paragraph', () => {
    const { container } = render(<ChatMarkdown text="hello world" />)
    expect(container.querySelectorAll('p')).toHaveLength(1)
    expect(container.textContent).toBe('hello world')
  })

  it('splits blank-line-separated text into separate paragraphs', () => {
    const { container } = render(<ChatMarkdown text={'first paragraph\n\nsecond paragraph'} />)
    const paragraphs = container.querySelectorAll('p')
    expect(paragraphs).toHaveLength(2)
    expect(paragraphs[0].textContent).toBe('first paragraph')
    expect(paragraphs[1].textContent).toBe('second paragraph')
  })

  it('turns a single newline inside a paragraph into a <br/>', () => {
    const { container } = render(<ChatMarkdown text={'line one\nline two'} />)
    const p = container.querySelector('p')
    expect(p?.querySelectorAll('br')).toHaveLength(1)
    expect(p?.textContent).toBe('line oneline two')
  })

  it('renders inline code', () => {
    const { container } = render(<ChatMarkdown text="run `npm test` now" />)
    const code = container.querySelector('p code')
    expect(code?.textContent).toBe('npm test')
    expect(container.querySelector('p')?.textContent).toBe('run npm test now')
  })

  it('treats an unmatched backtick as literal text', () => {
    const { container } = render(<ChatMarkdown text="here's a `code without close" />)
    expect(container.querySelector('code')).toBeNull()
    expect(container.textContent).toBe("here's a `code without close")
  })

  it('renders a fenced code block verbatim and ignores the language tag', () => {
    const { container } = render(<ChatMarkdown text={'```js\nconst x = 1\nconsole.log(x)\n```'} />)
    const code = container.querySelector('pre code')
    expect(code?.textContent).toBe('const x = 1\nconsole.log(x)')
    expect(container.textContent).not.toContain('js')
  })

  it('treats a fenced block with no closing fence as code through the end', () => {
    const { container } = render(<ChatMarkdown text={'```\nunterminated\nstill code'} />)
    const code = container.querySelector('pre code')
    expect(code?.textContent).toBe('unterminated\nstill code')
  })

  it('renders content after a closed fence as its own paragraph', () => {
    const { container } = render(<ChatMarkdown text={'```\ncode\n```\nafter'} />)
    expect(container.querySelector('pre code')?.textContent).toBe('code')
    expect(container.querySelector('p')?.textContent).toBe('after')
  })

  it('renders an unordered list from "- " lines', () => {
    const { container } = render(<ChatMarkdown text={'- one\n- two'} />)
    const items = container.querySelectorAll('ul li')
    expect(items).toHaveLength(2)
    expect(items[0].textContent).toBe('one')
    expect(items[1].textContent).toBe('two')
  })

  it('renders an unordered list from "* " lines', () => {
    const { container } = render(<ChatMarkdown text={'* alpha\n* beta'} />)
    expect(container.querySelectorAll('ul li')).toHaveLength(2)
  })

  it('renders an ordered list from "1. " / "2. " lines', () => {
    const { container } = render(<ChatMarkdown text={'1. first\n2. second'} />)
    const items = container.querySelectorAll('ol li')
    expect(items).toHaveLength(2)
    expect(items[0].textContent).toBe('first')
    expect(items[1].textContent).toBe('second')
  })

  it('renders raw HTML as literal text, never as an element', () => {
    const { container } = render(<ChatMarkdown text="<script>alert(1)</script>" />)
    expect(container.querySelector('script')).toBeNull()
    expect(container.textContent).toBe('<script>alert(1)</script>')
  })

  it('renders paragraphs before and after a list', () => {
    const { container } = render(<ChatMarkdown text={'intro\n\n- a\n- b\n\noutro'} />)
    expect(container.querySelectorAll('p')).toHaveLength(2)
    expect(container.querySelectorAll('ul li')).toHaveLength(2)
  })
})
