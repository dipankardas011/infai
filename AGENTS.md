# infai — AI Agent Development Guide

> Zero-management launch templates for [llama.cpp](https://github.com/ggerganov/llama.cpp). A terminal-based TUI tool to discover local models, manage launch profiles, and run `llama-server` with real-time monitoring.

## Table of Contents

1. [Quick Start](#quick-start)
2. [Architecture Overview](#architecture-overview)
3. [Package Structure](#package-structure)
4. [TUI Screens & Navigation](#tui-screens--navigation)
5. [Data Models](#data-models)
6. [Database Layer](#database-layer)
7. [Key Workflows](#key-workflows)
8. [Metrics & Monitoring](#metrics--monitoring)
9. [Themes & Styling](#themes--styling)
10. [Build & Release](#build--release)
11. [Conventions & Guidelines](#conventions--guidelines)

---

## Quick Start

```bash
# Prerequisites
go 1.23+ and a C compiler (for SQLite via CGO)

# Build
CGO_ENABLED=1 go build -o infai .

# Run
./infai
```

**Configuration storage** (SQLite at `~/.config/infai/config.db`):
- Linux: `~/.config/infai/config.db`
- macOS: `~/Library/Application Support/infai/config.db`
- Windows: `%AppData%\infai\config.db`

---

## Architecture Overview

infai is a **single-binary TUI application** built on the [Bubble Tea](https://github.com/charmbracelet/bubbletea) framework. It follows a **root model → sub-model** composition pattern where each screen is an independent `tea.Model`.

```
main.go
  └── tui.AppModel (root bubbletea model)
        ├── HomeModel           — dashboard with recents, folders, executor
        ├── ModelListModel      — browsable, filterable model list
        ├── ProfileListModel    — list of launch profiles for a model
        ├── ProfileEditModel    — form editor for profile fields
        ├── ConfirmModel        — review & launch confirmation
        ├── ServerModel         — live server logs + metrics viewport
        ├── ExploreModel        — scan directory management
        ├── ExecutorModel       — llama-server binary configuration
        └── ThemeSelectorModel  — theme picker

        ┌─ db.DB (SQLite persistence)
        ├─ scanner.Scan() (disk scanning for GGUF/MLX/safetensors)
        └─ launcher.BuildArgs() (command construction)
```

### Core Design Decisions

- **No web server** — everything runs in a single terminal via Bubble Tea's event loop
- **SQLite with embedded migrations** — all config stored locally; migrations use `go:embed`
- **Cross-platform** — Goreleaser multi-arch builds (linux-amd64/arm64, darwin-amd64/arm64)
- **gopsutil integration** — real-time system/process metrics (CPU, RAM, GPU via nvidia-smi)

---

## Package Structure

| Package | Path | Purpose |
|---------|------|---------|
| `main` | `main.go` | Entry point, DB init, scan, TUI bootstrap |
| `config` | `config/version.go` | Version string (injected via ldflags) |
| `db` | `db/db.go` | SQLite persistence, migrations, CRUD |
| `launcher` | `launcher/launcher.go` | Args building & process execution |
| `model` | `model/types.go` | Domain types: `ModelEntry`, `Profile`, `GGUFMetadata` |
| `scanner` | `scanner/scanner.go` | Disk scanning for model files |
| `tui` | `tui/*.go` | All TUI screens, styles, themes, keys |
| `migrations` | `migrations/*.sql` | SQL migrations (embedded) |

### Key Files in `tui/`

| File | Responsibility |
|------|----------------|
| `app.go` | Root model, screen routing, key dispatch, view composition |
| `home.go` | Dashboard: recents list, folder display, executor status |
| `modellist.go` | Filterable model list with bubbles/list |
| `profilelist.go` | Profile list per model, "new profile" item |
| `profileedit.go` | Multi-field form editor with scroll, validation |
| `confirm.go` | Command preview before launch |
| `server.go` | Live log streaming, viewport, status display |
| `metrics.go` | System + process metrics via gopsutil + nvidia-smi |
| `tps.go` | TPS history, percentile computation, live `/metrics` polling |
| `explore.go` | Scan directory management with file browser |
| `executor.go` | Executor binary configuration |
| `theme_selector.go` | Theme selection screen |
| `styles.go` | Lipgloss style variables, theme-reactive rebuild |
| `themes.go` | Theme definitions, cycling, active theme |
| `keys.go` | All key bindings per screen (bubbles/key) |
| `confirm.go` | Launch confirmation with command wrapping |

---

## TUI Screens & Navigation

```
                    ┌──────────┐
                    │   Home   │  ← Entry point, shows recents
                    └────┬─────┘
                         │ enter (on recent) or
              ┌──────────┴──────────┐
              │                     │
       ┌──────▼──────┐        ┌────▼─────┐
       │   All Models│        │  Folders │
       │   (press a) │        │ (press f)│
       └──────┬──────┘        └──────────┘
              │ enter
       ┌──────▼──────┐
       │ Profile List│
       └──────┬──────┘
              │ enter
        ┌─────┴─────┐
        │           │
   ┌────▼───┐  ┌────▼────────┐
   │Confirm │  │ ProfileEdit │
   └────┬───┘  └─────────────┘
        │ enter
   ┌────▼────────┐
   │  Server Log │  ← Live llama-server output
   └─────────────┘
```

### Additional Screens

| Screen | Entry Key | Description |
|--------|-----------|-------------|
| **Executor** | `c` on Home | Configure `llama-server` binary paths |
| **Theme Selector** | `t` on Home | Pick from 5 built-in themes |

### Screen IDs (internal)

```go
screenHome = iota       // 0 — dashboard
screenModelList         // 1 — all models
screenProfileList       // 2 — profiles
screenProfileEdit       // 3 — profile form
screenConfirm           // 4 — launch confirmation
screenServerRunning     // 5 — server logs
screenExplore           // 6 — scan folders
screenExecutor          // 7 — executors
screenThemeSelector     // 8 — themes
```

---

## Data Models

### ModelEntry

```go
type ModelEntry struct {
    ID          int64       // DB primary key
    ScanDir     string      // Parent directory from scan_dirs
    DirName     string      // Directory/file stem name
    GGUFPath    string      // Full path to .gguf file
    MmprojPath  string      // Optional multimodal projector
    DisplayName string      // Human-readable name
    Type        string      // "gguf" | "gguf_multimodal" | "mlx" | "safetensors" | "mlx_quantized" | "hf_quantized"
    Metadata    string      // Additional metadata (reserved)
}
```

### Profile

```go
type Profile struct {
    ID              int64       // DB primary key
    ModelID         int64       // FK → models.id
    Name            string      // Profile name (e.g., "text-only", "vision")
    Port            int         // llama-server port (default: 8000)
    Host            string      // Bind address (default: "0.0.0.0")
    ContextSize     int         // -c value (internal: raw tokens; UI: K/M display)
    NGL             string      // GPU layers, "auto" or integer
    BatchSize       *int        // -b (optional)
    UBatchSize      *int        // -ub (optional)
    CacheTypeK      *string     // KV cache type (optional)
    CacheTypeV      *string     // KV cache type (optional)
    FlashAttn       bool        // --flash-attn
    Jinja           bool        // --jinja
    Temperature     *float64    // --temperature (optional)
    ReasoningBudget *int        // --reasoning-budget (optional)
    TopP            *float64    // --top_p (optional)
    TopK            *int        // --top_k (optional)
    NoKVOffload     bool        // --no-kv-offload
    UseMmproj       bool        // --mmproj
    ExtraFlags      string      // Raw extra arguments
}
```

### Executor

```go
type Executor struct {
    ID        string  // e.g., "llamacpp"
    Path      string  // Full binary path
    IsDefault bool    // Whether this is the default
}
```

---

## Database Layer

**SQLite database** with foreign keys enabled, managed via `golang-migrate/v4` with embedded SQL files.

### Tables

| Table | Purpose |
|-------|---------|
| `models` | Discovered model files (GGUF, MLX, safetensors) |
| `scan_dirs` | Directories to scan for models |
| `profiles` | Launch profiles, unique per `(model_id, name)` |
| `recents` | Last-used model+profile pairs |
| `executors` | llama-server binary paths |
| `settings` | Key-value pairs (theme, schema_version) |

### Migration History

| Version | Changes |
|---------|---------|
| `000001` | Initial schema: models, scan_dirs, profiles, recents, executors, settings |
| `000002` | Added `type` and `metadata` columns to `models` (for MLX/safetensors support) |

### DB CRUD Methods

The `db.DB` type provides:
- **Settings**: `GetSetting(key)`, `SetSetting(key, value)`
- **Scan dirs**: `ListScanDirs()`, `AddScanDir(path)`, `RemoveScanDir(path)`
- **Models**: `UpsertModel()`, `ListModels()`, `ListRecents(limit)`, `MarkModelUsed(id)`, `Sync(scanned)` (full sync with removal detection)
- **Profiles**: `ListProfiles(modelID)`, `UpsertProfile()`, `DeleteProfile(id)`, `MarkRecent(modelID, profileID)`
- **Executors**: `ListExecutors()`, `UpsertExecutor()`, `SetDefaultExecutor(id)`, `GetDefaultExecutorPath()`

### Key Pattern: Full Sync (`Sync`)

The `Sync()` method performs a **diff-and-replace** operation:
1. Iterates DB models; deletes those removed from disk
2. Iterates scanned models; upserts new/updated ones
3. Returns counts of removed and updated models

---

## Key Workflows

### 1. Application Startup

```go
// main.go flow:
1. Open SQLite database → auto-migrate
2. List configured scan directories
3. scanner.Scan(scanDirs) → discover all models on disk
4. Upsert each model into DB
5. Load theme from settings → apply
6. Find default executor (DB → PATH fallback to "llama-server")
7. Create AppModel with initial data
8. Run Bubble Tea program (alt screen, mouse cell motion)
9. Handle SIGTERM/SIGINT for clean shutdown
```

### 2. Model Discovery & Scanning

The scanner supports three model formats:

| Format | Detection |
|--------|-----------|
| **GGUF** | `.gguf` file with magic `0x46554747` ("GGUF"); multimodal if paired with `mmproj` file |
| **MLX** | `.npz` files + `config.json` present |
| **Safetensors** | `.safetensors` files + `config.json`; further classified as `mlx_quantized` or `hf_quantized` based on config |

**Validation**: GGUF files are validated by reading the first 4 bytes and checking the magic number.

### 3. Profile Launch

```go
// 1. User selects model → sees profile list
// 2. Select/create profile → goes to Confirm screen
// 3. ConfirmModel.BuildCommand() constructs shell-quoted command string
// 4. User presses Enter:
//    a. MarkRecent(modelID, profileID) → updates recents
//    b. NewServerModel(args) → spawns llama-server with piped stdout/stderr
//    c. Switch to ServerRunning screen
```

**Process Management**:
- Server runs as a child process with `Setpgid: true` (new process group)
- Stdout/stderr piped through `io.Pipe` → goroutine reads lines → `logLineMsg` channel
- Stop: `SIGTERM` → 5s grace → `SIGKILL` if unresponsive
- Restart: tear down and re-spawn with same args

### 4. Profile Form (ProfileEditModel)

19 form fields with 4 kinds:
- **fieldText** — text input (Name, Host, NGL, Extra Flags)
- **fieldInt** — integer input (Port, Context Size, Batch Sizes, Top K, Reasoning Budget)
- **fieldFloat** — float input (Temperature, Top P)
- **fieldBool** — checkbox toggled with Space (Flash Attn, Jinja, No KV Offload, Use Mmproj)
- **fieldSelect** — left/right cycle (Context Unit, Cache Type K/V)

Navigation: Tab/Shift-Tab or Arrow keys. Scrollable via `viewOffset`. Save via Ctrl+S.

### 5. Scan Directory Sync

```go
// User adds/removes folders → presses "s" for sync:
// 1. ExploreModel sends syncRequest to syncWorker goroutine
// 2. syncWorker: Scan() all folders → LoadModelMetadata() → DB.Sync()
// 3. Result sent back → syncDoneMsg → root model updates lists
```

---

## Metrics & Monitoring

### System Metrics (`metrics.go`)

Pulled every 2 seconds via `gopsutil/v4`:

| Source | Metrics |
|--------|---------|
| **CPU** | `cpu.Percent(200ms, false)` → total CPU% |
| **RAM** | `mem.VirtualMemory()` → used/total GiB, percentage |
| **GPU** | `nvidia-smi` → per-GPU util%, memory used/total |
| **Process** | `process.NewProcess(pid)` → CPU%, RSS GiB |
| **VRAM** | `nvidia-smi --query-compute-apps` → PID-specific VRAM |

Formatted as pipe-separated segments: `"cpu 45%  |  ram 8.2/32.0GiB 25%  |  nvidia-smi gpu0 60% 12.0/24.0GiB"`

### Live Throughput Metrics (`tps.go`)

Two parallel data sources:

1. **Log-parsed TPS** — Parses `llama-server` stdout for `"tokens per second"` in eval lines → stored in history (max 100) → p50/p95 computed
2. **HTTP /metrics** — Polls `http://{host}:{port}/metrics` every 2s → parses Prometheus-format metrics:
   - `llamacpp:predicted_tokens_seconds` → avg gen TPS
   - `llamacpp:prompt_tokens_seconds` → prefill TPS
   - `llamacpp:requests_processing` → active requests
   - `llamacpp:requests_deferred` → queued requests
   - `llamacpp:tokens_predicted_total` → lifetime gen tokens
   - `llamacpp:prompt_tokens_total` → lifetime prompt tokens

### Prompt Progress

Regex-parsed from `"prompt processing progress,.*progress = X.XXX"` → displayed as percentage bar in the gen line.

---

## Themes & Styling

### Theme Architecture

Themes are defined as a palette of 7 lipgloss colors + background:

```go
type Theme struct {
    Name      string   // identifier (e.g., "tokyonight")
    Primary   Color    // titles, selected items, accents
    Secondary Color    // badges, keys
    Success   Color    // positive indicators
    Error     Color    // errors, warnings
    Muted     Color    // descriptions, secondary text
    Text      Color    // body text
    Bg        Color    // background (for badge backgrounds)
}
```

### Built-in Themes

| Theme | Primary | Source Inspiration |
|-------|---------|-------------------|
| **tokyonight** | `#7DCFFF` | Tokyo Night (blue) |
| **everforest** | `#83C092` | Everforest (green) |
| **onedark** | `#61AFEF` | One Dark (blue) |
| **rosepine** | `#9CCFD8` | Rosé Pine (ice) |
| **gruvbox** | `#83A598` | Gruvbox (teal) |

### Styling System

- `styles.go` defines global `lipgloss.Style` variables (`styleTitle`, `styleMuted`, etc.)
- `rebuildStyles()` is called on every theme change, pulling colors from `ActiveTheme`
- `SetTheme(name)` updates `ActiveTheme`, finds index, calls `rebuildStyles()`
- `CycleTheme()` advances to next theme (wraps around)
- Theme persisted in DB as `settings.theme` key
- Theme applied at startup if set in DB

---

## Build & Release

### Local Build

```bash
CGO_ENABLED=1 go build -o infai .
# Version is "dev" unless injected via ldflags
CGO_ENABLED=1 go build -ldflags "-X 'github.com/dipankardas011/infai/config.version=1.2.3'" -o infai .
```

### Goreleaser Release (`.goreleaser.yaml`)

Multi-arch cross-compilation via Docker:

| Target | CC | CXX |
|--------|----|-----|
| linux-amd64 | x86_64-linux-gnu-gcc | x86_64-linux-gnu-g++ |
| linux-arm64 | aarch64-linux-gnu-gcc | aarch64-linux-gnu-g++ |
| darwin-amd64 | o64-clang | o64-clang++ |
| darwin-arm64 | oa64-clang | oa64-clang++ |

**Artifacts**: `infai_{version}_{os}_{arch}.tar.gz`
**Release trigger**: `git push --tags v*`
**CI**: GitHub Actions with `goreleaser/goreleaser-cross` image (Go 1.26.2)

### Distribution Channels

| Channel | Method |
|---------|--------|
| **Homebrew** | `brew install dipankardas011/tap/infai` (auto-generated by Goreleaser) |
| **Linux script** | `curl -sL install.sh \| bash` |
| **Binary** | Download from GitHub Releases |
| **Go install** | `go install github.com/dipankardas011/infai@latest` |

---

## Conventions & Guidelines

### Code Style

- **Package naming**: Short, lowercase, one word (e.g., `db`, `tui`, `scanner`)
- **Type composition over inheritance**: Each screen is a struct with `Init()`, `Update()`, `View()` methods
- **Message types**: Unexported internal messages (e.g., `toastTickMsg`, `scanDoneMsg`), exported inter-screen messages (e.g., `saveProfileMsg`, `syncDoneMsg`)
- **Error handling**: Errors propagated to UI via `a.setErr(msg)` toast pattern (auto-dismiss after 4 seconds)
- **Optional values**: Use pointers (`*int`, `*float64`, `*string`) for nullable profile fields

### Key Binding Conventions

- Defined in `keys.go` using `bubbles/key.NewBinding()`
- Arrow keys + vim-style (`j`/`k`) always paired
- Navigation: `tab`/`shift+tab` for forms, `up`/`down` for lists
- `?` toggles full help on all screens (except profile edit and executor)
- `esc` = back on most screens
- `q` = quit (on Home or Model List)

### Screen Design

- All screens centered in a `lipgloss.Place(... Center Center ...)` container
- Rounded border boxes with theme-colored borders
- Width capped at 60 chars (responsive: `min(60, width - 4)`)
- Help bar at bottom using `bubbles/help`
- Toast errors shown at top in red with ⚠ prefix

### Scanner Conventions

- Hidden files/dirs (starting with `.`) are skipped
- Multiple `.gguf` files per directory are supported
- Multimodal detection: any file containing "mmproj" in name
- Type detection for safetensors: inspect `config.json` for `quantization` vs `quantization_config` keys

### Database Conventions

- Boolean values stored as `INTEGER` (0/1), converted via `boolToInt()` helper
- Foreign keys enforced (`_foreign_keys=on` in DSN)
- ON CONFLICT upserts used for idempotent operations
- CASCADE deletes on profile/model relationships

### Testing Approach

The codebase currently has no test files. When adding tests:
- Unit test `db` CRUD operations (use `:memory:` SQLite)
- Unit test `launcher.BuildArgs()` for various profile configs
- Unit test `scanner.Scan()` with mock directories
- Table-driven tests for profile validation (`ToProfile()`)
