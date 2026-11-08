// Read-only contract: subscribes to run-state events only, returns nothing from any
// handler, and never reads houston's reply.
import { randomUUID } from "node:crypto";
import http from "node:http";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

type Fields = { [k: string]: string | boolean | undefined };
type SessionCtx = {
  sessionManager: {
    getSessionId(): string | undefined;
    getSessionFile(): string | undefined;
    getCwd(): string | undefined;
  };
};

const MAX_WAITING = 256;
const HEARTBEAT_MS = 15000;
const RETRY_MS = 1000;
const REQUEST_TIMEOUT_MS = 2000;
const FLUSH_MS = 1000;

function stateDir(): string {
  const env = process.env;
  return env.HOUSTON_STATE_DIR || env.HOUSTON_STATUS_DIR || `${env.HOME}/.local/state/houston`;
}

function coords(ctx: SessionCtx): Fields {
  return {
    cwd: ctx.sessionManager.getCwd(),
    transcript: ctx.sessionManager.getSessionFile(),
    pane: process.env.TMUX_PANE,
    tmux_server: process.env.TMUX?.split(",")[1],
  };
}

function createShipper() {
  const st = {
    boot: randomUUID(),
    seq: 0,
    sid: "",
    socketPath: "",
    closed: false,
    queue: [] as string[],
    inflight: undefined as string | undefined,
    inflightReq: undefined as http.ClientRequest | undefined,
    heartbeat: undefined as ReturnType<typeof setInterval> | undefined,
    retryTimer: undefined as ReturnType<typeof setTimeout> | undefined,
    idleWaiters: [] as (() => void)[],
  };

  function notifyIdle(): void {
    if (st.inflight !== undefined || st.queue.length > 0) return;
    const waiters = st.idleWaiters;
    st.idleWaiters = [];
    for (const w of waiters) w();
  }

  function schedule(): void {
    if (st.retryTimer !== undefined) return;
    st.retryTimer = setTimeout(() => {
      st.retryTimer = undefined;
      pump();
    }, RETRY_MS);
    st.retryTimer.unref();
  }

  function pump(): void {
    if (st.inflight !== undefined || st.retryTimer !== undefined) return;
    const body = st.queue.shift();
    if (body === undefined) return;
    st.inflight = body;
    let settled = false;
    const done = (delivered: boolean): void => {
      if (settled) return;
      settled = true;
      st.inflightReq = undefined;
      if (!delivered && !st.closed) {
        st.queue.unshift(body);
        st.queue = st.queue.slice(-MAX_WAITING);
        schedule();
      }
      st.inflight = undefined;
      pump();
      notifyIdle();
    };
    const req = http.request(
      {
        socketPath: st.socketPath,
        method: "POST",
        path: "/v1/record",
        headers: {
          host: "houston.invalid",
          "content-type": "application/json",
          "content-length": Buffer.byteLength(body, "utf8"),
        },
        agent: false,
      },
      (res) => {
        res.resume();
        done(true);
      },
    );
    st.inflightReq = req;
    req.setTimeout(REQUEST_TIMEOUT_MS, () => req.destroy());
    req.on("error", () => done(false));
    req.on("close", () => done(false));
    req.end(body);
  }

  function post(ev: string, fields: Fields = {}): void {
    if (st.closed || !st.sid) return;
    st.seq++;
    const record: { [k: string]: unknown } = {
      v: 1,
      engine: "pi",
      sid: st.sid,
      boot: st.boot,
      seq: st.seq,
      ts: Date.now(),
      ev,
    };
    for (const [k, v] of Object.entries(fields)) {
      if (v !== undefined && v !== "") record[k] = v;
    }
    st.queue.push(JSON.stringify(record));
    if (st.queue.length > MAX_WAITING) st.queue = st.queue.slice(-MAX_WAITING);
    pump();
  }

  function startHeartbeat(): void {
    if (st.heartbeat !== undefined) clearInterval(st.heartbeat);
    st.heartbeat = setInterval(() => {
      if (st.inflight === undefined && st.queue.length === 0) post("heartbeat");
    }, HEARTBEAT_MS);
    st.heartbeat.unref();
  }

  function sessionStart(ctx: SessionCtx): void {
    const id = ctx.sessionManager.getSessionId();
    if (!id) return;
    st.closed = false;
    st.sid = id;
    st.socketPath = `${stateDir()}/claude-mod.sock`;
    startHeartbeat();
    post("session.start", coords(ctx));
  }

  async function sessionShutdown(reason: string | undefined): Promise<void> {
    if (st.closed) return;
    if (reason !== "reload") post("session.end", { reason });
    st.closed = true;
    if (st.heartbeat !== undefined) clearInterval(st.heartbeat);
    st.heartbeat = undefined;
    if (st.retryTimer !== undefined) clearTimeout(st.retryTimer);
    st.retryTimer = undefined;
    if (st.inflight === undefined && st.queue.length === 0) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const bound = new Promise<void>((resolve) => {
      timer = setTimeout(resolve, FLUSH_MS);
    });
    const drained = new Promise<void>((resolve) => {
      st.idleWaiters.push(resolve);
    });
    pump();
    await Promise.race([drained, bound]);
    clearTimeout(timer);
    st.queue = [];
    st.inflightReq?.destroy();
  }

  return { post, sessionStart, sessionShutdown };
}

export default function (pi: ExtensionAPI) {
  const shipper = createShipper();
  pi.on("session_start", async (_event, ctx) => {
    shipper.sessionStart(ctx);
  });
  pi.on("agent_start", async (_event, ctx) => {
    shipper.post("turn.start", coords(ctx));
  });
  pi.on("turn_start", async () => {
    shipper.post("step.start");
  });
  pi.on("tool_execution_start", async (event) => {
    shipper.post("tool.start", { tool: event.toolName, id: event.toolCallId });
  });
  pi.on("tool_execution_end", async (event) => {
    shipper.post("tool.end", { tool: event.toolName, id: event.toolCallId, err: event.isError || undefined });
  });
  pi.on("agent_settled", async (event) => {
    shipper.post("turn.complete", { reason: event.aborted ? "aborted" : "answer" });
  });
  pi.on("session_compact", async () => {
    shipper.post("compact.end");
  });
  pi.on("session_compact_failed", async () => {
    shipper.post("compact.end");
  });
  pi.on("session_shutdown", async (event) => {
    await shipper.sessionShutdown(event.reason);
  });
}
