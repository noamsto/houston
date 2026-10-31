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
2. Access via Tailscale (recommended)
3. Or SSH tunnel: `ssh -L 9090:localhost:9090 host`


