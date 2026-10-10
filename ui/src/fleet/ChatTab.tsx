import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { Run } from '../api/runs'
import { fetchTool } from '../api/chat'
import type { ChatToolDetail } from '../api/chat'
import { sendImage, sendKey, sendText } from '../api/terminal'
import type { TerminalAddress } from '../api/terminal'
import { QuickCommands } from '../components/QuickCommands'
import { StagedImageChip } from '../components/StagedImageChip'
import { isClaudeAgent, suggestedCommand } from '../components/quickCommands'
import { useComposerDraft } from '../components/useComposerDraft'
import { readBase64, useStagedImage } from '../components/useStagedImage'
import { useRunChat } from '../hooks/useRunChat'
import { useStickyScroll } from '../hooks/useStickyScroll'
import { buildItems, provisionalTool, reconcileOptimistic, toolRowLabel } from './chatModel'
import type { AssistantItem, ChatItem, DividerItem, Optimistic, ToolCall, ToolsItem, UserItem } from './chatModel'
import { kindGlyph, statusGlyph } from './chatGlyphs'
import { ChatMarkdownLazy, ChatPlainText } from './chatMarkdownLazy'
import { preloadChatMarkdown } from './chatMarkdownLoader'
import { PermissionBar } from './PermissionBar'
import { QuestionCard } from './QuestionCard'
import { RunQuestion, RunStatusStrip } from './RunStatusStrip'

const REVEAL_TICKS = 16
const REVEAL_MS = 1000

function prefersReducedMotion(): boolean {
  return typeof window !== 'undefined' && Boolean(window.matchMedia?.('(prefers-reduced-motion: reduce)').matches)
}

function UserBubble({ item }: { item: UserItem }) {
  return (
    <div className="chat-bubble chat-user" data-seq={item.seq} data-id={item.id}>
      {item.text}
    </div>
  )
}

function Divider({ item }: { item: DividerItem }) {
  const [open, setOpen] = useState(false)
  const label = (
    <>
      <span className="chat-glyph" aria-hidden="true">✦</span>
      {item.text}
    </>
  )
  if (!item.summary) {
    return (
      <div className="chat-divider" data-seq={item.seq} data-id={item.id}>
        {label}
      </div>
    )
  }
  return (
    <>
      <button
        type="button"
        className="chat-divider"
        data-seq={item.seq}
        data-id={item.id}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {label}
      </button>
      {open && (
        <div className="chat-divider-summary">
          <ChatMarkdownLazy text={item.summary} />
        </div>
      )}
    </>
  )
}

/** Reveals `item.text` progressively over ~1s when `live`, else complete
 *  immediately. Text folded in mid-reveal continues from what is already
 *  shown and finishes ~1s after the growth. Each id is revealed only once — a
 *  later render of the same id after its reveal is shown complete right away. */
