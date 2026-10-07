# infai

```
██╗███╗   ██╗███████╗ █████╗ ██╗
██║████╗  ██║██╔════╝██╔══██╗██║
██║██╔██╗ ██║█████╗  ███████║██║
██║██║╚██╗██║██╔══╝  ██╔══██║██║
██║██║ ╚████║██║     ██║  ██║██║
╚═╝╚═╝  ╚═══╝╚═╝     ╚═╝  ╚═╝╚═╝
```

![](./cover.webp)

**A terminal UI for managing and launching local inference servers.**

Configure launch profiles for [llama.cpp](https://github.com/ggerganov/llama.cpp) and [vLLM](https://github.com/vllm-project/vllm), scan model directories for GGUF and SafeTensors files, download models from HuggingFace, and monitor running servers — all from a single TUI.

---

## Installation

### Homebrew (macOS / Linux)

```bash
brew install dipankardas011/tap/infai
```

### Script (Linux)

```bash
curl -sL https://raw.githubusercontent.com/dipankardas011/infai/main/install.sh | bash
```

### Binary

Download a pre-built binary from the [Releases](https://github.com/dipankardas011/infai/releases) page.

Builds are available for Linux (amd64, arm64) and macOS (amd64, arm64).

### Linux packages

Install `infai` from the
[openSUSE Software portal](https://software.opensuse.org/download.html?project=home%3Adipankardas%3Ainfai&package=infai).
Repository setup instructions are available for Debian, Ubuntu, Fedora, and
openSUSE.

### infaiw background server

`infaiw` is released separately from `infai`. Its services run `infaiw server`
as your user and are not started automatically on installation. The default
address is `localhost:6000`.

#### Homebrew

```bash
brew install --formula dipankardas011/tap/infaiw
brew services start dipankardas011/tap/infaiw
```

If you previously installed the cask, migrate once:

```bash
brew uninstall --cask infaiw
brew update
brew install --formula dipankardas011/tap/infaiw
brew services start dipankardas011/tap/infaiw
```

The tap remains `dipankardas011/homebrew-tap`; releases now publish
`Formula/infaiw.rb` instead of `Casks/infaiw.rb`. The executable moves from the
Caskroom to the Cellar, with the usual `brew --prefix` bin symlink. Existing
user data is not moved or removed by the release workflow. Do not use `sudo`
for the user service. Logs are in `$(brew --prefix)/var/log/infaiw.log`.

Use `brew services stop infaiw` or `brew services restart infaiw` to manage it.
Homebrew manages launchd on macOS and systemd on Linux.

#### Linux packages (OBS)

Install `infaiw` from the
[openSUSE Software portal](https://software.opensuse.org/download.html?project=home%3Adipankardas%3Ainfai&package=infaiw).

The service is **opt-in**: installing the package does not start the server
or enable automatic startup. Run the following commands as your normal user,
without `sudo`:

```bash
systemctl --user daemon-reload
systemctl --user enable --now infaiw.service
journalctl --user -u infaiw.service -f
```

- `--user` runs the service as your account, not root, with access to your
  user files and credentials.
- `enable` enables automatic startup on future logins.
- `--now` also starts `infaiw server` immediately.
- `journalctl` shows the service logs; press `Ctrl+C` to stop following logs
  without stopping the server.

Without lingering, the user service normally stops after your last login
session ends. To keep it running after logout and start it at boot, enable
lingering for your account (this may require administrator privileges):

```bash
sudo loginctl enable-linger "$USER"
```

For optional server configuration, create `~/.config/infaiw/server.env` with
`INFAI_AGENT_*` assignments, for example `INFAI_AGENT_PORT=6000`. Protect it
with `chmod 600` if it contains secrets. Apply changes with
`systemctl --user restart infaiw.service`. Shell environment variables are
not automatically inherited by background services.

To stop the server and disable automatic startup on future logins:

```bash
systemctl --user disable --now infaiw.service
```

Disabling the service does not delete your user data or configuration.

Packaging follows the [Homebrew service DSL](https://docs.brew.sh/Formula-Cookbook#service-files),
[openSUSE unit location guidelines](https://en.opensuse.org/openSUSE:Systemd_packaging_guidelines),
and Debian's [dh_installsystemduser](https://manpages.debian.org/bookworm/debhelper/dh_installsystemduser.1.en.html)
with `--no-enable` for opt-in activation. § These are the packaging references.

### From source

Requires Go 1.23+ and a C compiler (CGO is needed for SQLite).

```bash
go install github.com/dipankardas011/infai@latest
```

## Usage

```bash
infai            # launch the TUI
infai --version  # print version
```

On first launch, infai creates a local SQLite database to store scan directories, engine paths, and launch profiles. Add at least one model directory and one inference engine to get started.

## Features

- **Multi-engine support** — llama.cpp and vLLM. Configure binary paths, add arguments, and manage multiple engine installations.
- **Model scanning** — Recursively scans configured directories for GGUF and SafeTensors model files. Detects multimodal projectors and links them automatically.
- **HuggingFace downloads** — Search and download models directly from HuggingFace Hub. Supports GGUF variant selection for repos with multiple quantizations. Downloads are resumable and atomic.
- **Launch profiles** — Save named configurations (context size, batch size, GPU layers, port, extra flags) per model. Launch, edit, or delete profiles from the TUI.
- **Live server monitoring** — View real-time inference logs in a scrollable viewport. Tracks tokens-per-second and request metrics from the running server's `/metrics` endpoint.
- **Themes** — 11 built-in themes: tokyonight, everforest, onedark, rosepine, gruvbox, catppuccin, nord, dracula, kanagawa, solarized, monokai.

## Key bindings

| Screen | Keys | Action |
|---|---|---|
| Home | `a` `f` `c` | All models / Manage folders / Configure engines |
| Model list | `Enter` `/` `r` | Select / Filter / Rescan |
| Profile list | `Enter` `e` `d` | Launch / Edit / Delete |
| Editor | `Tab` `Space` `Ctrl+S` | Navigate / Toggle / Save |
| Logs | `s` `Esc` `Up/Down` | Stop server / Back / Scroll |

## Data storage

All settings and profiles are stored in a local SQLite database:

| OS | Path |
|---|---|
| Linux | `~/.config/infai/config.db` |
| macOS | `~/Library/Application Support/infai/config.db` |
| Windows | `%AppData%\infai\config.db` |

Schema migrations run automatically on startup.

## Contributing

Bug reports and pull requests are welcome on [GitHub](https://github.com/dipankardas011/infai).

```bash
# clone and build
git clone https://github.com/dipankardas011/infai.git
cd infai
go build -o infai ./cmd/inference

# run tests
go test ./...
```

## License

[Apache 2.0](LICENSE)
