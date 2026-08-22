// Package store owns the on-disk persistence for the agent harness.
//
// # Provider registry
//
// models.json is hand-edited and read-only from the app's point of view. The
// ProviderStore loads it once and serves it in memory:
//
//	{
//	  "providers": {
//	    "local-llama": {
//	      "endpoint": "http://127.0.0.1:8080",
//	      "api_type": "openai",
//	      "models": {
//	        "qwen2.5-7b": { "name": "qwen2.5-7b-instruct", "context_window": 32768 }
//	      }
//	    }
//	  }
//	}
//
// The provider name is the map key; the model key is what the UI shows and its
// optional "name" field is the exact id sent to the provider's API (falls back
// to the key). No defaults, no hot reload: edits are picked up on restart.
//
// # Session transcripts
//
// Every session is one <uuid>.jsonl file under sessions/. Each line is one
// JSON record:
//
//	{"kind":"meta",...}                        session header (one-time facts)
//	{"kind":"message","message":{...}}         one full message (system/user/assistant)
//	{"kind":"tool_call",...} / {"kind":"tool_result",...}
//	{"kind":"meta",...}                        header update (turn_count, model switch)
//
// # The stream vs the file
//
// When the model runs it streams. Deltas are LIVE ONLY — they are never
// written to the transcript file:
//
//	provider SSE chunks
//	   │  readStream()  (models/generic_openai.go)
//	   ├─► opts.OnDelta(DeltaContent, text)  →  Recorder.Record(KindDelta, ...)
//	   │        │
//	   │        └─► sinks (SSE to the user / stdout) ONLY
//	   │             Recorder skips the file for KindDelta
//	   │
//	   └─► content.WriteString(...) / reasoning.WriteString(...)  ← same call
//
// readStream does both in one pass: it forwards each chunk to the live hook
// AND accumulates the whole reply into strings.Builders. When the stream ends
// it returns ONE consolidated message:
//
//	msg := contracts.ChatMessage{
//	    Role:             "assistant",
//	    Content:          &content.String(),   // full text, deltas joined
//	    ReasoningContent: reasoning.String(),
//	}
//
// That full message then flows: agent.Invoke appends it to history, and
// session.Chat's persistMessagesLocked writes it as ONE KindMessage JSON line.
// So the guarantee is "what you see streamed is what ends up on disk" — the
// file just stores the joined result, not the pieces.
//
// Meta updates (turn_count, model switch) are appended as later meta records,
// so the LAST meta line is authoritative. Readers must take the last meta, not
// the first.
//
// # Durability
//
// Records are buffered (bufio) and become durable only when Recorder.Sync
// succeeds (flush + fsync). The engine syncs once per chat turn: a finished
// turn is always on disk and a crash loses at most the turn in flight. New
// session files also fsync their parent directory. The first persistence
// failure is sticky and surfaced through the recorder, and Load errors on any
// corrupt line (with the line number) instead of skipping it — failures are
// loud, never silent data loss.
//
// # Layers
//
//	SessionStore  — list/load/delete/open transcripts (session.go)
//	Recorder      — per-session multi-writer: sinks + buffered file append (recorder.go)
//	ProviderStore — read-only provider/model registry (models.go)
package store
