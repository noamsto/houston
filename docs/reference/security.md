# Security

Token gate, Origin and Host allowlists, peer tokens.

Moved verbatim from the root `CLAUDE.md`; section headings are unchanged.

## Security

**Token-gated API.** On first run houston generates a random token into
`<status-dir>/token` (`0600`). Every `/api/` request must present it as the
`houston_token` cookie, an `Authorization: Bearer` header, or a `?token=` query
parameter. The query parameter is accepted **only on a WebSocket upgrade**,
which is the one request a browser can't attach a header to; everywhere else it
would just leak the token into history and proxy logs. Loading the SPA issues
the cookie as an httpOnly `SameSite=Strict` response cookie, so a browser that
has opened the UI once authenticates transparently on every subsequent call.
The cookie is issued **only to connections from a loopback address**, decided
from `r.RemoteAddr` alone (`X-Forwarded-For`, `X-Real-IP` and `Forwarded` are
ignored), because the Host allowlist admits the Tailscale names and so cannot
keep a network client from loading the page. A browser reaches a non-loopback
houston through a local reverse proxy, an SSH tunnel or another houston's Fleet.
A local proxy arrives from loopback, so every client it admits receives the
token: the proxy must authenticate or restrict who can reach it. A non-loopback client still gets the page but no token, so its API calls get
401; peers keep authenticating with `Authorization: Bearer`.

**Origin allowlist.** Requests are checked against same-origin plus, under
`-debug`, the Vite dev server — covering both the HTTP API and the WebSocket
handshake, so a page from another origin is refused even with a valid token.

**Host allowlist.** Origin checking alone cannot stop DNS rebinding: a rebound
attacker controls both `Host` and `Origin`, so they always agree. houston
therefore pins the `Host` header, derived from what the machine knows about
itself — `os.Hostname()`, its Tailscale address (CGNAT `100.64.0.0/10`), the
reverse-DNS name of that address, and loopback literals. It never resolves a
client-supplied name, because under rebinding that resolves to us and would
validate the attacker's own claim. An unrecognised `Host` gets **421** and, more
importantly, is never issued a token. If you reach houston by a name it can't
derive — a reverse proxy, custom DNS — add it with `-hostname` (repeatable).

`-no-auth` disables the **token** only. Origin and Host checking still apply:
turning off authentication shouldn't make the server cross-origin drivable.
It also turns off forwarding to peers (403 `peer actions need auth`, no
forwardable caps), since a forward acts on the peer with the peer's token.

`/api/debug/pprof/` (`net/http/pprof`) is registered only under `-debug`, and
sits behind the same token and Host gates as the rest of `/api/`.

**Peer tokens.** A peer's token (`-peer`/`-peer-token-file`, see "Federation (peers)") is sent only as an `Authorization: Bearer` header to that peer's own URL: the source's client uses no proxy and follows no redirect (forwarded requests and the terminal WebSocket use the same transport, dial, token and cleartext-hop check, and a forwarded request is built from scratch, so the browser's cookie, `Authorization`, `Origin` and `?token=` are never sent to a peer), a bad token file shows up as a health class, and the token is never logged or returned by `/api/hosts`. Plain `http` to a peer is refused at startup by the URL rule (loopback or tailnet names and addresses only) and again at dial time unless the connection is loopback or has tailnet addresses on both ends (class `insecure`, before any byte is written), so a name that resolves to a public address never gets the token in cleartext. An IPv4 tailnet hop also needs the local address on a Tailscale interface (`tailscale*`, or `utun*` on macOS), since `100.64.0.0/10` is also carrier-grade NAT space, so a `100.x` uplink address with tailscaled down is refused. Residual: on macOS another VPN's `utun` interface holding a `100.64/10` address still passes; use `https` to rule that out. The peer's own token, Origin and Host gates apply to the dial as to any client.

This model is the primary defense; the following are additional layers:
1. Default bind: `127.0.0.1:9090` (localhost only)
2. Browser access through a local reverse proxy that restricts its clients (Tailscale Serve admits the whole tailnet)
3. Or SSH tunnel: `ssh -L 9090:localhost:9090 host`

## Crew actions

`POST /api/runs/{id}/crew/action` (dispatcher mode, 404 in tmux mode) lets the UI run a verb crew itself offered for an attention item (`runs/crewview.go`, `server/runs_crewaction.go`, `crewview/action.go`). It runs a command, so each rule below closes a specific hole:

- **No client argv.** The body is `{item, text?, confirm?}`: an item id, never a command. The server looks the id up in its own current view for that run's bus (`ownedItem`: same crew, and the run's branch for a worker or a branchless item for a dispatcher), so a client cannot name another crew's or worker's item. The wire (`crew.attention`) carries `action.{verb,label,command,needs_input,pr}` for display; the argv never leaves the server.
- **Allowlist by exact shape** (`crewview.Check`). Only `peek` (runs `where`), `nudge`, `reply` and `merge`, each with the exact argv after `crew` that houston derives from the item (`--crew` matching the item's crew, the item's branch or worker, `reply`'s last word the `<text>` placeholder, `merge <pr> --crew <crew>`). A producer's argv that differs, a verb outside the list, an origin other than `local` or `remote_worker`, or an `argv[0]` whose base name is not `crew` is 403 `not executable`, so a compromised or newer crew cannot make houston run an arbitrary command.
- **houston runs its own `crew`.** `argv[0]` is never executed; `server/crew_exec.go` runs the binary resolved once at startup (`resolveCrew`: the tmux server's PATH ahead of houston's own, `mode.Find`) with `argv[1:]`, directly (no shell), with cwd the git root the view was started in. The same binary serves the view stream and `/reply`.
- **Environment stripped.** The view stream (`crewview` `strippedEnv`) and the action (`strippedCrewEnv`, `server/crew_exec.go`) run without `CREW_BIN`, `CREW_ID`, `CREW_WORKER_ID`, `TMUX_PANE` and `DISPATCH_SPEC`: `crew dash` folds `CREW_BIN` into every argv it prints, so an inherited value would put another program in `argv[0]`, and the others would make crew act as houston's own pane or worker. The action then sets `CREW_ID` to the item's crew.
- **Merge needs confirmation.** `merge` without `"confirm": true` is 428 `{"confirm": "Squash-merge PR #N?"}` and runs nothing; the UI asks inline. It is the only irreversible verb.
- **Reply text** is required for `reply` (empty or over 8 KiB, `crewview.MaxReplyText`, is 400) and replaces the placeholder as one argv element; it is the only client-supplied content, and a `--` precedes it so a leading dash is not a flag. Body over the size cap is 413.
- **Timeouts**: 15 s, 120 s for `merge` (it waits on `gh` and the remote); a timeout is 504.
- **Never forwarded to a peer.** The route is `localOnlyRun`: a `peer-` run id gets 403 `peer actions are not available`, and the peer source's `sanitise` (`peer/source.go`) drops `action` from every mirrored run's attention items (the display-only `command` text stays), so a mirrored run never offers a button.
- **Logging.** One line per attempt with run id, item kind, verb and status; never the text, argv, item message or command output, which carry task content.
