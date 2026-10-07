import type { StagedImage } from './useStagedImage'

type Props = { staged: StagedImage; onRemove: () => void; disabled?: boolean }

export function StagedImageChip({ staged, onRemove, disabled }: Props) {
  return (
    <div className="staged-chip" data-testid="staged-image">
      {staged.previewUrl && staged.file.type.startsWith('image/') && <img className="staged-chip-thumb" alt="" src={staged.previewUrl} />}
      <span className="staged-chip-name">{staged.file.name}</span>
      <button
        type="button"
        className="staged-chip-remove"
        aria-label="Remove attachment"
        onClick={onRemove}
        disabled={disabled}
      >
        ×
      </button>
    </div>
  )
}
