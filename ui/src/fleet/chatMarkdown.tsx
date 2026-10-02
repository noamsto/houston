import type { ComponentProps, MouseEvent } from 'react'
import Markdown, { type Components } from 'react-markdown'
import remarkBreaks from 'remark-breaks'
import remarkGfm from 'remark-gfm'

const SAFE_URL = /^(https?:|mailto:)/i
const REMOTE_URL = /^https?:/i
const FRAGMENT_URL = /^#user-content-[A-Za-z0-9_-]+$/

// Anything else — javascript:, data:, relative paths — is dropped, so the
// `a`/`img` overrides below see no href/src and render plain text instead.
// The one same-document exception is a GFM footnote fragment; labels that
// mdast-util-to-hast percent-encodes (non-ASCII, '.') fail the pattern and
// stay plain text.
function urlTransform(url: string): string | undefined {
  return SAFE_URL.test(url) || FRAGMENT_URL.test(url) ? url : undefined
}

type FootnoteAttrs = { 'data-footnote-ref'?: boolean; 'data-footnote-backref'?: boolean }

const components: Components = {
  a(props: ComponentProps<'a'> & FootnoteAttrs) {
    const { href, title, id, children, className } = props
    if (!href) return <>{children}</>
    if (FRAGMENT_URL.test(href)) {
      // A real navigation would rewrite the SPA's hash route, so scroll within
      // this message's own root (footnote ids repeat across messages).
      const scrollToTarget = (e: MouseEvent<HTMLAnchorElement>) => {
        e.preventDefault()
        e.currentTarget
          .closest('.chat-md')
          ?.querySelector(`[id="${href.slice(1)}"]`)
          ?.scrollIntoView({ block: 'nearest' })
      }
      return (
        <a
          href={href}
          title={title}
          id={id}
          className={className}
          data-footnote-ref={props['data-footnote-ref']}
          data-footnote-backref={props['data-footnote-backref']}
          aria-describedby={props['aria-describedby']}
          aria-label={props['aria-label']}
          onClick={scrollToTarget}
        >
          {children}
        </a>
      )
    }
    return (
      <a href={href} title={title} target="_blank" rel="noopener noreferrer">
        {children}
      </a>
    )
  },
  // Never fetch remote images from a transcript (privacy, mobile data).
  img({ src, alt }) {
    const label = alt ? `[image: ${alt}]` : '[image]'
    if (typeof src === 'string' && REMOTE_URL.test(src)) {
      return (
        <a className="chat-md-img" href={src} target="_blank" rel="noopener noreferrer">
          {label}
        </a>
      )
    }
    return <span className="chat-md-img">{label}</span>
  },
  pre({ node, children }) {
    const code = node?.children[0]
    const classes = code?.type === 'element' ? code.properties.className : undefined
    const lang = Array.isArray(classes)
      ? classes.map(String).find((c) => c.startsWith('language-'))?.slice('language-'.length)
      : undefined
    return (
      <div className="chat-md-code">
        {lang && <span className="chat-md-lang">{lang}</span>}
        <pre className="chat-md-pre">{children}</pre>
      </div>
    )
  },
  table({ children }) {
    return (
      <div className="chat-md-table">
        <table>{children}</table>
      </div>
    )
  },
}

export function ChatMarkdown({ text }: { text: string }) {
  if (!text) return null
  return (
    <div className="chat-md">
      <Markdown remarkPlugins={[remarkGfm, remarkBreaks]} components={components} urlTransform={urlTransform}>
        {text}
      </Markdown>
    </div>
  )
}
