import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { StagedImageChip } from './StagedImageChip'

afterEach(cleanup)

const file = new File(['x'], 'shot.png', { type: 'image/png' })

describe('StagedImageChip', () => {
  it('shows the file name and the thumbnail', () => {
    const { container } = render(
      <StagedImageChip staged={{ file, previewUrl: 'blob:p' }} onRemove={() => {}} />,
    )
    expect(screen.getByTestId('staged-image').textContent).toContain('shot.png')
    expect(container.querySelector('img')?.getAttribute('src')).toBe('blob:p')
  })

  it('omits the thumbnail without a preview url', () => {
    const { container } = render(
      <StagedImageChip staged={{ file, previewUrl: null }} onRemove={() => {}} />,
    )
    expect(container.querySelector('img')).toBeNull()
  })

  it('shows only the name for a non-image file even with a preview url', () => {
    const pdf = new File(['x'], 'doc.pdf', { type: 'application/pdf' })
    const { container } = render(
      <StagedImageChip staged={{ file: pdf, previewUrl: 'blob:p' }} onRemove={() => {}} />,
    )
    expect(container.querySelector('img')).toBeNull()
    expect(screen.getByTestId('staged-image').textContent).toContain('doc.pdf')
  })

  it('calls onRemove from the remove button', () => {
    const onRemove = vi.fn()
    render(<StagedImageChip staged={{ file, previewUrl: null }} onRemove={onRemove} />)
    fireEvent.click(screen.getByRole('button', { name: 'Remove attachment' }))
    expect(onRemove).toHaveBeenCalledTimes(1)
  })

  it('disables the remove button when disabled', () => {
    render(<StagedImageChip staged={{ file, previewUrl: null }} onRemove={() => {}} disabled />)
    expect(screen.getByRole('button', { name: 'Remove attachment' }).hasAttribute('disabled')).toBe(true)
  })
})
