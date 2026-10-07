import { useCallback, useEffect, useRef, useState } from 'react'

const KEY_PREFIX = 'houston-draft:'
// Keys without a session identity (a run id, or a pane whose session is not
// known yet) can be inherited by a later agent, so an old draft must not
// outlive the session it was typed for.
const DRAFT_TTL_MS = 24 * 60 * 60_000

const storageKey = (key: string) => `${KEY_PREFIX}${key}`

type Listener = (sent: string) => void
const listeners = new Map<string, Set<Listener>>()

/** The stored text, or null when absent, malformed or expired (the item is removed then). */
function readDraft(storeKey: string): string | null {
  try {
    const raw = localStorage.getItem(storeKey)
    if (raw === null) return null
    try {
      const draft: unknown = JSON.parse(raw)
      if (
        draft &&
        typeof draft === 'object' &&
        't' in draft &&
        typeof draft.t === 'string' &&
        'at' in draft &&
        typeof draft.at === 'number' &&
        Date.now() - draft.at <= DRAFT_TTL_MS
      ) {
        return draft.t
      }
    } catch {
      // malformed: fall through to removal
    }
    localStorage.removeItem(storeKey)
  } catch {
    // ignore
  }
  return null
}

function loadDraft(key: string): string {
  return readDraft(storageKey(key)) ?? ''
}

function saveDraft(key: string, text: string) {
  try {
    if (text === '') localStorage.removeItem(storageKey(key))
    else localStorage.setItem(storageKey(key), JSON.stringify({ t: text, at: Date.now() }))
  } catch {
    // ignore
  }
}

let swept = false

function sweepExpiredDrafts() {
  if (swept) return
  swept = true
  try {
    const keys: string[] = []
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)
      if (k?.startsWith(KEY_PREFIX)) keys.push(k)
    }
    for (const k of keys) readDraft(k)
  } catch {
    // ignore
  }
}

/**
 * Composer text persisted per key in localStorage for 24h. The key is read
 * once per mount. `clearIf(sent)` drops the draft only if it still equals the
 * text that was sent, in storage and in every mounted composer of that key, so
 * it also works for a send that finishes after the composer remounted.
 */
export function useComposerDraft(key: string) {
  const [text, setText] = useState(() => {
    sweepExpiredDrafts()
    return loadDraft(key)
  })
  // Skipping the write for unchanged text keeps a reopened draft's age intact.
  const saved = useRef(text)

  useEffect(() => {
    if (text === saved.current) return
    saved.current = text
    saveDraft(key, text)
  }, [key, text])

  useEffect(() => {
    const listener: Listener = (sent) => setText((cur) => (cur === sent ? '' : cur))
    const set = listeners.get(key) ?? new Set<Listener>()
    set.add(listener)
    listeners.set(key, set)
    return () => {
      set.delete(listener)
      if (set.size === 0) listeners.delete(key)
    }
  }, [key])

  const clearIf = useCallback(
    (sent: string) => {
      if (loadDraft(key) === sent) saveDraft(key, '')
      listeners.get(key)?.forEach((listener) => listener(sent))
    },
    [key],
  )

  return [text, setText, clearIf] as const
}
