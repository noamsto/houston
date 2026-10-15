import './composer.css'
import { COMPOSER_TAP_PX } from './composerHooks'

const tapSize = { minWidth: COMPOSER_TAP_PX, minHeight: COMPOSER_TAP_PX }

export function AttachButton({ onClick, disabled }: { onClick: () => void; disabled?: boolean }) {
  return (
    <button type="button" className="composer-btn" style={tapSize} title="Attach image" aria-label="Attach image" disabled={disabled} onClick={onClick}>
      <span aria-hidden="true">📎</span>
    </button>
  )
}

export function VoiceButton({ listening, onClick, disabled }: { listening: boolean; onClick: () => void; disabled?: boolean }) {
  return (
    <button
      type="button"
      className={`composer-btn${listening ? ' composer-btn--recording' : ''}`}
      style={tapSize}
      title={listening ? 'Stop recording' : 'Voice input'}
      aria-label={listening ? 'Stop recording' : 'Voice input'}
      aria-pressed={listening}
      disabled={disabled}
      onClick={onClick}
    >
      <span aria-hidden="true">🎤</span>
    </button>
  )
}

export function SendButton({ onClick, disabled }: { onClick: () => void; disabled?: boolean }) {
  return (
    <button type="button" className="composer-btn composer-btn--send" style={tapSize} title="Send" aria-label="Send" disabled={disabled} onClick={onClick}>
      Send
    </button>
  )
}
