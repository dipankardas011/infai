# infai — Agent Development Guide

Terminal UI for managing and launching local inference servers. Supports llama.cpp and vLLM as inference backends.

## Build

```bash
CGO_ENABLED=1 go build -o infai .
go test ./...
```

CGO is required — the project uses `mattn/go-sqlite3`, which is a C library. Builds will fail without a C compiler and `CGO_ENABLED=1`.

Version is injected via ldflags (`-X github.com/dipankardas011/infai/config.version=...`). Without it, the binary reports `dev`.

## Layered Architecture

The codebase enforces a strict dependency direction. Violating it causes import cycles.

```
tui  →  backend  →  db, scanner, launcher, hub, downloader, inference
                 →  runner (process execution)
                 →  model (shared domain types, no dependencies)
```

- **`model`** — pure data types, no imports from other project packages. Everything depends on it; it depends on nothing.
- **`backend`** — use-case orchestration. The TUI calls backend methods; backend calls into db, scanner, etc. Backend never imports tui.
- **`tui`** — presentation only. Receives a `*db.DB` and calls backend/runner directly. Never imported by any other package.
- **`runner`** — owns the child process lifecycle. Used by tui to start/stop servers. Does not know about profiles or models — it receives a `LaunchSpec` (binary path + args + env).
- **`inference`** — engine-specific adapters. Translates a (engine, model, profile) triple into a `runner.LaunchSpec`. Also owns metrics parsing per engine kind.

## Bubble Tea Patterns

Every screen is a struct implementing `Init()`, `Update()`, `View()`. The root model (`tui.AppModel`) owns a `screenKind` enum and dispatches to the active screen's `Update`/`View`.

**Message conventions:**
- Unexported messages (`profilesTabLaunchMsg`, `toastTickMsg`) stay within a single screen.
- Exported messages (`saveProfileMsg`, `syncDoneMsg`) cross screen boundaries through AppModel.
- Cmd-returning messages follow Bubble Tea conventions — side effects happen in `Update`, never in `View`.

**Adding a new screen:**
1. Add a constant to the `screenKind` enum in `tui/app.go`.
2. Create the screen struct in its own file with `Init`, `Update`, `View`.
3. Add routing in `AppModel.Update` and `AppModel.View`.
4. Add key bindings in `tui/keys.go`.

**Adding a new tab to the home screen:**
1. Add a constant to the tab enum in `tui/home.go`.
2. Add the tab name to `tabNames`.
3. Create the tab model in its own file.
4. Wire it into `HomeModel`.

## Database Invariants

- Foreign keys are enforced (`_foreign_keys=on` in the DSN). Deleting a model cascades to its profiles and recents.
- Booleans are stored as `INTEGER` 0/1. Use `boolToInt()` in db.go for conversions.
- Migrations use `golang-migrate/v4` with SQL files embedded via `go:embed` in `migrations/embed.go`. They run automatically on `db.Open()`.
- The `patches/` directory contains Go-based data migrations for transformations too complex for SQL (e.g., rewriting model types across tables). Patches are keyed by migration version and run after the corresponding SQL migration.
- `config.MinSchemaVersion` in `config/version.go` marks the oldest patch that still needs to run. Bump it when removing old patch files.
- `Sync` and `SyncPerRoot` do diff-and-replace: they delete DB models no longer on disk and upsert new ones. This means a scan directory rename looks like a delete + re-add.

## Inference Engine Adapter Pattern

New inference backends are added by:
1. Adding a `EngineKind` constant in `model/types.go`.
2. Implementing the adapter interface in `inference/adapter.go` — `BuildLaunchSpec` (profile → CLI args) and `NewMetricsSource` (parse the engine's `/metrics` output).
3. Registering it in the adapter lookup in `inference/adapter.go`.

Each engine has its own Prometheus metric prefix (`llamacpp:`, `vllm:`). The metrics parser in `inference/metrics.go` is generic — adapters just provide the prefix and field mapping.

## Scanner Rules

- Only `.gguf` and `.safetensors` are scanned. GGUF files are validated by reading magic bytes (`0x46554747`).
- Hidden files and directories (`.` prefix) are always skipped.
- Multimodal detection: any sibling file containing `mmproj` in its name is linked as a projector.
- Multiple GGUF files in the same directory are treated as separate models.
- SafeTensors models require a `config.json` in the same directory.

## Process Management

`runner.ServerProcess` spawns the inference server as a child process:
- `Setpgid: true` so the server gets its own process group (clean kill on shutdown).
- Stdout and stderr are piped through goroutines that emit line-by-line messages to the TUI.
- Stop sequence: SIGTERM → 5-second grace period → SIGKILL.
- Max 10,000 log lines retained in memory per server instance.

## HuggingFace Integration

- `hub/` is the API client. It talks to `huggingface.co` directly (search, file listing, download URL resolution).
- `downloader/` handles the actual file transfer. Downloads are resumable (HTTP Range headers) and atomic (write to `.part` temp file, rename on completion).
- For repos with multiple GGUF quantizations, `downloader/plan.go` presents variant selection to the caller.
- Downloaded models record their HuggingFace provenance (`SourceRepo`, `SourceRevision`, `SourceFiles`) in the DB so re-syncs don't lose the origin.

## Gotchas

- **Cross-compilation**: Goreleaser uses `goreleaser-cross` Docker image for CGO cross-compilation. The CC/CXX pairs in `.goreleaser.yaml` must match the target OS/arch exactly. Adding a new target means finding the right cross-compiler toolchain.
- **Clipboard**: `tui/scrollbar.go` contains clipboard copy logic (not just scrollbar rendering, despite the filename). It tries `wl-copy`, `xclip`, `pbcopy` in order.
- **Port checking**: `tui/ports.go` checks if a port is available before launch. This is a TOCTOU race by nature but catches the common case.
- **Theme persistence**: theme name is stored as a `settings` row. If a theme is removed from `themes.go`, the stored name silently falls back to the default.
- **Profile optional fields**: pointer types (`*int`, `*float64`, `*string`) mean "not set" (omitted from CLI args). Zero values and nil are different — a `*int` of `0` means "pass 0", nil means "don't pass the flag".
- **Toast errors**: `a.setErr()` shows a red banner that auto-dismisses after 4 seconds via a `time.After` tick. Multiple errors overwrite each other (no queue).

## Testing Patterns

- DB tests use `:memory:` SQLite — fast, no cleanup needed.
- Scanner tests create temp directories with fixture files.
- Launcher tests verify CLI arg construction with table-driven cases.
- TUI tests use `tea.ProgramOpt` to drive the model without a real terminal.
- `go test ./...` runs everything. No external services or network calls in tests (hub/downloader tests use httptest servers).
