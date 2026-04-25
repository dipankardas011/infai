# infai

```text
██╗███╗   ██╗███████╗ █████╗ ██╗
██║████╗  ██║██╔════╝██╔══██╗██║
██║██╔██╗ ██║█████╗  ███████║██║
██║██║╚██╗██║██╔══╝  ██╔══██║██║
██║██║ ╚████║██║     ██║  ██║██║
╚═╝╚═╝  ╚═══╝╚═╝     ╚═╝  ╚═╝╚═╝
```

**Zero-management launch templates for [llama.cpp](https://github.com/ggerganov/llama.cpp).**  
Simplify configuration management and run local models faster. Perfect for rapid experimentation and testing without the need to remember complex shell commands.

![profile config](profile-config.webp)
![running model](runningmodel.webp)

## Features

- **One-Click Launch** — Instant start with pre-configured profiles. No more memorizing long CLI commands.
- **Faster Experiments** — Quickly swap between different quantization types, context sizes, and batch settings to test model performance.
- **Zero Template Management** — Stop messing with shell aliases and scripts; manage everything through a clean, centralized TUI.
- **Live Inference Logs** — Monitor your server in real-time with a built-in scrollable viewport.
- **Smart UI** — Intuitive pickers for quantization types and context size units (K, M).
- **Centralized Path Control** — Manage all your model directories and your `llama-server` binary path in one place.
- **Themes** — 11+ themes (Tokyonight, Gruvbox, Rose Pine, etc.) to match your terminal setup.

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
