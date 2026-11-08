import assert from "node:assert/strict";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { mock, test } from "node:test";
import { fileURLToPath } from "node:url";

type Handler = (event: unknown, ctx: unknown) => unknown;
type Record = { [k: string]: unknown; seq: number; ev: string; boot: string; ts: number };
type OnRequest = (
  req: http.IncomingMessage,
  res: http.ServerResponse,
  body: Record,
  index: number,
) => void;

const dir = path.dirname(fileURLToPath(import.meta.url));
const golden = JSON.parse(fs.readFileSync(path.join(dir, "testdata", "records.json"), "utf8"));

const ctx = {
  sessionManager: {
    getSessionId: () => "sid-1",
    getSessionFile: () => "/tmp/p/s.jsonl",
    getCwd: () => "/tmp/p",
  },
};

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function waitFor(pred: () => boolean, ms = 3000) {
  const end = Date.now() + ms;
  while (!pred()) {
    if (Date.now() > end) throw new Error("waitFor timed out");
    await sleep(10);
  }
}

async function harness(onRequest?: OnRequest) {
  const { default: factory } = await import("./houston.ts");
  mock.timers.enable({ apis: ["setInterval"] });
  const spy = mock.method(globalThis, "setInterval");
  const timeoutSpy = mock.method(globalThis, "setTimeout");
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), "pi-mod-"));
  process.env.HOUSTON_STATE_DIR = stateDir;
  delete process.env.HOUSTON_STATUS_DIR;
  process.env.TMUX = "/tmp/tmux-1000/default,4242,0";
  process.env.TMUX_PANE = "%7";

  const bodies: Record[] = [];
  const raws: string[] = [];
  let connections = 0;
  let closedConnections = 0;
  const server = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      const raw = Buffer.concat(chunks).toString("utf8");
      raws.push(raw);
      const body = JSON.parse(raw) as Record;
      bodies.push(body);
      if (onRequest) return onRequest(req, res, body, bodies.length - 1);
      res.statusCode = 204;
      res.end();
    });
  });
  server.on("connection", (sock) => {
    connections++;
    sock.on("close", () => closedConnections++);
  });
  await new Promise<void>((r) => server.listen(path.join(stateDir, "claude-mod.sock"), r));

  const load = () => {
    const loaded: { [name: string]: Handler } = {};
    const pi = {
      on(name: string, fn: Handler) {
        loaded[name] = fn;
      },
    };
    factory(pi as never);
    return loaded;
  };
  const handlers = load();
  const factorySetTimeoutCalls = timeoutSpy.mock.callCount();
  timeoutSpy.mock.restore();

  return {
    bodies,
    raws,
    handlers,
    load,
    connections: () => connections,
    closedConnections: () => closedConnections,
    setIntervalCalls: () => spy.mock.callCount(),
    factorySetTimeoutCalls,
    fire: (name: string, event: unknown = {}) => handlers[name](event, ctx),
    async close() {
      mock.timers.reset();
      mock.restoreAll();
      server.closeAllConnections();
      await new Promise((r) => server.close(r));
      fs.rmSync(stateDir, { recursive: true, force: true });
    },
  };
}

test("factory opens no connection and starts no timer", async () => {
  const h = await harness();
  try {
    await sleep(100);
    assert.equal(h.connections(), 0);
    assert.equal(h.setIntervalCalls(), 0);
    assert.equal(h.factorySetTimeoutCalls, 0);
    mock.timers.tick(15000);
    await sleep(50);
    assert.equal(h.bodies.length, 0);
  } finally {
    await h.close();
  }
});

test("golden record stream", async () => {
  const h = await harness();
  try {
    await h.fire("session_start", { reason: "startup" });
    await h.fire("agent_start", {});
    await h.fire("turn_start", { turnIndex: 0, timestamp: 1 });
    await h.fire("tool_execution_start", {
      toolCallId: "tc1",
      toolName: "bash",
      args: { cmd: "SENTINEL_ARG" },
    });
    await h.fire("tool_execution_end", {
      toolCallId: "tc1",
      toolName: "bash",
      result: "SENTINEL_RESULT",
      isError: false,
    });
    await h.fire("session_compact", {});
    await h.fire("agent_settled", { aborted: false });
    await waitFor(() => h.bodies.length === 7);
    await sleep(50);
    mock.timers.tick(15000);
    await waitFor(() => h.bodies.length === 8);
    await h.fire("session_shutdown", { reason: "quit" });

    const boots = new Set(h.bodies.map((b) => b.boot));
    assert.equal(boots.size, 1);
    assert.deepEqual(
      h.bodies.map((b) => b.seq),
      h.bodies.map((_, i) => i + 1),
    );
    const normalized = h.bodies.map((b) => ({ ...b, boot: "BOOT", ts: 1000 }));
    assert.deepEqual(normalized, golden);
    for (const raw of h.raws) assert.ok(!raw.includes("SENTINEL"), raw);
  } finally {
    await h.close();
  }
});

