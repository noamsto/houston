import Markdown, { type Components } from 'react-markdown'
import remarkBreaks from 'remark-breaks'
import remarkGfm from 'remark-gfm'

const SAFE_URL = /^(https?:|mailto:)/i
const REMOTE_URL = /^https?:/i

// Anything else — javascript:, data:, relative paths — is dropped, so the
// `a`/`img` overrides below see no href/src and render plain text instead.
function urlTransform(url: string): string | undefined {
  return SAFE_URL.test(url) ? url : undefined
}

const components: Components = {
  a({ href, title, children }) {
    if (!href) return <>{children}</>
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
