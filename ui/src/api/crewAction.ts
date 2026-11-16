export type CrewActionOutcome =
  | { ok: true; output: string }
  | { ok: false; status: number; message: string; confirm?: string; pr?: number }

// Mirror of the server's POST /api/runs/{id}/crew/action contract: 200
// {output}; 428 {confirm,pr} for a merge sent without confirm; 409 {confirm,pr}
// when a confirmed merge named a stale PR; anything else a plain-text body.
export async function runCrewAction(
  runId: string,
  item: string,
  opts: { text?: string; confirm?: boolean; pr?: number } = {},
): Promise<CrewActionOutcome> {
  let res: Response
  try {
    res = await fetch(`/api/runs/${encodeURIComponent(runId)}/crew/action`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ item, ...opts }),
    })
  } catch (err) {
    return { ok: false, status: 0, message: err instanceof Error ? err.message : 'network error' }
  }
  let body: string
  try {
    body = (await res.text()).trim()
  } catch {
    return { ok: false, status: 0, message: 'network error' }
  }
  const malformed = (): CrewActionOutcome => ({ ok: false, status: res.status, message: 'malformed response: ' + body })
  const parse = (): Record<string, unknown> | null => {
    try {
      const v: unknown = JSON.parse(body)
      return typeof v === 'object' && v !== null ? (v as Record<string, unknown>) : null
    } catch {
      return null
    }
  }
  if (res.ok) {
    const parsed = parse()
    return typeof parsed?.output === 'string' ? { ok: true, output: parsed.output } : malformed()
  }
  if (res.status === 428) {
    const parsed = parse()
    if (typeof parsed?.confirm !== 'string') return malformed()
    const { confirm } = parsed
    return typeof parsed.pr === 'number'
      ? { ok: false, status: 428, message: confirm, confirm, pr: parsed.pr }
      : { ok: false, status: 428, message: confirm, confirm }
  }
  if (res.status === 409) {
    const parsed = parse()
    if (typeof parsed?.confirm === 'string' && typeof parsed.pr === 'number') {
      return { ok: false, status: 409, message: parsed.confirm, confirm: parsed.confirm, pr: parsed.pr }
    }
  }
  return { ok: false, status: res.status, message: body }
}
