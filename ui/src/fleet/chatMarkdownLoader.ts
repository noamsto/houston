// Keeps react-markdown and the remark plugins out of the main chunk; the
// `.then` wrapper leaves chatMarkdown's named export untouched.
export const loadChatMarkdown = () => import('./chatMarkdown').then((m) => ({ default: m.ChatMarkdown }))

/** Start fetching the chunk ahead of the first bubble so history doesn't flash
 *  as raw source. A failure is ignored: the render path falls back to text. */
export function preloadChatMarkdown() {
  loadChatMarkdown().catch(() => {})
}
