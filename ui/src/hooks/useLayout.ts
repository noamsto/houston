import { useEffect, useState } from 'react'

const FONT_SIZE_STORAGE_KEY = 'houston-terminal-font-size'
const DEFAULT_TERMINAL_FONT_SIZE = 14

function loadTerminalFontSize(): number {
  try {
    const saved = localStorage.getItem(FONT_SIZE_STORAGE_KEY)
    const n = saved === null ? NaN : Number(saved)
    if (Number.isFinite(n)) return n
  } catch {
    // ignore
  }
  return DEFAULT_TERMINAL_FONT_SIZE
}

/** Persisted per-device terminal font-size preference. */
export function useTerminalFontSize() {
  const [fontSize, setFontSize] = useState<number>(loadTerminalFontSize)

  useEffect(() => {
    localStorage.setItem(FONT_SIZE_STORAGE_KEY, String(fontSize))
  }, [fontSize])

  return [fontSize, setFontSize] as const
}

const GROUP_BY_PROJECT_STORAGE_KEY = 'houston-fleet-group-by-project'

function loadGroupByProject(): boolean {
  try {
    return localStorage.getItem(GROUP_BY_PROJECT_STORAGE_KEY) === 'true'
  } catch {
    return false
  }
}

/** Per-device Fleet grouping preference, remembered in localStorage. */
export function useGroupByProject(): [boolean, (v: boolean) => void] {
  const [grouped, setGrouped] = useState<boolean>(loadGroupByProject)

  const set = (v: boolean) => {
    setGrouped(v)
    try {
      localStorage.setItem(GROUP_BY_PROJECT_STORAGE_KEY, String(v))
    } catch {
      // ignore
    }
  }

  return [grouped, set]
}