function AssistantBubble({ item, live, reducedMotion, revealed, onRevealed, onTick }: {
  item: AssistantItem
  live: boolean
  reducedMotion: boolean
  revealed: Set<string>
  onRevealed: (id: string) => void
  onTick: () => void
}) {
  const shouldReveal = live && !reducedMotion && !revealed.has(item.id)
  const [display, setDisplay] = useState(shouldReveal ? '' : item.text)
  const [complete, setComplete] = useState(!shouldReveal)
  const shownRef = useRef(0)

  useEffect(() => {
    if (!shouldReveal) {
      setDisplay(item.text)
      setComplete(true)
      return
    }
    const total = item.text.length
    const perTick = Math.max(1, Math.ceil((total - shownRef.current) / REVEAL_TICKS))
    const timer = setInterval(() => {
      shownRef.current = Math.min(total, shownRef.current + perTick)
      onTick()
      if (shownRef.current >= total) {
        setDisplay(item.text)
        setComplete(true)
        onRevealed(item.id)
        clearInterval(timer)
      } else {
        setDisplay(item.text.slice(0, shownRef.current))
      }
    }, REVEAL_MS / REVEAL_TICKS)
    return () => clearInterval(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [item.id, item.text, shouldReveal])

  return (
    <div className={`chat-bubble chat-assistant${item.commentary ? ' commentary' : ''}`} data-seq={item.seq} data-id={item.id}>
      {complete ? <ChatMarkdownLazy text={display} /> : <ChatPlainText text={display} />}
    </div>
  )
}

function diffText(diff: { oldText: string; newText: string }): string {
  const oldLines = diff.oldText.split('\n').map((l) => `- ${l}`)
  const newLines = diff.newText.split('\n').map((l) => `+ ${l}`)
  return [...oldLines, ...newLines].join('\n')
}

type ToolDetailState = { status: 'idle' } | { status: 'loading' } | { status: 'error' } | { status: 'ready'; data: ChatToolDetail }

function ToolCallRow({ call, runId }: { call: ToolCall; runId: string }) {
  const [detail, setDetail] = useState<ToolDetailState>({ status: 'idle' })
  const status = statusGlyph(call.status)

  const handleClick = () => {
    if (detail.status === 'loading' || detail.status === 'ready') return
    setDetail({ status: 'loading' })
    fetchTool(runId, call.toolCallId).then(
      (data) => setDetail({ status: 'ready', data }),
      () => setDetail({ status: 'error' }),
    )
  }

  return (
    <div className="chat-tool-call">
      <button type="button" className="chat-tool-call-row" onClick={handleClick}>
        <span className="chat-tool-kind" aria-hidden="true">{kindGlyph(call.kind)}</span>
        <span className="chat-tool-name">{call.tool}</span>
        {call.title && <span className="chat-tool-title">{call.title}</span>}
        {call.status && (
          <span className="chat-tool-status" data-status={call.status}>
            {status && <span className="chat-glyph" aria-hidden="true">{status}</span>}
            {call.status}
          </span>
        )}
        {call.subagent && (
          <span className="chat-tool-subagent">
            <span className="chat-glyph" aria-hidden="true">⑂</span>
            subagent
          </span>
        )}
      </button>
      {detail.status === 'loading' && <div className="chat-tool-detail">Loading…</div>}
      {detail.status === 'error' && <div className="chat-tool-detail error">Couldn't load tool detail</div>}
      {detail.status === 'ready' && (
        <div className="chat-tool-detail">
          {detail.data.inputOmitted && (
            <span className="chat-tool-input-omitted">input omitted (over 16 KiB)</span>
          )}
          {detail.data.diff ? (
            <pre className="chat-diff">{diffText(detail.data.diff)}</pre>
          ) : (
            <pre className="chat-tool-output">{detail.data.output}</pre>
          )}
          {detail.data.truncated && <span className="chat-tool-truncated">(truncated)</span>}
        </div>
      )}
    </div>
  )
}

function ToolsRow({ item, runId }: { item: ToolsItem; runId: string }) {
  const [expanded, setExpanded] = useState(false)
  const failed = item.rows.some((r) => r.failed)

  return (
    <div className="chat-tools" data-seq={item.seq} data-id={item.id}>
      <button
        type="button"
        className={`chat-tools-toggle${failed ? ' failed' : ''}`}
        aria-expanded={expanded}
        onClick={() => setExpanded((e) => !e)}
      >
        {item.rows.map((row, i) => (
          <Fragment key={i}>
            {i > 0 && ' · '}
            <span className="chat-glyph" aria-hidden="true">{kindGlyph(row.kind)}</span>
            {toolRowLabel(row)}
            {row.failed && <span className="chat-glyph-after" aria-hidden="true">✗</span>}
          </Fragment>
        ))}
      </button>
      {expanded && (
        <div className="chat-tools-calls">
          {item.calls.map((call) => (
            <ToolCallRow key={call.toolCallId} call={call} runId={runId} />
          ))}
        </div>
      )}
    </div>
  )
}

function ChatItemRow({ item, runId, canAnswer, answerable, live, reducedMotion, revealed, onRevealed, onTick }: {
  item: ChatItem
  runId: string
  canAnswer: boolean
  answerable: boolean
  live: boolean
  reducedMotion: boolean
  revealed: Set<string>
  onRevealed: (id: string) => void
  onTick: () => void
}) {
  if (item.kind === 'user') return <UserBubble item={item} />
  if (item.kind === 'divider') return <Divider item={item} />
  if (item.kind === 'tools') return <ToolsRow item={item} runId={runId} />
  if (item.kind === 'question') return <QuestionCard item={item} runId={runId} canAnswer={canAnswer} answerable={answerable} onLayout={onTick} />
  return <AssistantBubble item={item} live={live} reducedMotion={reducedMotion} revealed={revealed} onRevealed={onRevealed} onTick={onTick} />
}

/** `onSend` resolves to an error reason, or null once the text was sent. */
function Composer({ run, suggestion, onSend }: { run: Run; suggestion: string | null; onSend: (text: string) => Promise<string | null> }) {
  const [text, setText, clearIf] = useComposerDraft(`chat:${run.draft_key ?? run.id}`)
  const { staged, stage, clear } = useStagedImage()
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const disabled = !run.caps.terminal
  const address: TerminalAddress = { kind: 'run', id: run.id }

  // Esc is the user's stop-the-agent key, so it never takes the in-flight
  // gate; only text/image sends do. A failure is shown until the next action
  // starts, and a success never clears another action's error.
  const sendingRef = useRef(false)

  const prefill = (value: string) => {
    if (text.trim() || staged) return false
    setText(value)
    textareaRef.current?.focus()
    return true
  }

  const attempt = async (send: () => Promise<string | null>, sent: string | null) => {
    const isSend = sent !== null
    if (isSend) {
      if (sendingRef.current) return
      sendingRef.current = true
      setSending(true)
    }
    setError(null)
    let err: string | null
    try {
      err = await send()
    } finally {
      if (isSend) {
        sendingRef.current = false
        setSending(false)
      }
    }
    if (err) {
      setError(err)
      return
    }
    if (isSend) {
      clearIf(sent)
      clear()
    }
  }

  const handleSend = () => {
    const trimmed = text.trim()
    if (staged) {
      const { file } = staged
      void attempt(async () => {
        let data: string
        try {
          data = await readBase64(file)
        } catch {
          return 'could not read file'
        }
        return sendImage(address, trimmed, { name: file.name, type: file.type, data })
      }, text)
      return
    }
    if (!trimmed) return
    void attempt(() => onSend(trimmed), text)
  }

  const handleFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    if (sendingRef.current) {
      setError('busy — pick the file again')
      return
    }
    stage(file)
  }

  return (
    <div className="chat-composer">
      {disabled && <div className="chat-composer-reason">No live terminal for this run</div>}
      {error && <div className="chat-composer-error" role="alert">{error}</div>}
      {staged && <StagedImageChip staged={staged} onRemove={clear} disabled={sending} />}
      {!disabled && <QuickCommands address={address} agent={run.agent} state={run.state} suggestion={suggestion} onPrefill={prefill} />}
      <div className="chat-composer-row">
        <button type="button" className="chat-composer-esc" disabled={disabled} onClick={() => void attempt(() => sendKey(address, 'Escape'), null)}>
          Esc
        </button>
        <textarea
          className="chat-composer-textarea"
          ref={textareaRef}
          value={text}
          disabled={disabled}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
              e.preventDefault()
              handleSend()
            }
          }}
          placeholder="Message…"
          rows={1}
        />
        <input ref={fileInputRef} type="file" style={{ display: 'none' }} onChange={handleFile} />
        <button type="button" className="chat-composer-attach" aria-label="Attach image" disabled={disabled || sending} onClick={() => fileInputRef.current?.click()}>
          <span aria-hidden="true">⊕</span>
        </button>
        <button type="button" className="chat-composer-send" disabled={disabled || sending} onClick={handleSend}>
          Send
        </button>
      </div>
    </div>
  )
}

