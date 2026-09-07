// Package store owns the on-disk persistence for the agent harness.
//
// # Provider registry
//
// ProviderStore keeps a provider-neutral registry in models.toml. A missing
// file represents an empty registry. Files are decoded strictly, and successful
// mutations validate the complete registry before atomically replacing the
// file. Registry files use mode 0600 and their directory uses mode 0700.
//
// Provider IDs and model slugs are TOML map keys. Catalog providers obtain
// their API protocol and base endpoint from providers.json. Only custom
// providers store a base_endpoint override. A model's optional name is its
// exact API ID; when omitted, its map key is used. There is no default.
//
//	[providers.local]
//	base_endpoint = "http://127.0.0.1:8080"
//
//	[providers.local.auth]
//	type = "bearer"
//	token = "secret"
//
//	[providers.local.models.qwen]
//	name = "qwen2.5-7b-instruct"
//	context_length = 32768
//	thinking_modes = ["off", "medium", "high"]
//	reasoning_effort = "medium"
//
// Authentication is persisted in TOML but omitted from Provider's JSON
// representation so provider API responses do not disclose credentials.
// Reload picks up validated hand edits without restarting the process.
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
//	SessionStore     - list/delete/open timelines (session.go)
//	SessionEventHub  - per-session live sink broadcaster (session_event_hub.go)
//	ProviderStore    - mutable provider/model registry (models.go)
package store
