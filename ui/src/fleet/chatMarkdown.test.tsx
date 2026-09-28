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

  it('renders inside a .chat-md root', () => {
    const { container } = render(<ChatMarkdown text="hello" />)
    expect(container.firstElementChild?.className).toBe('chat-md')
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

  it('turns a single newline into a line break inside one paragraph', () => {
    const { container } = render(<ChatMarkdown text={'line one\nline two'} />)
    expect(container.querySelectorAll('p')).toHaveLength(1)
    expect(container.querySelectorAll('p br')).toHaveLength(1)
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

  it('renders a fenced code block verbatim with a language label', () => {
    const { container } = render(<ChatMarkdown text={'```js\nconst x = 1\nconsole.log(x)\n```'} />)
    const code = container.querySelector('pre.chat-md-pre code')
    expect(code?.textContent).toBe('const x = 1\nconsole.log(x)\n')
    expect(container.querySelector('.chat-md-lang')?.textContent).toBe('js')
  })

  it('omits the language label for an untagged fence', () => {
    const { container } = render(<ChatMarkdown text={'```\nplain\n```'} />)
    expect(container.querySelector('pre.chat-md-pre code')?.textContent).toBe('plain\n')
    expect(container.querySelector('.chat-md-lang')).toBeNull()
  })

  it('treats a fenced block with no closing fence as code through the end', () => {
    const { container } = render(<ChatMarkdown text={'```\nunterminated\nstill code'} />)
    expect(container.querySelector('pre code')?.textContent).toBe('unterminated\nstill code\n')
  })

  it('renders content after a closed fence as its own paragraph', () => {
    const { container } = render(<ChatMarkdown text={'```\ncode\n```\nafter'} />)
    expect(container.querySelector('pre code')?.textContent).toBe('code\n')
    expect(container.querySelector('p')?.textContent).toBe('after')
  })

  it('renders an unordered list from "- " and "* " lines', () => {
    const dash = render(<ChatMarkdown text={'- one\n- two'} />)
    const items = dash.container.querySelectorAll('ul li')
    expect(items).toHaveLength(2)
    expect(items[0].textContent).toBe('one')
    expect(items[1].textContent).toBe('two')
    cleanup()
    const star = render(<ChatMarkdown text={'* alpha\n* beta'} />)
    expect(star.container.querySelectorAll('ul li')).toHaveLength(2)
  })

  it('renders an ordered list', () => {
    const { container } = render(<ChatMarkdown text={'1. first\n2. second'} />)
    const items = container.querySelectorAll('ol li')
    expect(items).toHaveLength(2)
    expect(items[0].textContent).toBe('first')
    expect(items[1].textContent).toBe('second')
  })

  it('renders a GFM task list with disabled checkboxes', () => {
    const { container } = render(<ChatMarkdown text={'- [x] done\n- [ ] todo'} />)
    const boxes = container.querySelectorAll<HTMLInputElement>('li input[type="checkbox"]')
    expect(boxes).toHaveLength(2)
    expect(boxes[0].checked).toBe(true)
    expect(boxes[1].checked).toBe(false)
    expect([...boxes].every((b) => b.disabled)).toBe(true)
  })

  it('renders paragraphs before and after a list', () => {
    const { container } = render(<ChatMarkdown text={'intro\n\n- a\n- b\n\noutro'} />)
    expect(container.querySelectorAll('p')).toHaveLength(2)
    expect(container.querySelectorAll('ul li')).toHaveLength(2)
  })

  it('renders bold, italic and strikethrough', () => {
    const { container } = render(<ChatMarkdown text="**bold** _italic_ ~~gone~~" />)
    expect(container.querySelector('strong')?.textContent).toBe('bold')
    expect(container.querySelector('em')?.textContent).toBe('italic')
    expect(container.querySelector('del')?.textContent).toBe('gone')
  })

  it('renders headings h1 through h6', () => {
    const md = ['# one', '## two', '### three', '#### four', '##### five', '###### six'].join('\n')
    const { container } = render(<ChatMarkdown text={md} />)
    expect(container.querySelector('h1')?.textContent).toBe('one')
    expect(container.querySelector('h6')?.textContent).toBe('six')
    expect(container.querySelectorAll('h1, h2, h3, h4, h5, h6')).toHaveLength(6)
  })

  it('renders an https link as a new-tab anchor', () => {
    const { container } = render(<ChatMarkdown text="see [docs](https://example.com/x)" />)
    const a = container.querySelector('a')
    expect(a?.getAttribute('href')).toBe('https://example.com/x')
    expect(a?.getAttribute('target')).toBe('_blank')
    expect(a?.getAttribute('rel')).toBe('noopener noreferrer')
    expect(a?.textContent).toBe('docs')
  })

  it('renders a mailto link as an anchor', () => {
    const { container } = render(<ChatMarkdown text="[mail](mailto:a@b.c)" />)
    expect(container.querySelector('a')?.getAttribute('href')).toBe('mailto:a@b.c')
  })

  it('renders javascript:, data: and relative links as plain text', () => {
    const md = '[js](javascript:alert(1)) [data](data:text/html,x) [rel](/etc/passwd)'
    const { container } = render(<ChatMarkdown text={md} />)
    expect(container.querySelector('a')).toBeNull()
    expect(container.textContent).toBe('js data rel')
  })

  it('never renders an <img>, showing a link-styled label instead', () => {
    const { container } = render(<ChatMarkdown text="![a cat](https://example.com/cat.png)" />)
    expect(container.querySelector('img')).toBeNull()
    const label = container.querySelector('a.chat-md-img')
    expect(label?.textContent).toBe('[image: a cat]')
    expect(label?.getAttribute('href')).toBe('https://example.com/cat.png')
  })

  it('renders an image with an unsafe src as unlinked text', () => {
    const { container } = render(<ChatMarkdown text="![x](data:image/png;base64,AAAA)" />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('a')).toBeNull()
    expect(container.querySelector('span.chat-md-img')?.textContent).toBe('[image: x]')
  })

  it('wraps a GFM table in a horizontal scroll container', () => {
    const md = '| a | b |\n|---|---|\n| 1 | 2 |'
    const { container } = render(<ChatMarkdown text={md} />)
    const table = container.querySelector('.chat-md-table > table')
    expect(table).not.toBeNull()
    expect(table?.querySelectorAll('th')).toHaveLength(2)
    expect(table?.querySelectorAll('td')).toHaveLength(2)
  })

  it('renders a blockquote and a horizontal rule', () => {
    const { container } = render(<ChatMarkdown text={'> quoted\n\n---\n\nafter'} />)
    expect(container.querySelector('blockquote')?.textContent?.trim()).toBe('quoted')
    expect(container.querySelector('hr')).not.toBeNull()
  })

  it('renders raw HTML as literal text, never as an element', () => {
    const { container } = render(<ChatMarkdown text="<script>alert(1)</script>" />)
    expect(container.querySelector('script')).toBeNull()
    expect(container.textContent).toBe('<script>alert(1)</script>')
  })

  it('renders an inline <img onerror> as literal text', () => {
    const { container } = render(<ChatMarkdown text={'hi <img src=x onerror="alert(1)"> there'} />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toBe('hi <img src=x onerror="alert(1)"> there')
  })
})
