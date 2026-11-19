import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { DispatchAttachments } from './DispatchAttachments'
import { DISPATCH_IMAGE_HINT, dispatchSkippedMessage, toImageUploads } from './dispatchAttachments'

afterEach(cleanup)

const png = new File(['hello'], 'shot.png', { type: 'image/png' })

describe('DispatchAttachments', () => {
  it('shows the hint and a file input limited to the four image types', () => {
    const { container } = render(
      <DispatchAttachments staged={[]} onStage={() => 0} onRemove={() => {}} idPrefix="t" />,
    )
    expect(screen.getByText(DISPATCH_IMAGE_HINT)).toBeTruthy()
    const input = container.querySelector('input[type=file]') as HTMLInputElement
    expect(input.multiple).toBe(true)
    expect(input.accept).toBe('image/png,image/jpeg,image/gif,image/webp')
  })

  it('stages the picked files', () => {
    const onStage = vi.fn(() => 0)
    const { container } = render(
      <DispatchAttachments staged={[]} onStage={onStage} onRemove={() => {}} idPrefix="t" />,
    )
    fireEvent.change(container.querySelector('input[type=file]')!, { target: { files: [png] } })
    expect(onStage).toHaveBeenCalledWith([png])
  })

  it('renders a chip per staged image and × calls onRemove with its id', () => {
    const onRemove = vi.fn()
    render(
      <DispatchAttachments staged={[{ id: 7, file: png, previewUrl: null }]} onStage={() => 0} onRemove={onRemove} idPrefix="t" />,
    )
    expect(screen.getByTestId('staged-image').textContent).toContain('shot.png')
    fireEvent.click(screen.getByLabelText('Remove attachment'))
    expect(onRemove).toHaveBeenCalledWith(7)
  })

  it('shows the skipped message, cleared on the next clean pick', () => {
    const onStage = vi.fn().mockReturnValueOnce(2).mockReturnValueOnce(0)
    const { container } = render(
      <DispatchAttachments staged={[]} onStage={onStage} onRemove={() => {}} idPrefix="t" />,
    )
    const input = container.querySelector('input[type=file]')!
    fireEvent.change(input, { target: { files: [png] } })
    expect(screen.getByRole('alert').textContent).toBe(dispatchSkippedMessage(2))
    fireEvent.change(input, { target: { files: [png] } })
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('disables the attach button when disabled', () => {
    render(<DispatchAttachments staged={[]} onStage={() => 0} onRemove={() => {}} disabled idPrefix="t" />)
    expect((screen.getByLabelText('Attach image') as HTMLButtonElement).disabled).toBe(true)
  })
})

describe('dispatchSkippedMessage', () => {
  it('names the count and the limits', () => {
    expect(dispatchSkippedMessage(1)).toBe('1 file skipped — up to 10 images (PNG, JPEG, GIF, WebP), 10 MiB each, 25 MiB in total')
    expect(dispatchSkippedMessage(3)).toBe('3 files skipped — up to 10 images (PNG, JPEG, GIF, WebP), 10 MiB each, 25 MiB in total')
  })
})

describe('toImageUploads', () => {
  it('maps staged images to name, type and bare base64', async () => {
    expect(await toImageUploads([{ id: 0, file: png, previewUrl: null }])).toEqual([
      { name: 'shot.png', type: 'image/png', data: 'aGVsbG8=' },
    ])
  })
})
