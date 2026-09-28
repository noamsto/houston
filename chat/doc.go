// Package chat turns an agent's own session transcript into a replayable
// stream of ACP (Agent Client Protocol) session/update notifications. It is
// standalone — it imports only the standard library (enforced by
// boundary_test.go) — because reading transcripts is a different job from
// routing hook events, and future consumers (an ACP client, aeye's
// session-backfill) shouldn't need the rest of houston.
//
// # Reader contract
//
// A Reader turns one engine's transcript file into Updates. Read is called
// repeatedly with a Cursor carried forward by the caller; it must be
// chunking-independent: reading a file in one call or in any number of
// incremental calls yields the same sequence of Updates with the same IDs.
// This makes Update.ID ("<line-byte-offset>:<n>", n = the update's 0-based
// index within that line) stable across houston restarts and lets a caller
// re-read from byte 0 and land on IDs it already holds.
//
// Consequences: a Reader consumes only complete lines (terminated by '\n');
// a trailing partial line is left for the next call (the returned Cursor's
// Offset stops before it). No Update is ever replaced or retracted by a
// Reader — an ID, once emitted, is final. If the file is shorter than the
// Cursor's Offset, or no longer holds the bytes the Cursor was taken after
// (truncated and regrown, or a new session reusing the same path — a Reader
// may fingerprint them in Cursor.Pending, which callers treat as opaque),
// Read reports reset=true and starts over from offset 0; the caller is
// responsible for treating that as a new stream (new epoch, ordinals
// restart) since Seq itself is assigned by the caller, not the Reader.
//
// A Reader's methods are safe for concurrent use: no per-file state persists
// across calls (all carry-over lives in the caller's Cursor), so the same
// Reader can serve a live tail, a scroll-back page, and a tool-detail lookup
// concurrently.
//
// # Output contract
//
// Each transcript message becomes its own *_message_chunk or tool_call the
// moment it's read — text is never held back waiting for a message to
// close, because that would delay narration until a following tool call's
// input finishes streaming, and re-emitting a chunk once the message closes
// would violate the "no update is ever replaced" rule above. Instead,
// Update.Meta["messageId"] groups every chunk and tool_call that belongs to
// one source message. A consumer derives "commentary" (assistant text
// between tool calls, rendered dimmer) from that: an agent_message_chunk is
// commentary when Meta["phase"] == "commentary" (an engine may set this
// explicitly) or when a later tool_call in the stream shares its
// Meta["messageId"] — so the commentary style may apply only after the text
// has already rendered, once its tool_call arrives.
//
// Other Meta keys: "tool" names the tool on a tool_call (the human-readable
// hint rides Title instead — see ToolTitle); "origin" marks a
// user_message_chunk that came from something other than a typed human
// prompt (e.g. "task-notification"); "subagent" names the agent id a
// tool_call_update's result spawned, when that subagent's transcript file
// is known to exist.
package chat
