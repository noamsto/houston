import { useRuns } from '../hooks/useRuns'
import { useIsDesktop } from '../hooks/useMediaQuery'
import { useNow } from './useNow'
import { ConsoleShell } from './ConsoleShell'
import { MobileShell } from './MobileShell'
import '../theme/mocha.css'
import './fleet.css'

// The store sits above the layout switch so a resize across the breakpoint
// swaps layouts without reopening the EventSource.
export function Shell() {
  const { runs, connected, hasSnapshot } = useRuns()
  const now = useNow()
  const isDesktop = useIsDesktop()
  const Layout = isDesktop ? ConsoleShell : MobileShell
  return <Layout runs={runs} connected={connected} hasSnapshot={hasSnapshot} now={now} />
}
