import { useEffect, useRef } from 'react'
import type { SessionsData } from '../api/types'

/** Collect all window keys that currently need attention. */
function attentionKeys(sessions: SessionsData): Map<string, { session: string; window: string; activity: string }> {
  const map = new Map<string, { session: string; window: string; activity: string }>()
  for (const s of sessions.needs_attention) {
    for (const w of s.windows) {
      if (!w.needs_attention) continue
      const key = `${s.session.name}:${w.window.index}`
      const activity =
        w.parse_result.type === 'error' ? 'Error' :
        w.parse_result.type === 'question' ? 'Waiting for input' :
        w.parse_result.type === 'choice' ? 'Waiting for choice' :
        w.parse_result.activity || 'Needs attention'
      const label = w.branch && w.branch !== 'main' && w.branch !== 'master'
        ? w.branch : w.window.name
      map.set(key, { session: s.session.name, window: label, activity })
    }
  }
  return map
}

export function useAttentionNotifications(sessions: SessionsData | null) {
  const prevKeysRef = useRef<Set<string>>(new Set())
  const permissionRef = useRef<NotificationPermission>(
    typeof Notification !== 'undefined' ? Notification.permission : 'denied',
  )

  // Request permission once on mount
  useEffect(() => {
    if (typeof Notification === 'undefined') return
    if (Notification.permission === 'default') {
      Notification.requestPermission().then((p) => {
        permissionRef.current = p
      })
    }
  }, [])

  useEffect(() => {
    if (!sessions || permissionRef.current !== 'granted') return

    const current = attentionKeys(sessions)
    const prev = prevKeysRef.current

    for (const [key, info] of current) {
      if (prev.has(key)) continue
      // New attention window — notify
      new Notification(`${info.session} — ${info.window}`, {
        body: info.activity,
        tag: key, // dedup same window
      })
    }

    prevKeysRef.current = new Set(current.keys())
  }, [sessions])
}
