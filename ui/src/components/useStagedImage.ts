import { useCallback, useEffect, useState } from 'react'

export type StagedImage = { file: File; previewUrl: string | null }

export const readBase64 = (file: File) =>
  new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve((reader.result as string).replace(/^data:[^;]+;base64,/, ''))
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })

/** An attached image held until Send; its preview URL is revoked when replaced, cleared or unmounted. */
export function useStagedImage() {
  const [staged, setStaged] = useState<StagedImage | null>(null)

  const stage = useCallback((file: File) => {
    // happy-dom (and some webviews) lack createObjectURL
    const previewUrl = typeof URL.createObjectURL === 'function' ? URL.createObjectURL(file) : null
    setStaged({ file, previewUrl })
  }, [])

  const clear = useCallback(() => setStaged(null), [])

  useEffect(() => {
    const url = staged?.previewUrl
    if (!url) return
    return () => URL.revokeObjectURL(url)
  }, [staged])

  return { staged, stage, clear }
}
