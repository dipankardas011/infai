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

**Minimum terminal size**: 60×20 characters. A warning is displayed if the window is too small.

---

## Architecture Overview

infai is a **single-binary TUI application** built on the [Bubble Tea](https://github.com/charmbracelet/bubbletea) framework. It follows a **root model → sub-model** composition pattern where each screen is an independent `tea.Model`.

```
main.go
  ├── backend.Service          — UI-free app/use-case layer
  │     ├── profile CRUD + recents
  │     ├── scan folder sync (scanner + db)
  │     ├── executor settings
  │     └── launch arg construction
  ├── runner.ServerProcess     — UI-free llama-server process execution
  └── tui.AppModel (root bubbletea model)
        ├── HomeModel           — tabbed dashboard (3 tabs)
        │     ├── ProfilesTabModel    — profile list + config preview split panel
        │     ├── ModelsTabModel      — scan directory presentation
        │     └── EnginesTabModel     — inference engine presentation
        ├── ModelListModel      — model picker (for new profiles)
        ├── ProfileEditModel    — form editor for profile fields
        ├── ServerModel         — live server logs + metrics viewport
        └── ThemeSelectorModel  — theme picker

        ┌─ db.DB (SQLite persistence)
        ├─ scanner.Scan() (disk scanning for GGUF/MLX/safetensors)
        ├─ launcher.BuildArgs() (command construction)
        └─ runner.StartServer() (process execution)
```

### Core Design Decisions

- **Layered architecture** — `tui` is presentation, `backend` owns use-cases/errors, `runner` owns process execution
- **No web server** — everything runs in a single terminal via Bubble Tea's event loop
- **SQLite with embedded migrations** — all config stored locally; migrations use `go:embed`
- **Cross-platform** — Goreleaser multi-arch builds (linux-amd64/arm64, darwin-amd64/arm64)
- **gopsutil integration** — real-time system/process metrics (CPU, RAM, GPU via nvidia-smi)
- **Tabbed home screen** — three tabs (Profiles, Models, Inference Engines) for intuitive navigation
- **Split-panel profiles** — left panel uses `bubbles/list`, right panel uses `bubbles/viewport`
- **Direct launch** — Enter on a profile launches the server immediately (no confirm screen)
- **Per-tab keymaps** — Help bar changes to show relevant keys for each tab
- **Clipboard integration** — copy server logs via `wl-copy`, `xclip`, or `pbcopy`

---

## Package Structure

| Package | Path | Purpose |
|---------|------|---------|
| `main` | `main.go` | Entry point, DB init, scan, TUI bootstrap |
| `config` | `config/version.go` | Version string (injected via ldflags) |
| `backend` | `backend/service.go` | UI-free application/use-case layer over DB, scanner, launcher |
| `db` | `db/db.go` | SQLite persistence, migrations, CRUD |
| `launcher` | `launcher/launcher.go` | Command/args construction |
| `runner` | `runner/server.go` | UI-free llama-server process execution and log channels |
| `model` | `model/types.go` | Domain types: `ModelEntry`, `Profile`, `GGUFMetadata` |
| `scanner` | `scanner/scanner.go` | Disk scanning for model files |
| `tui` | `tui/*.go` | All TUI screens, styles, themes, keys, scrollbars |
| `migrations` | `migrations/*.sql` | SQL migrations (embedded) |

### Key Files in `tui/`

| File | Responsibility |
|------|----------------|
| `app.go` | Root model, screen routing, key dispatch, message handling; calls `backend.Service` for use-cases |
| `home.go` | Tabbed dashboard container (3 tabs), tab switching, view composition |
| `profiles_tab.go` | Profiles tab: split panel (left=list, right=config preview), scrollable both sides, launch/edit/delete/new |
| `models_tab.go` | Models tab presentation: scan directory list, add/remove/sync via `backend.Service` |
| `engines_tab.go` | Engines tab presentation: executor config via `backend.Service` |
| `modellist.go` | Filterable model picker with bubbles/list (used for new profile) |
| `profileedit.go` | Multi-field form editor with scroll, validation (19 fields) |
| `confirm.go` | Command preview before launch |
| `server.go` | Server presentation: live logs, metrics viewport, stop/restart UI; process work delegated to `runner` |
| `metrics.go` | System + process metrics via gopsutil + nvidia-smi |
| `tps.go` | TPS history, percentile computation, live `/metrics` polling |
| `scrollbar.go` | Custom vertical/horizontal scrollbar rendering + clipboard utility |
| `header.go` | Header rendering ("infai" + version), tab bar, min-size warning |
| `theme_selector.go` | Theme selection screen |
| `styles.go` | Lipgloss style variables, theme-reactive rebuild |
| `themes.go` | Theme definitions, cycling, active theme |
| `keys.go` | All key bindings per screen (bubbles/key) |

---

## TUI Screens & Navigation

### Home Screen (Tabbed)

The home screen uses a **3-tab layout** with a header bar, tab navigation, and context-sensitive content:

```
┌──────────────────────────────────────────────────────────┐
│ infai v1.0.0                              t:theme  ?:help │  ← header
│ [ Profiles ]  Models    Engines                           │  ← tab bar
├──────────────────────────────────────────────────────────┤
│ ┌──────────────────┐ ┌──────────────────────────────────┐│
│ │ + New Profile    │ │ Profile Config          ░│       ││
│ │ ▶ my-profile     │ │ ░│                                 ││
│ │   llama-3b       │ │ Model: llama-3b                   ││
│ │   recent-q4      │ │ ▓│                                  ││
│ │ ──────────       │ │ Host: 0.0.0.0       ░│           ││
│ │   vision-profile │ │ Port: 8000                        ││
│ │   text-only      │ │ ░│                                  ││
│ │                  │ │ enter:launch  e:edit  x:delete    ││
│ └──────────────────┘ └──────────────────────────────────┘│
└──────────────────────────────────────────────────────────┘
←/s-tab:prev tab  →/tab:next tab  t:theme  q:quit  ?:help
```

### Screen Flow

```
                    ┌──────────┐
                    │   Home   │  ← Tabbed dashboard (entry point)
                    │  (3 tabs)│
                    └────┬─────┘
                         │
            ┌────────────┼────────────┐
            │            │            │
     ┌──────▼──────┐ ┌──▼────┐ ┌─────▼──────┐
     │  Profiles   │ │Models │ │Inference   │
     │   Tab       │ │ Tab   │ │Engines Tab │
     └──────┬──────┘ └───────┘ └────────────┘
            │
    ┌───────┼───────────┐
    │       │           │
    │  enter│(selected) │  n (new profile)
    │       │           │
┌───▼──┐ ┌──▼───┐  ┌────▼─────┐
│Conf. │ │Edit  │  │Model     │
│irm   │ │Prof. │  │Picker    │
└──┬───┘ └──────┘  └────┬─────┘
   │ enter               │ enter
┌──▼────────┐       ┌────▼─────┐
│  Server   │       │  Edit    │
│  Logs     │       │  Profile │
└───────────┘       └──────────┘
```

### Tabs

| Tab | Shortcut | Content |
|-----|----------|---------|
| **Profiles** | `1` | Top 3 recent profiles + separator + all profiles (left panel). Configuration preview (right panel). Scrollbars on both. |
| **Models** | `2` | List of scan directories. Add folder (opens file browser), sync, delete. Model count shown. |
| **Engines** | `3` | Current executor path. Auto-detect, add custom, set default. |

### Profile Tab Key Bindings

| Key | Action |
|-----|--------|
| `↑/↓` or `j/k` | Navigate profile list (bubbles/list with built-in scrollbar) |
| `enter` | Launch selected profile **directly** (no confirm screen) |
| `n` | Create new profile (opens Model Picker → Profile Edit) |
| `e` | Edit selected profile |
| `x` | Delete selected profile (with confirmation dialog) |
| `/` | Filter profiles (bubbles/list built-in filtering) |

### Server Screen Key Bindings

| Key | Action |
|-----|--------|
| `s` | Stop server (SIGTERM) |
| `r` | Restart stopped server |
| `c` | Clear logs |
| `y` | Copy all logs to clipboard |
| `esc` | Stop & return home (if running) or just return (if stopped) |
| `↑/↓/pgup/pgdn` | Scroll logs vertically |
| `←/→` | Scroll logs horizontally (with scrollbar indicator) |

### Additional Screens

| Screen | Entry | Description |
|--------|-------|-------------|
| **Model Picker** | `n` on Profiles tab | Browsable, filterable model list for creating profiles |
| **Profile Edit** | `e` on profile or via new profile flow | 19-field form editor with Tab navigation, Ctrl+S to save |
| **Server Logs** | `enter` on profile (auto-launch) | Live logs with bubbles/viewport scrollbars, metrics, clipboard copy |
| **Theme Selector** | `t` (global) | Pick from 5 built-in themes |
| **File Browser** | `a` in Models tab or Engines tab | Directory picker with up/down/enter/back navigation and `/` filter |

### Screen IDs (internal)

```go
screenHome = iota       // 0 — tabbed dashboard
screenModelList         // 1 — model picker
screenProfileEdit       // 2 — profile form
screenConfirm           // 3 — launch confirmation
screenServerRunning     // 4 — server logs
screenThemeSelector     // 5 — themes
```

### Tab IDs (internal)

```go
tabProfiles = 0         // Profile management
tabModels   = 1         // Scan directory management
tabEngines  = 2         // Executor configuration
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
- **Profiles**: `ListProfiles(modelID)`, `ListAllProfiles()`, `UpsertProfile()`, `GetProfile(id)`, `DeleteProfile(id)`, `MarkRecent(modelID, profileID)`
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
7. Load recent profiles (limit 3) + all profiles from DB
8. Create AppModel with HomeModel (tabbed, initialized with data)
9. Run Bubble Tea program (alt screen, mouse cell motion)
10. Handle SIGTERM/SIGINT for clean shutdown
```

### 2. Model Discovery & Scanning

The scanner supports three model formats:

| Format | Detection |
|--------|-----------|
| **GGUF** | `.gguf` file with magic `0x46554747` ("GGUF"); multimodal if paired with `mmproj` file |
| **MLX** | `.npz` files + `config.json` present |
| **Safetensors** | `.safetensors` files + `config.json`; further classified as `mlx_quantized` or `hf_quantized` based on config |

**Validation**: GGUF files are validated by reading the first 4 bytes and checking the magic number.

### 3. Profile Launch (from Profiles Tab)

```go
// 1. User navigates Profiles tab, selects a profile
// 2. Right panel shows full configuration preview
// 3. User presses Enter:
//    a. profilesTabLaunchMsg sent → AppModel
//    b. launcher.BuildArgs(executor, model, profile) → CLI args
//    c. MarkRecent(modelID, profileID) → updates recents
//    d. NewServerModel(args) → spawns llama-server with piped stdout/stderr
//    e. Switch to ServerRunning screen
// No confirm screen — one-press launch for speed.
```

**Process Management**:
- Server runs as a child process with `Setpgid: true` (new process group)
- Stdout/stderr piped through `io.Pipe` → goroutine reads lines → `logLineMsg` channel
- Stop: `SIGTERM` → 5s grace → `SIGKILL` if unresponsive
- Restart: tear down and re-spawn with same args
- Logs: max 10,000 lines retained; clear with `c`; copy to clipboard with `y`

### 4. Profile Creation (from Profiles Tab)

```go
// 1. User presses 'n' on Profiles tab
//    → profilesTabNewProfileMsg
//    → openProfileModelPicker → switches to ModelList screen
// 2. User selects a model → switches to ProfileEdit screen
// 3. User fills form (19 fields), presses Ctrl+S
//    → saveProfileMsg → UpsertProfile → back to Home (Profiles tab)
```

### 5. Profile Deletion (from Profiles Tab)

```go
// 1. User selects profile → right panel shows config
// 2. User presses 'x' → delete confirmation shown in right panel
// 3. 'y' confirms → profilesTabDeleteProfileMsg → DeleteProfile → refresh home
// 4. 'n' or esc cancels
```

### 6. Models Tab — Scan Directory Management

```go
// User manages model directories:
// - 'a' → opens file browser → select folder → AddScanDir
// - 'd' → RemoveScanDir (on selected folder)
// - 's' → sync all folders:
//   1. scanner.Scan(folders)
//   2. LoadModelMetadata for each entry
//   3. db.Sync(scanned) → upsert new, remove stale
//   4. Refresh home with new model count
```

### 7. Profile Form (ProfileEditModel)

19 form fields with 4 kinds:
- **fieldText** — text input (Name, Host, NGL, Extra Flags)
- **fieldInt** — integer input (Port, Context Size, Batch Sizes, Top K, Reasoning Budget)
- **fieldFloat** — float input (Temperature, Top P)
- **fieldBool** — checkbox toggled with Space (Flash Attn, Jinja, No KV Offload, Use Mmproj)
- **fieldSelect** — left/right cycle (Context Unit, Cache Type K/V)

Navigation: Tab/Shift-Tab or Arrow keys. Scrollable via `viewOffset`. Save via Ctrl+S.

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

### Scrollbar Rendering

Uses standard Bubble Tea components for scrolling:
- **Profiles tab left panel**: `bubbles/list` with built-in vertical scrollbar
- **Profiles tab right panel**: `bubbles/viewport` with built-in vertical scrollbar
- **Server logs**: `bubbles/viewport` with built-in vertical scrollbar + custom horizontal scrollbar via `scrollbar.go`
- **Models tab**: Custom list with `scrollbar.go` rendering for vertical scrollbar
- All scrollbars theme-reactive via `rebuildStyles()`

### Clipboard Integration

Server logs can be copied to the system clipboard via `y` key. Uses:
- `wl-copy` on Wayland
- `xclip -selection clipboard` on X11
- `pbcopy` on macOS
- Falls back gracefully if no clipboard tool is available

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
- **Message types**: Unexported internal messages (e.g., `toastTickMsg`, `profilesTabLaunchMsg`), exported inter-screen messages (e.g., `saveProfileMsg`)
- **Error handling**: Errors propagated to UI via `a.setErr(msg)` toast pattern (auto-dismiss after 4 seconds)
- **Optional values**: Use pointers (`*int`, `*float64`, `*string`) for nullable profile fields

### Key Binding Conventions

- Defined in `keys.go` using `bubbles/key.NewBinding()`
- **Per-tab keymaps**: Help bar changes to show tab-specific keys (Profiles/Models/Engines each have their own keymap)
- Arrow keys + vim-style (`j`/`k`) always paired
- Navigation: `tab`/`shift+tab` for forms, `up`/`down` for lists, `←/→` for tabs
- Number keys `1`/`2`/`3` for direct tab access
- `?` toggles full help on all screens (except profile edit)
- `esc` = back on most screens
- `q` = quit (on Home)
- `t` = theme selector (global, except in editor/server)

### Screen Design

- **Home screen**: Header bar ("infai" + version) → Tab bar → Content area
- Tab bar shows active tab with inverted colors (primary bg, bg fg)
- Inactive tabs shown in muted color
- All screens use rounded border boxes with theme-colored borders
- Responsive width: minimum 60 chars, adapts to terminal width
- Help bar at bottom using `bubbles/help`
- Toast errors shown at top in red with ⚠ prefix

### Tab Design

- **Profiles tab**: Horizontal split (40% left / 60% right)
  - Left panel: scrollable profile list with custom vertical scrollbar
  - Right panel: scrollable configuration preview with vertical scrollbar
  - "New Profile" always at top of list
  - Top 3 recents grouped separately with separator line
- **Models tab**: Single panel with directory list
  - Built-in file browser for adding directories
  - Sync with spinner indicator
- **Engines tab**: Single panel with executor info
  - Shows current path, detected binary, saved executors

### Scrollbar Conventions

- **Vertical scrollbar**: Rendered alongside content as a single-character column
  - `░` for track, `▓` for thumb
  - Thumb position and size proportional to viewport/content ratio
- **Horizontal scrollbar**: Rendered below log viewport as a single line
  - `─` for track, `━` for thumb
  - Controlled with `←`/`→` keys (5-char increments)
- Scrollbar colors update with theme changes via `rebuildStyles()`

### Responsive Design

- **Minimum window size**: 60 cols × 20 rows
  - Warning screen shown if terminal is below minimum
- **Content width**: Adapts to terminal width, capped per-container
- **Split panel**: Always horizontal, left panel ~40%, right ~60%
- **Long paths**: Truncated with `…` prefix when exceeding available width
- **Small terminals**: Panels shrink proportionally; content may overflow with scrollbars

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
- Unit test `tui.RenderScrollbar()` for edge cases (empty, single item, many items)
- Table-driven tests for profile validation (`ToProfile()`)
