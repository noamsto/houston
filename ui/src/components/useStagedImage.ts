import { useCallback, useEffect, useRef, useState } from 'react'

export type StagedImage = { id: number; file: File; previewUrl: string | null }

// The server caps an input body at 50 MiB of base64 JSON, about 37 MiB of raw
// bytes; staying under 36 MiB leaves room for the JSON framing and the text.
export const MAX_STAGED_BYTES = 36 * 1024 * 1024

export const readBase64 = (file: File) =>
  new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve((reader.result as string).replace(/^data:[^;]+;base64,/, ''))
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })

const revoke = (url: string | null) => {
  if (url) URL.revokeObjectURL(url)
}

export const skippedMessage = (n: number) =>
  `${n} file${n === 1 ? '' : 's'} skipped — attachments are limited to ${MAX_STAGED_BYTES / (1024 * 1024)} MiB in total`

/** Attached images held until Send; each preview URL is revoked when its image is removed, cleared or unmounted. */
export function useStagedImage() {
  const [staged, setStaged] = useState<StagedImage[]>([])
  const stagedRef = useRef(staged)
  const nextId = useRef(0)

  const set = useCallback((next: StagedImage[]) => {
    stagedRef.current = next
    setStaged(next)
  }, [])

  // Appends the files that fit under the size cap; returns how many were refused.
  const stage = useCallback((files: File[]): number => {
    let total = stagedRef.current.reduce((n, s) => n + s.file.size, 0)
    const added: StagedImage[] = []
    let skipped = 0
    for (const file of files) {
      if (total + file.size > MAX_STAGED_BYTES) {
        skipped++
        continue
      }
      total += file.size
      // happy-dom (and some webviews) lack createObjectURL
      const previewUrl = typeof URL.createObjectURL === 'function' ? URL.createObjectURL(file) : null
      added.push({ id: nextId.current++, file, previewUrl })
    }
    if (added.length > 0) set([...stagedRef.current, ...added])
    return skipped
  }, [set])

  const remove = useCallback((id: number) => {
    const gone = stagedRef.current.find((s) => s.id === id)
    if (!gone) return
    revoke(gone.previewUrl)
    set(stagedRef.current.filter((s) => s.id !== id))
  }, [set])

  const clear = useCallback(() => {
    for (const s of stagedRef.current) revoke(s.previewUrl)
    set([])
  }, [set])

  useEffect(() => () => {
    for (const s of stagedRef.current) revoke(s.previewUrl)
  }, [])

  return { staged, stage, remove, clear }
}