test("every handler returns undefined", async () => {
  const h = await harness();
  try {
    const events: { [name: string]: unknown } = {
      session_start: { reason: "startup" },
      turn_start: { turnIndex: 0, timestamp: 1 },
      tool_execution_start: { toolCallId: "tc1", toolName: "bash", args: {} },
      tool_execution_end: { toolCallId: "tc1", toolName: "bash", result: "", isError: false },
      agent_settled: { aborted: false },
      session_shutdown: { reason: "quit" },
    };
    const names = Object.keys(h.handlers).filter((n) => n !== "session_shutdown");
    assert.ok(names.includes("session_compact_failed"));
    for (const name of ["session_start", ...names.filter((n) => n !== "session_start")]) {
      assert.equal(await h.fire(name, events[name] ?? {}), undefined, name);
    }
    assert.equal(await h.fire("session_shutdown", events.session_shutdown), undefined);
  } finally {
    await h.close();
  }
});

test("aborted settle reports reason aborted", async () => {
  const h = await harness();
  try {
    await h.fire("session_start", { reason: "startup" });
    await h.fire("agent_settled", { aborted: true });
    await waitFor(() => h.bodies.length === 2);
    const last = h.bodies[h.bodies.length - 1];
    assert.equal(last.ev, "turn.complete");
    assert.equal(last.reason, "aborted");
  } finally {
    await h.close();
  }
});

test("tool error sets err", async () => {
  const h = await harness();
  try {
    await h.fire("session_start", { reason: "startup" });
    await h.fire("tool_execution_end", {
      toolCallId: "tc1",
      toolName: "bash",
      result: "x",
      isError: true,
    });
    await waitFor(() => h.bodies.length === 2);
    const last = h.bodies[h.bodies.length - 1];
    assert.equal(last.ev, "tool.end");
    assert.equal(last.err, true);
  } finally {
    await h.close();
  }
});

test("shutdown reload posts no session.end", async () => {
  const h = await harness();
  try {
    await h.fire("session_start", { reason: "startup" });
    await h.fire("session_shutdown", { reason: "reload" });
    await sleep(150);
    assert.deepEqual(
      h.bodies.map((b) => b.ev),
      ["session.start"],
    );
  } finally {
    await h.close();
  }
});

test("shutdown quit flushes session.end and stops posting", async () => {
  const h = await harness();
  try {
    await h.fire("session_start", { reason: "startup" });
    await h.fire("session_shutdown", { reason: "quit" });
    const evs = h.bodies.map((b) => b.ev);
    assert.deepEqual(evs, ["session.start", "session.end"]);

    await h.fire("turn_start", { turnIndex: 1, timestamp: 2 });
    mock.timers.tick(15000);
    await sleep(150);
    assert.equal(h.bodies.length, 2);
  } finally {
    await h.close();
  }
});

test("retries a record whose connection dropped", async () => {
  const arrivals: number[] = [];
  const h = await harness((req, res, _body, i) => {
    arrivals.push(Date.now());
    if (i === 0) return req.socket.destroy();
    res.statusCode = 204;
    res.end();
  });
  try {
    await h.fire("session_start", { reason: "startup" });
    await h.fire("turn_start", { turnIndex: 0, timestamp: 1 });
    await waitFor(() => h.bodies.length >= 3, 4000);
    await sleep(100);
    assert.deepEqual(
      h.bodies.map((b) => [b.seq, b.ev]),
      [
        [1, "session.start"],
        [1, "session.start"],
        [2, "step.start"],
      ],
    );
    assert.deepEqual(h.bodies[0], h.bodies[1]);
    const gap = arrivals[1] - arrivals[0];
    assert.ok(gap >= 900, `retry came after ${gap}ms`);
  } finally {
    await h.close();
  }
});

