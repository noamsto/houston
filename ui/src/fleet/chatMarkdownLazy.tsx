import { Component, Suspense, lazy } from 'react'
import type { ReactNode } from 'react'
import { loadChatMarkdown } from './chatMarkdownLoader'

const LazyChatMarkdown = lazy(loadChatMarkdown)

/** Plain-text stand-in for `ChatMarkdown`: whitespace and line breaks
 *  preserved, no markdown parsing. */
export function ChatPlainText({ text }: { text: string }) {
  return <span className="chat-plain">{text}</span>
}

// A chunk that fails to load (stale hash after a rebuild, flaky network) makes
// React.lazy throw on every render; without this the whole app would unmount.
class ChunkErrorBoundary extends Component<{ fallback: ReactNode; children: ReactNode }, { failed: boolean }> {
  state = { failed: false }
  static getDerivedStateFromError() {
    return { failed: true }
  }
  render() {
    return this.state.failed ? this.props.fallback : this.props.children
  }
}

export function ChatMarkdownLazy({ text }: { text: string }) {
  const plain = <ChatPlainText text={text} />
  return (
    <ChunkErrorBoundary fallback={plain}>
      <Suspense fallback={plain}>
        <LazyChatMarkdown text={text} />
      </Suspense>
    </ChunkErrorBoundary>
  )
}
