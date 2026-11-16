export type CrewActionOutcome =
  | { ok: true; output: string }
  | { ok: false; status: number; message: string; confirm?: string }

// Mirror of the server's POST /api/runs/{id}/crew/action contract: 200
// {output}, 428 {confirm} for a merge sent without confirm, anything else a
// plain-text body.
export async function runCrewAction(
  runId: string,
  item: string,
  opts: { text?: string; confirm?: boolean } = {},
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
  const body = (await res.text()).trim()
  if (res.ok) return { ok: true, output: (JSON.parse(body) as { output: string }).output }
  if (res.status === 428) {
    const { confirm } = JSON.parse(body) as { confirm: string }
    return { ok: false, status: 428, message: confirm, confirm }
  }
  return { ok: false, status: res.status, message: body }
}
