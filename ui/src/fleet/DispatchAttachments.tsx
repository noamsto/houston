import { useRef, useState } from 'react'
import { AttachButton } from '../components/ComposerControls'
import { StagedImageChip } from '../components/StagedImageChip'
import type { StagedImage } from '../components/useStagedImage'
import { DISPATCH_IMAGE_HINT, IMAGE_TYPES, dispatchSkippedMessage } from './dispatchAttachments'

type Props = {
  staged: StagedImage[]
  onStage: (files: File[]) => number
  onRemove: (id: number) => void
  disabled?: boolean
  idPrefix: string
}

export function DispatchAttachments({ staged, onStage, onRemove, disabled, idPrefix }: Props) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [error, setError] = useState<string | null>(null)

  const handleFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? [])
    e.target.value = ''
    if (files.length === 0) return
    const skipped = onStage(files)
    setError(skipped > 0 ? dispatchSkippedMessage(skipped) : null)
  }

  return (
    <div className="dispatch-attachments">
      <input
        ref={inputRef}
        id={`${idPrefix}-attach`}
        type="file"
        multiple
        accept={IMAGE_TYPES.join(',')}
        style={{ display: 'none' }}
        onChange={handleFile}
      />
      <AttachButton disabled={disabled} onClick={() => inputRef.current?.click()} />
      {staged.length > 0 && (
        <div className="staged-chips">
          {staged.map((s) => (
            <StagedImageChip key={s.id} staged={s} onRemove={() => onRemove(s.id)} disabled={disabled} />
          ))}
        </div>
      )}
      <p className="dispatch-hint">{DISPATCH_IMAGE_HINT}</p>
      {error && <p role="alert" className="dispatch-hint dispatch-error">{error}</p>}
    </div>
  )
}
