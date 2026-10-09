# MCP tools

infai can connect to local stdio MCP servers and remote Streamable HTTP MCP
servers. Every interactive, single-loop, and sidecar session owns independent
connections. A local server process is reused across turns within that session;
other sessions launch their own processes.

## Configuration

Create `tools.json` under the harness config directory:

- Linux: `$XDG_CONFIG_HOME/infai/harness/tools.json`, normally
  `~/.config/infai/harness/tools.json`.
- Other platforms: `os.UserConfigDir()/infai/harness/tools.json`.

```json
{
  "mcpServers": {
    "local": {
      "type": "stdio",
      "command": "/absolute/path/to/mcp-server",
      "args": [],
      "env": {
        "API_TOKEN": "your-token-here"
      }
    },
    "remote": {
      "type": "streamable-http",
      "url": "https://service.example/mcp",
      "bearerTokenEnv": "INFAI_REMOTE_MCP_TOKEN",
      "timeoutSeconds": 60
    }
  }
}
```

This is infai's configuration format, not an MCP protocol message. Servers are
keyed by name; that key must contain only ASCII letters, digits, underscores or
hyphens, and it is what tool names and error messages use.

- `type`: `stdio` or `streamable-http`.
- `stdio`: `command` is executed directly, without a shell, in the session's
  workspace. `args` are separate arguments. The process inherits infai's
  environment; `env` adds or overrides variables for the child process. Write the
  value directly — `command` and `env` are the only places a stdio server's
  credentials can live, and the MCP subprocess runs as your OS user either way.
- `streamable-http`: `url` must be an HTTP(S) endpoint. Use HTTPS for remote
  credentials. `headers` supplies literal HTTP headers; `bearerTokenEnv`
  references the environment variable containing the bearer token. Do not
  combine it with an explicit `Authorization` header. Redirects are not followed.
- `timeoutSeconds`: connection/discovery and per-call deadline, default 60.
- A missing file or an empty `mcpServers` map disables MCP. Invalid
  configuration, missing referenced credentials, or connection/discovery failures
  prevent that session from starting. Configuration and tool discovery are
  snapshots: changes take effect for newly created or resumed sessions, not
  already running ones.

`bearerTokenEnv` is the only environment reference, and it must exist in the
**infaiw server process**, not just the client terminal. For a background
service, configure its environment and restart it before creating new sessions.

## Safety and behavior

Configured commands run during session startup for discovery. Only configure
trusted executables and endpoints; tool-call approval is not a subprocess sandbox.
Each discovered tool requires human approval, including calls made by sidecars.
Server-provided read-only annotations do not bypass approval. Tools receive names
of the form `mcp_<server>_<tool>`, passed through unchanged; a model provider
rejects names outside its `[A-Za-z0-9_-]{1,64}` pattern, so keep both names short
and plain.

Results support text content and JSON `structuredContent`. Server tool errors
remain errors with their text/JSON details. Image, audio and resource content
returns an explicit unsupported-content error; the remote action may already have
completed. Large tool outputs are truncated with an explicit marker.

Canceling a turn cancels its in-flight MCP call and prevents queued calls from
executing. Closing a session closes its connections and stdio processes. Cancellation
is best effort remotely: it does not undo side effects. Calls are not automatically
replayed after connection failures. A resumed infai session establishes fresh MCP
connections; timeline branch selection does not roll back remote state.

The official Go SDK negotiates the current `2026-07-28` stateless protocol and
older initialization/session-based revisions. SDK `ClientSession` ownership is
independent of protocol-level state. § [SDK lifecycle documentation](https://github.com/modelcontextprotocol/go-sdk/blob/v1.8.0/docs/protocol.md)

Protect the config directory with mode `0700` and `tools.json` with mode `0600`
if it contains literal secrets. Prefer environment references to plaintext values.
Neither file permissions nor environment references isolate credentials from
approved shell commands or MCP subprocesses running as your OS user. Connection
errors do not include raw transport errors, URLs, headers, or credential values;
server-returned tool output is external data and may itself contain sensitive data.

Resources, prompts, OAuth login/refresh UI, elicitation, sampling, and task
extensions are not enabled by this integration.