export function ChatTab({ run, now }: { run: Run; now: number }) {
  const { status, updates, liveIds, more, loadEarlier, retry } = useRunChat(run.id, true)
  const items = useMemo(() => buildItems(updates), [updates])
  const canAnswer = run.caps.terminal && run.state !== 'done' && run.state !== 'failed'
  const answerable = canAnswer && isClaudeAgent(run.agent)
  const [promptShown, setPromptShown] = useState(false)
  const suggestion = useMemo(() => suggestedCommand(updates), [updates])
  const [revealedIds, setRevealedIds] = useState<Set<string>>(new Set())
  const markRevealed = (id: string) => setRevealedIds((prev) => (prev.has(id) ? prev : new Set(prev).add(id)))
  const [revealVersion, setRevealVersion] = useState(0)
  const bumpReveal = useCallback(() => setRevealVersion((v) => v + 1), [])
  const reducedMotion = useMemo(() => prefersReducedMotion(), [])
  useEffect(() => { preloadChatMarkdown() }, [])

  const [pending, setPending] = useState<Optimistic[]>([])
  const [nowTick, setNowTick] = useState(() => Date.now())

  useEffect(() => {
    if (pending.length === 0) return
    const id = setInterval(() => setNowTick(Date.now()), 5000)
    return () => clearInterval(id)
  }, [pending.length])

  const reconciled = useMemo(() => reconcileOptimistic(pending, updates, nowTick), [pending, updates, nowTick])
  // Drop confirmed bubbles during render (React's "adjusting state" pattern,
  // as in useTerminalLifecycle.ts).
  if (reconciled.confirmed.length > 0) {
    setPending((p) => p.filter((x) => !reconciled.confirmed.includes(x.localId)))
  }

  // "Since" is the newest transcript timestamp seen: provisionalTool compares
  // it with u.ts, and render stays free of Date.now().
  const latestTs = updates.length ? updates[updates.length - 1].ts : 0
  const [toolShown, setToolShown] = useState<{ tool: string; since: number } | null>(
    run.activity.tool ? { tool: run.activity.tool, since: latestTs } : null,
  )
  if (run.activity.tool !== toolShown?.tool) {
    setToolShown(run.activity.tool ? { tool: run.activity.tool, since: latestTs } : null)
  }
  const provisional = toolShown ? provisionalTool(run, updates, toolShown.since) : null

  const { ref: scroller, stuck, onScroll, preserveAnchor, scrollToLatest } = useStickyScroll<HTMLDivElement>(
    [items, provisional, reconciled.remaining, revealVersion],
    items[0]?.seq,
  )

  const handleShowEarlier = () => {
    preserveAnchor()
    void loadEarlier()
  }

  const handleSend = async (text: string) => {
    const localId = `local-${Date.now()}-${Math.random().toString(36).slice(2)}`
    setPending((p) => [...p, { localId, text, sentAt: Date.now() }])
    const err = await sendText({ kind: 'run', id: run.id }, text)
    if (err) setPending((p) => p.filter((x) => x.localId !== localId))
    return err
  }

  const questionPending = answerable && items.some(
    (i) => i.kind === 'question' && i.call.status !== 'completed' && i.call.status !== 'failed',
  )

  const header = (
    <>
      <RunStatusStrip run={run} now={now} />
      {!(status === 'ready' && run.question?.kind === 'turn') && (
        <RunQuestion run={run} answeredHere={promptShown || questionPending} />
      )}
      <PermissionBar run={run} onPrompt={setPromptShown} />
    </>
  )

  if (status === 'loading') {
    return (
      <div className="chat">
        {header}
        <div className="run-detail-empty">Loading chat…</div>
      </div>
    )
  }
  if (status === 'unavailable' || status === 'error') {
    return (
      <div className="chat">
        {header}
        <div className="run-detail-empty">
          <p>{status === 'unavailable' ? 'Chat unavailable' : "Couldn't load chat"}</p>
          <button type="button" className="run-detail-back-cta" onClick={retry}>Retry</button>
        </div>
      </div>
    )
  }

  return (
    <div className="chat">
      {header}

      <div className="chat-timeline-wrap">
        <div className="chat-timeline" ref={scroller} onScroll={onScroll}>
          {more && (
            <button type="button" className="chat-earlier" onClick={handleShowEarlier}>
              Show earlier
            </button>
          )}

          {items.map((item) => (
            <ChatItemRow
              key={item.id}
              item={item}
              runId={run.id}
              canAnswer={canAnswer}
              answerable={answerable}
              live={liveIds.has(item.id)}
              reducedMotion={reducedMotion}
              revealed={revealedIds}
              onRevealed={markRevealed}
              onTick={bumpReveal}
            />
          ))}

          {reconciled.remaining.map((p) => (
            <div
              key={p.localId}
              className={`chat-bubble chat-user chat-optimistic${p.unconfirmed ? ' unconfirmed' : ''}`}
              data-id={p.localId}
            >
              {p.text}
              {p.unconfirmed && <span className="chat-optimistic-flag">not confirmed</span>}
            </div>
          ))}

          {provisional && (
            <div className="chat-provisional" data-id="provisional">
              <span className="chat-tool-name">{provisional.tool}</span>
              {provisional.hint && <span className="chat-hint">{provisional.hint}</span>}
              <span className="chat-provisional-marker" aria-hidden="true">⋯</span>
            </div>
          )}

          {(run.state === 'running' || run.state === 'thinking') && (
            <div className="chat-working">
              <span className="chat-working-dot" aria-hidden="true">●</span>
              working…
            </div>
          )}
        </div>

        {!stuck && (
          <button type="button" className="chat-latest" onClick={scrollToLatest}>
            ↓ Latest
          </button>
        )}
      </div>

      <Composer key={run.draft_key ?? run.id} run={run} suggestion={suggestion} onSend={handleSend} />
    </div>
  )
}
