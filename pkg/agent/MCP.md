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
      "bearerToken": "your-token-here",
      "timeoutSeconds": 60
    },
    "oauth": {
      "type": "streamable-http",
      "url": "https://service.example/mcp",
      "oauth": {}
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
  environment; `env` adds or overrides variables for the child process. In the
  add/edit form it is one `NAME=value` entry per row. Write the
  value directly — `command` and `env` are the only places a stdio server's
  credentials can live, and the MCP subprocess runs as your OS user either way.
- `streamable-http`: `url` must be an HTTP(S) endpoint. Use HTTPS for remote
  credentials. `headers` supplies literal HTTP headers; `bearerToken` is the
  bearer token itself, written straight into this file. Do not combine it with an
  explicit `Authorization` header. Redirects are not followed.
- `timeoutSeconds`: connection/discovery and per-call deadline, default 60.
- `oauth`: opts a streamable-http server into OAuth 2.1. An empty block registers
  the client dynamically with PKCE; `clientId` (with an optional `clientSecret`)
  uses a preregistered client instead. `issuer`, `scopes` and `redirectUrl` are
  optional overrides. `accessToken` and `expiry` are written back after a
  successful authorization — do not edit them by hand.
- A missing file or an empty `mcpServers` map disables MCP. A malformed
  `tools.json` prevents the session from starting, but a server that cannot be
  connected, authorized or discovered is skipped: the session starts with the
  servers that worked, and each skipped server is recorded in the server log
  with a short reason for contributing no tools. Configuration and tool
  discovery are snapshots: changes take effect for newly created or resumed
  sessions, not already running ones.

Every credential in this file is a literal value — there is no environment-variable
reference, here or anywhere else in the MCP config. `tools.json` lives in the
harness config directory the **infaiw server process** reads, which is not
necessarily the terminal you run the client from; for a background service, edit
the file on the host that runs the server.

## Managing servers

The `infaiw mcp` commands drive `tools.json` through the agent server, so they
edit the file the server process reads:

- `infaiw mcp list` shows every server with its transport, authentication and
  token state.
- `infaiw mcp add` opens one form: it asks for the transport, then the name and
  endpoint, then whether the server uses OAuth, and — when it does not — which
  credential it takes. A stdio server's environment and a streamable-http
  server's headers are entered on their own screen, one `name=value` per row:
  `enter` on that row opens it, `enter` again edits a row, and `a` and `d` add
  and delete one. It stores the server, authorizes it when it
  uses OAuth, then probes it.
- `infaiw mcp edit [name]` opens the same form on an existing server. The name,
  the transport and the OAuth choice are fixed, because a different one is a
  different server; a credential left empty keeps its stored value.
- `infaiw mcp remove [name]` asks once and then deletes a server and its
  authorization.
- `infaiw mcp refresh [name]` authorizes an OAuth server again, in the browser.

The form covers what `infaiw mcp` normally needs. A preregistered OAuth client
(`clientId`, `clientSecret`, `issuer`, `scopes`) is set by editing `oauth` in
`tools.json` directly.

## Authorization

Authorization happens only in `infaiw mcp add` and `infaiw mcp refresh`. The
server runs the OAuth 2.1 authorization-code flow through the official SDK: it
discovers the authorization server, registers a client, opens a loopback callback
listener, and sends the URL to the client, which prints it and hands it to the
desktop's opener. The callback it listens on is printed beside the URL, because an
authorization server that refuses the request names that exact value. The resulting
access token and its expiry are stored in the server's own `oauth` block, next to
the endpoint they were minted for.

The callback is a port this process picks at every authorization (or the port in
`oauth.redirectUrl`, when one is pinned), and only the authorization itself can
reach it: it is not a route on the agent server's HTTP API. A client registration
therefore has to be made for the callback in hand, so:

- An entry that pins no `redirectUrl` registers a client dynamically for the
  callback it is about to use. A `clientId` stored without a `redirectUrl` is
  ignored (with a warning in the agent log), because no earlier registration can
  cover a port that did not exist then.
- An entry that pins both `redirectUrl` and `clientId` authorizes as that
  preregistered client, and the authorization server must already have that exact
  redirect URL registered.

`redirect_uri does not match any of the OAuth 2.0 Client's pre-registered redirect
urls` from an authorization server means the request went out as a preregistered
client whose redirect URL is not the printed callback: clear `clientId` (and
`clientSecret`) to register a fresh client, or set `redirectUrl` to the URL that
client has registered.

A session reads that block once, when it is created or resumed, and never
authorizes or renews anything itself. A token that is missing, expired or
insufficient makes the server unavailable for that session: the connection is
skipped, and a call that still fails is reported as an authorization error telling
the operator to run `infaiw mcp refresh` and reopen the session. Reauthorizing
does not reach into a running session, which keeps the credential it started with.

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

Protect the config directory with mode `0700` and `tools.json` with mode `0600`:
it holds literal credentials. File permissions do not isolate credentials from
approved shell commands or MCP subprocesses running as your OS user. Connection
errors do not include raw transport errors, URLs, headers, or credential values;
server-returned tool output is external data and may itself contain sensitive data.

Resources, prompts and elicitation are enabled, along with OAuth 2.1 for
streamable-http servers. Sampling and task extensions are not enabled by this
integration.
