import { useRuns } from '../hooks/useRuns'
import { useIsDesktop } from '../hooks/useMediaQuery'
import { useNow } from './useNow'
import { ConsoleShell } from './ConsoleShell'
import { MobileShell } from './MobileShell'
import '../theme/mocha.css'
import './fleet.css'

// Hoisting the store above the layout switch means crossing the breakpoint
// (window resize) swaps layouts without closing and reopening the
// EventSource — still exactly one data path.
export function Shell() {
  const { runs, connected, hasSnapshot } = useRuns()
  const now = useNow()
  const isDesktop = useIsDesktop()
  const Layout = isDesktop ? ConsoleShell : MobileShell
  return <Layout runs={runs} connected={connected} hasSnapshot={hasSnapshot} now={now} />
}
