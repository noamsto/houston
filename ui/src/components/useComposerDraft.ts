import { useCallback, useEffect, useState } from 'react'

const storageKey = (key: string) => `houston-draft:${key}`

function loadDraft(key: string): string {
  try {
    return localStorage.getItem(storageKey(key)) ?? ''
  } catch {
    return ''
  }
}

function saveDraft(key: string, text: string) {
  try {
    if (text === '') localStorage.removeItem(storageKey(key))
    else localStorage.setItem(storageKey(key), text)
  } catch {
    // ignore
  }
}

/**
 * Composer text persisted per key in localStorage. Consumers remount per run,
 * so the key is read once. `clearIf(sent)` drops the draft only if it still
 * equals the text that was sent; storage is cleared directly so it works after
 * the composer has unmounted.
 */
export function useComposerDraft(key: string) {
  const [text, setText] = useState(() => loadDraft(key))

  useEffect(() => saveDraft(key, text), [key, text])

  const clearIf = useCallback(
    (sent: string) => {
      if (loadDraft(key) === sent) saveDraft(key, '')
      setText((cur) => (cur === sent ? '' : cur))
    },
    [key],
  )

  return [text, setText, clearIf] as const
}
