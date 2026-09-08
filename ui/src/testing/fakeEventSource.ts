/**
 * A minimal EventSource stand-in for tests that need deterministic control
 * over connection lifecycle and event delivery — real EventSource has
 * neither.
 */

type Listener = (ev: MessageEvent) => void

export class FakeEventSource {
  url: string
  closed = false
  readyState = 0
  onerror: ((ev: Event) => void) | null = null

  private listeners = new Map<string, Set<Listener>>()

  constructor(url: string) {
    this.url = url
  }

  addEventListener(type: string, listener: Listener): void {
    let set = this.listeners.get(type)
    if (!set) {
      set = new Set()
      this.listeners.set(type, set)
    }
    set.add(listener)
  }

  removeEventListener(type: string, listener: Listener): void {
    this.listeners.get(type)?.delete(listener)
  }

  close(): void {
    this.closed = true
    this.readyState = 2
  }

  emit(type: string, payload: unknown): void {
    this.dispatch(type, JSON.stringify(payload))
  }

  /** Unlike `emit`, sends `data` verbatim — for tests of malformed payloads,
   * where stringifying a string would round-trip back into valid JSON. */
  emitRaw(type: string, data: string): void {
    this.dispatch(type, data)
  }

  open(): void {
    this.readyState = 1
    this.dispatch('open', '')
  }

  error(): void {
    this.onerror?.(new Event('error'))
  }

  private dispatch(type: string, data: string): void {
    const ev = new MessageEvent(type, { data })
    for (const listener of this.listeners.get(type) ?? []) {
      listener(ev)
    }
  }
}

export interface FakeEventSourceHandle {
  instances: FakeEventSource[]
  uninstall: () => void
}

/**
 * Installs FakeEventSource as `globalThis.EventSource` and returns a handle
 * with a fresh registry of every instance constructed while installed.
 */
export function installFakeEventSource(): FakeEventSourceHandle {
  const instances: FakeEventSource[] = []
  const original = globalThis.EventSource

  class TrackedFakeEventSource extends FakeEventSource {
    constructor(url: string) {
      super(url)
      instances.push(this)
    }
  }

  globalThis.EventSource = TrackedFakeEventSource as unknown as typeof EventSource

  return {
    instances,
    uninstall: () => {
      globalThis.EventSource = original
    },
  }
}
