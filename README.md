# infai

A terminal UI for managing [llama.cpp](https://github.com/ggerganov/llama.cpp) model profiles.  
Store named launch profiles in SQLite and run `llama-server` with live log streaming.

![themes: tokyonight · everforest · onedark · rosepine · gruvbox]()

## Features

- **Model Browser** — Auto-scans directories for `.gguf` and `mmproj` files.
- **Named Profiles** — Save multiple configurations (e.g., `text-only`, `low-vram`) per model.
- **Smart UI** — Easy pickers for quantization types and units (K, M) for context size.
- **Live Logs** — Built-in scrollable viewport for `llama-server` output.
- **Themes** — 11+ themes (Tokyonight, Gruvbox, Rose Pine, etc.) — press `t` to cycle.

## Install

Requires Go 1.23+ and a C compiler (for SQLite).

```bash
go install github.com/dipankardas011/infai@latest
```

## Key Bindings

| Screen | Keys | Action |
|--------|------|--------|
| **Home** | `a`, `f`, `c` | All models, manage folders, configure executor |
| **Model List** | `enter`, `/`, `r` | Select, Filter, Rescan |
| **Profile List** | `enter`, `e`, `d` | Launch, Edit, Delete |
| **Editor** | `tab`, `space`, `ctrl+s`| Navigate, Toggle, Save |
| **Logs** | `s`, `esc`, `↑↓` | Stop, Back, Scroll |

## Configuration

Settings and profiles are stored in a local SQLite database:
- **Linux**: `~/.config/infai/config.db`
- **macOS**: `~/Library/Application Support/infai/config.db`
- **Windows**: `%AppData%\infai\config.db`

## License
MIT
