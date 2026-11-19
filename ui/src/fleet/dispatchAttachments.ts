import type { ImageUpload } from '../api/dispatch'
import { readBase64, type StageLimits, type StagedImage } from '../components/useStagedImage'

const MiB = 1024 * 1024

export const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'] as const

export const DISPATCH_IMAGE_LIMITS: StageLimits = {
  maxTotal: 25 * MiB,
  maxEach: 10 * MiB,
  maxFiles: 10,
  types: IMAGE_TYPES,
}

const LIMITS_TEXT = '10 images (PNG, JPEG, GIF, WebP), 10 MiB each, 25 MiB in total'

export const DISPATCH_IMAGE_HINT = `Up to ${LIMITS_TEXT}.`

export const dispatchSkippedMessage = (n: number) =>
  `${n} file${n === 1 ? '' : 's'} skipped — up to ${LIMITS_TEXT}`

export const toImageUploads = (staged: StagedImage[]): Promise<ImageUpload[]> =>
  Promise.all(staged.map(async (s) => ({ name: s.file.name, type: s.file.type, data: await readBase64(s.file) })))
