import { Suspense, lazy } from 'react'

// The markdown renderer and its deps (react-markdown, remark) are only needed
// once an assistant bubble finishes revealing, so keep them out of the main
// chunk. The `.then` wrapper leaves chatMarkdown's named export untouched.
const LazyChatMarkdown = lazy(() => import('./chatMarkdown').then((m) => ({ default: m.ChatMarkdown })))

/** Plain-text stand-in for `ChatMarkdown`, matching what the typewriter shows
 *  mid-reveal: whitespace and line breaks preserved, no markdown parsing. */
export function ChatPlainText({ text }: { text: string }) {
  return <span className="chat-plain">{text}</span>
}

export function ChatMarkdownLazy({ text }: { text: string }) {
  return (
    <Suspense fallback={<ChatPlainText text={text} />}>
      <LazyChatMarkdown text={text} />
    </Suspense>
  )
}
