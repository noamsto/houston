interface Entry {
  url: string
  state: unknown
}

export interface FakeHistory extends History {
  entries: Entry[]
  index: number
}

/**
 * happy-dom's History neither restores `state` nor fires `popstate` on
 * back()/go(). This installs an in-memory one that behaves like a browser:
 * push/replace fire no events; traversal is async and fires `popstate`, plus
 * `hashchange` when the hash changed.
 */
export function installFakeHistory(): { history: FakeHistory; uninstall: () => void } {
  const original = window.history
  const descriptor = Object.getOwnPropertyDescriptor(window, 'history')
  const sync = (url: string) => original.replaceState(null, '', url)
  const entries: Entry[] = [{ url: window.location.href, state: null }]
  let index = 0

  const traverse = (delta: number) => {
    setTimeout(() => {
      const target = index + delta
      if (delta === 0 || target < 0 || target >= entries.length) return
      const oldURL = window.location.href
      const oldHash = window.location.hash
      index = target
      sync(entries[index].url)
      window.dispatchEvent(new PopStateEvent('popstate', { state: entries[index].state }))
      if (window.location.hash !== oldHash) {
        window.dispatchEvent(new HashChangeEvent('hashchange', { oldURL, newURL: window.location.href }))
      }
    }, 0)
  }

  const resolve = (url?: string | URL | null) => (url == null ? entries[index].url : new URL(String(url), window.location.href).href)

  const fake = {
    entries,
    get index() { return index },
    get length() { return entries.length },
    get state() { return entries[index].state },
    scrollRestoration: 'auto',
    pushState(state: unknown, _title: string, url?: string | URL | null) {
      const href = resolve(url)
      entries.splice(index + 1)
      entries.push({ url: href, state })
      index = entries.length - 1
      sync(href)
    },
    replaceState(state: unknown, _title: string, url?: string | URL | null) {
      const href = resolve(url)
      entries[index] = { url: href, state }
      sync(href)
    },
    back: () => traverse(-1),
    forward: () => traverse(1),
    go: (delta = 0) => traverse(delta),
  } as FakeHistory

  Object.defineProperty(window, 'history', { value: fake, configurable: true })
  return {
    history: fake,
    uninstall: () => {
      if (descriptor) Object.defineProperty(window, 'history', descriptor)
      else Reflect.deleteProperty(window, 'history')
    },
  }
}

/** Lets a queued back()/go() land. */
export const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 5))