test("heartbeat posts on tick but not while a record waits", async () => {
  let release: (() => void) | undefined;
  const h = await harness((_req, res, _body, i) => {
    const done = () => {
      res.statusCode = 204;
      res.end();
    };
    if (i === 0) return void (release = done);
    done();
  });
  try {
    h.fire("session_start", { reason: "startup" });
    await waitFor(() => h.bodies.length === 1);
    mock.timers.tick(15000);
    await sleep(150);
    assert.equal(h.bodies.length, 1);
    release?.();
    await sleep(150);
    assert.equal(h.bodies.filter((b) => b.ev === "heartbeat").length, 0);

    mock.timers.tick(15000);
    await waitFor(() => h.bodies.some((b) => b.ev === "heartbeat"));
    assert.equal(h.bodies.filter((b) => b.ev === "heartbeat").length, 1);
  } finally {
    await h.close();
  }
});

test("queue is capped to the newest 256 waiting records", async () => {
  let release: (() => void) | undefined;
  const h = await harness((_req, res, _body, i) => {
    const done = () => {
      res.statusCode = 204;
      res.end();
    };
    if (i === 0) return void (release = done);
    done();
  });
  try {
    h.fire("session_start", { reason: "startup" });
    await waitFor(() => h.bodies.length === 1);
    for (let i = 0; i < 300; i++) h.fire("turn_start", { turnIndex: i, timestamp: i });
    await sleep(100);
    release?.();
    await waitFor(() => h.bodies.length >= 257);
    await sleep(300);
    const seqs = h.bodies.map((b) => b.seq);
    assert.equal(seqs.length, 257);
    assert.equal(seqs.filter((s) => s === 1).length, 1);
    const waiting = seqs.slice(1);
    assert.equal(waiting.length, 256);
    assert.deepEqual(
      waiting,
      Array.from({ length: 256 }, (_, i) => 46 + i),
    );
  } finally {
    await h.close();
  }
});

test("shutdown flush is bounded when the server never answers", async () => {
  const h = await harness(() => {});
  try {
    h.fire("session_start", { reason: "startup" });
    await waitFor(() => h.bodies.length === 1);
    const t0 = Date.now();
    await h.fire("session_shutdown", { reason: "quit" });
    const elapsed = Date.now() - t0;
    assert.ok(elapsed < 2000, `flush took ${elapsed}ms`);
    await waitFor(() => h.closedConnections() === 1, 500);
    await sleep(1500);
    assert.equal(h.bodies.length, 1);
  } finally {
    await h.close();
  }
});

test("shutdown quit during retry backoff still delivers session.end", async () => {
  const h = await harness((req, res, _body, i) => {
    if (i === 0) return req.socket.destroy();
    res.statusCode = 204;
    res.end();
  });
  try {
    await h.fire("session_start", { reason: "startup" });
    await waitFor(() => h.bodies.length === 1);
    await sleep(50);
    await h.fire("session_shutdown", { reason: "quit" });
    assert.equal(h.bodies[h.bodies.length - 1].ev, "session.end");
  } finally {
    await h.close();
  }
});

test("each load of the extension gets its own boot", async () => {
  const h = await harness();
  try {
    await h.fire("session_start", { reason: "startup" });
    await waitFor(() => h.bodies.length === 1);
    await h.fire("session_shutdown", { reason: "reload" });

    const second = h.load();
    await second.session_start({ reason: "reload" }, ctx);
    await waitFor(() => h.bodies.length === 2);

    assert.notEqual(h.bodies[0].boot, h.bodies[1].boot);
    assert.equal(h.bodies[1].seq, 1);
  } finally {
    await h.close();
  }
});

test("heartbeat is skipped while a dropped record waits to retry", async () => {
  const h = await harness((req, res, _body, i) => {
    if (i === 0) return req.socket.destroy();
    res.statusCode = 204;
    res.end();
  });
  try {
    await h.fire("session_start", { reason: "startup" });
    await waitFor(() => h.bodies.length === 1);
    await sleep(50);
    mock.timers.tick(15000);
    await waitFor(() => h.bodies.length === 2, 4000);
    await sleep(150);
    assert.equal(h.bodies.filter((b) => b.ev === "heartbeat").length, 0);
  } finally {
    await h.close();
  }
});
