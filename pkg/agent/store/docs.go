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
// # Session timelines
//
// Every session is one <uuid> directory under sessions/. Timeline chunks and
// indexes contain the durable events:
//
//	{"kind":"meta",...}                        session header and updates
//	{"kind":"message","message":{...}}         one full message
//	{"kind":"tool_call",...} / {"kind":"tool_result",...}
//
// # The stream vs the timeline
//
// When the model runs it streams. Deltas are live only and are never written
// to the timeline:
//
//	provider SSE chunks
//	   |  readStream() -> SessionEventHub.Publish(KindDelta, ...)
//	   |       |
//	   |       +-> sinks (SSE to the user / stdout) only
//	   |
//	   +-> content and reasoning builders
//
// readStream does both in one pass: it forwards each chunk to the live hook
// and accumulates the whole reply. When the stream ends it returns one
// consolidated message, which is persisted as one KindMessage timeline event.
//
// # Layers
//
//	SessionStore  — list/delete/open timelines (session.go)
//	SessionEventHub — per-session live sink broadcaster (session_event_hub.go)
//	ProviderStore — read-only provider/model registry (models.go)
package store
