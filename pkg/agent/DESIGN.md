# infai Agent Engine — Design Decisions & Next Steps

Status: active (primitive loop working; tool/subagent layer is next)
Scope: `pkg/agent/*`

This document records the decisions we reached together and the next build steps.
It is a living doc — update it as decisions change.

---

## 1. Mission

A primitive agentic engine for infai. The model is a *pure text-in/text-out*
component with **zero capabilities**. Every interaction with the outside world
is granted by the harness and travels through a named, policy-checked code path.

## 2. Core architecture decisions

### D1. Server = execution host; TUI = pure client. No single-run mode.
The `<binary> server` process is the only thing that executes tools, spawns
subagents, and talks to the model. The TUI (`<binary>`) is a client that
attaches over HTTP/SSE. There is deliberately **no** "run everything in the
client process" mode — the server is the chokepoint for policy and isolation.

### D2. The harness grants capability; the model never asks for it.
Tools are **code paths, not command strings**. The model cannot execute a shell
command unless a tool accepts one. Tool design is the primary defense: a tool
like `search_files(pattern, dir)` performs its own path validation; nothing
accepts arbitrary `bash -c`.

### D3. Streaming is always on (server → model) and always on (TUI).
- The adapter always consumes the model via SSE (`Stream: true`), accumulating
  the full message even when nobody listens to deltas. One code path.
- The TUI always streams from the server (`?stream=true`, `Accept:
  text/event-stream`). No JSON fallback, no toggle — the earlier dual nature was
  removed (`pkg/agent/tui/remote.go`).

### D4. `contracts.ChatMessage` is the single source of truth and the wire format.
- Preserve `reasoning_content` on the wire (DeepSeek requires it for tool turns;
  vLLM degrades without it).
- Never send a `thinking` message field (not in the OpenAI message schema).

### D5. Sessions are passive state in a registry.
Sessions live server-side and survive client disconnects. No per-session
goroutines are held; close via ctx cancel + registry removal.

## 3. Tool & permission layer (next milestone)

### D6. ToolEngine is a central place (like `engine`).
Given a session + a requested `ToolCall`, it grants access to that specific tool
against the session's policy, executes it, and returns a result.

### D7. AccessControl is middleware over every tool call.
A **pure function** `authorize(ctx, sessionPolicy, toolCall) → decision`
(allow / deny / ask), wrapped as middleware in the tool path. Pattern borrowed
from Authzed, but **not** the system — no SpiceDB/Zanzibar, no policy DSL. A
policy is a Go struct / JSON table. It must be I/O-free and unit-testable.

### D8. The workspace is an explicit capability, not process cwd.
claude-code/opencode get isolation "for free" because the process is born in the
cwd. A long-lived multi-session server cannot rely on its own cwd, so:
- Session creation declares a `workspace` root (TUI passes cwd — same UX).
- All file paths are resolved with `Abs` + `EvalSymlinks`, then prefix-checked
  against the workspace root (symlink escapes are the #1 footgun).
- The workspace is **fixed at session creation**. New project = new session.
- `fs` capability = workspace (rw) + profile-declared extra roots (ro),
  e.g. `~/.gitconfig`, `~/.npmrc`. Extra roots are human-declared, never
  model-chosen.
- **Deny semantics**: a workspace escape is a hard deny, reported back to the
  model as a tool result ("denied: path outside workspace /x") so the model
  self-corrects (nemoclaw's "try allowed, report failure mode"). Only `ask`
  ops go to a human.

### D9. Bash is a special tool: ask-by-default.
Bash always risks escaping an allowlist (the `grep`/`find` → `bash -c` class of
escape). Policy = per-session allowlist of **full argv** (an allowed `git` does
not allow `git push`), plus an `*` fallback meaning **always ask the human**.
Everything else is a direct code-path tool.

### D10. Scratchpad: runtime-only working memory per agent.
Not persisted. Carries tool-call coordination across one turn: write-before-read
tracking, consecutive-error counts, retry budgets.

### D11. Validate hooks → completion gate → agent profile.
When the agent says "done", the harness runs the session's validation
(`go build && go vet`, `pnpm run dev`, …) and feeds the result back so the
agent iterates or gives up. policy + allowed tools + validate + system prompt +
pinned model = a **versioned agent profile**.

### D12. Composite tools (pipes) — sequence, not byte streams.
Bash's pipes are compositional; tools should be too. But tools speak structured
JSON, not raw byte streams, so a pipe composite feeds **structured output into a
matching input schema**:

```
pipeline(toolA, toolB)   # toolB declares an input schema matching toolA's output
```

A bash-pipe is itself a deterministic *sequence step* — so pipes live naturally
at the workflow layer (D14), where they are just UNIX. Composite tool == a
2-node sequence in the workflow graph (unification, section 8).

## 4. Subagent rules

- **Ceiling, then subset**: a subagent inherits the parent's policy as its
  *maximum*; `spawn_subagent(task, scope)` narrows it further. Blanket-allow
  inside a subagent is forbidden — it would be the model's way around the
  parent's permission checks.
- **Single-turn loop**: the subagent runs a bounded loop and returns a result.
  No continuous conversation.
- **Evaluation criterion — child-owned, checked by the child only**: every spawn
  carries an explicit completion criterion. The child calls `evaluate()` each
  loop iteration: pass → return `Done`, fail → continue. The parent **never
  re-runs the child's criterion** (that would pollute the parent). Instead the
  parent owns *fitness* — "is this useful to my goal" — and decides
  accept / retry / ask human. Different judgment, stays in the parent.
  - The eval-loop is a **mode, not mandatory**: `spawn(task, criterion?, scope)`
    with `max_iterations: 1` gives a single-shot delegation.
- **Hard rule: parent = decision hub, subagent = worker.** The child never
  decides policy; it does work and reports. Consistent by default, relaxed
  for trivial delegations.
- **Context: only the parent compacts.** The child gets a fresh, bounded
  context and returns a **compact artifact** (`Summary` + `Steps` trace) —
  never its transcript. That is what keeps the parent's context from exploding.
- **Thinking mode is a spawn parameter**, set by the parent (budget/effort).
- **No HITL inside the subagent**: the child cannot reach a human. HITL lives at
  the parent/session level. On failure it returns a trace; the parent relays it.

```
SubagentResult {
  Status:          Done | Blocked | Denied | Timeout
  CriterionResult: pass/fail
  Summary:         "what it did"
  Steps:           []action+result   // what & where
  StopReason:      "human denial" | "policy block" | "timeout" | "deadlock"
}
```

## 5. Fire-and-forget concurrent tool calls

The model's turn ends when it emits `[tool_1, tool_2, tool_3]` in one response.
The harness fans them out and waits — the model does not wait for them.

- Each call runs under its own `context.WithTimeout(ctx, toolTimeout)`.
- A `WaitGroup`/collector waits for **all**, then results are appended **keyed
  by `tool_call_id` in the request order** (never goroutine-completion order).
- Result envelope:

```
ToolCallResult { CallID, Status: success|error|timeout|canceled, Output, Error }
```

- A timeout kills *that* call, not the batch. The model sees the timeout result
  and decides (retry / adjust / give up).
- **The timeout clock starts AFTER approval.** Approval (`ask`) is untimed (a
  human takes as long as they take); the timer starts on actual execution.
- Default harness timeout: **5m**, overridable per call (caller's ctx feeds it).
- Parent cancellation tears down the whole fan-out via `ctx`.

## 6. Workflow engine (vision — next big milestone)

`<binary> --workflow=<file>` composes a **graph of computation units**: AI
agents *and* deterministic systems. Deterministic where it's great, LLM where
it's great — always a combination. Not stuck in HITL: headless, CI-friendly,
OTel-observable (per-node spans).

### D13. One abstraction: `Node` + `State`
```
Node  := { Name, Kind, input/output field mappings }
State := map[string]any     // structured data flowing between nodes
```

**Three node kinds:**
- `agent` — wraps a session: input state → templated prompt (+ tool scope +
  subagent policy) → agent runs → extract outputs into state.
- `step` — deterministic: bash `cmd` (pipes live here; reuse `runner` for
  exec/timeout/rlimits), a Go func, or a direct `ToolCall`.
- `composite` — `sequence`, `parallel`, `branch` (if/else on state), `loop`
  (until condition or budget). **Control-flow that escapes agent evaluation.**

Edges map `node.outputs.field → next.inputs.field`. A branch is decided
deterministically (`{plan}.ok == true`) **or by an agent** (non-deterministic
routing) — the "combination" in one primitive.

```yaml
release.yaml:
  plan:    {kind: agent,  prompt: "plan release for {repo}", outputs: {plan: reply}}
  build:   {kind: step,   cmd: "go build ./...",   after: plan}
  gate:    {kind: branch, on: "{plan}.ok",         yes: [deploy], no: [report]}
  deploy:  {kind: step,   cmd: "infai deploy",     after: gate}
```

### D14. Plumbing
`infai --workflow=release.yaml` (client) → POSTs workflow → server runs it as a
session → SSE streams node events (agent deltas + node status, unified) → exit
code = pass/fail. Approvals resolve via policy or record as blocked (headless).
Same server = usable as a GitHub Actions service. OTel per-node spans = the
LangSmith analogue.

### D15. Build path: deterministic-first
Steps + sequence + parallel + branch run with **zero LLM** (testable graph
runner) *first*; then `agent` becomes "just another node kind". The graph model
subsumes earlier layers: composite tool = 2-node sequence, subagent = agent
node, bash command = step node.

## 7. Security model (layers)

1. Tool design — code paths, no command strings. (primary)
2. AccessControl — allow/deny/ask middleware per call. (enforcement)
3. Workspace — canonicalized paths bound to the session root. (scope)
4. Process hygiene — server runs as an unprivileged user; exec under process
   groups + `prlimit` (CPU/mem/nofile). (containment)
5. Network/egress — policy + human approval on unapproved egress.
6. syscall filtering (seccomp) — hygiene layer only; it does **not** stop
   `bash -c`. It can only block whole classes (`mount`, `ptrace`, userns
   creation). Enforced by the process on its own children via
   `prctl(PR_SET_SECCOMP)`; **no root required**.
7. Sandbox backstop (deferred) — `bwrap` (unprivileged user namespaces) via
   `--bind $WORKSPACE /sandbox` turns the path rule into kernel-enforced mounts.
   Also no root required; deferred until the tool set stabilizes.

## 8. Deferred (on purpose)

- **git worktree for multi-session contention** — we *will* borrow this: when a
  workspace has `.git`, give each session its own worktree
  (`$XDG_DATA_HOME/infai/worktrees/<repo-id>/<session-id>`) so sessions build
  and edit the same repo in parallel with zero contention, each on its own
  branch. Worktree is *concurrency/branch* isolation, **not** security.
  - Non-git projects fall back to the plain workspace.
  - Later: a warning when another session is already running on the same folder.
- **WASM workbench** — right for pure-compute plugin tools, wrong for real
  binaries (git, npm, test runners) whose credential helpers break under WASI.
- **Workspace lock / serialization** — only needed for non-git workspaces.
- Concurrency amplifies shared-workspace races; worktree is the fix.

## 9. Next steps (build order)

**Done:** provider registry + session persistence (`pkg/agent/store`), slash-command
CLI with live status header, recorder multi-writer (stream → user + transcript).

Storage layout (`$XDG_CONFIG_HOME/infai/harness`):
- `models.json` — provider registry (name/base_url/model/api_key/ctx_window/
  default + lazily fetched available models from the provider's `/v1/models`).
- `sessions/<uuid>.jsonl` — one append-only transcript per session. Kinds:
  `meta`, `message`, `tool_call`, `tool_result`, `usage`, `delta` (delta is
  live-only — it fans out to the user sink but never lands in the file).
- The session's `SessionEventHub` is the live broadcaster: one `Publish()` fans out to the
  live sink (SSE response / stdout) and persists the durable transcript.
- Session meta records the model used, last-use method (cli/ui/server) and cwd.
- Tool-call schema + recording path are wired; the tool loop (AccessControl)
  is the remaining producer.

1. `Tool` contract + registry — `Tool`, `ToolCall`, `ToolResult`, `ToolRegistry`
   (each tool declares its capability + schema). Pure types next to `contracts`.
2. AccessControl — policy model (allow/deny/ask per capability + path/host
   constraints) + `authorize()` pure function + middleware.
3. ToolEngine — executes an *authorized* call: runs the tool impl under
   ctx/timeout, returns `ToolResult`, records to scratchpad.
4. Scratchpad — in-memory per-session helper.
5. **Loop integration (the big one)** — `Invoke` grows a tool step: model
   proposes `ToolCall` → AccessControl → fan-out concurrent execution
   (section 5) → append results → loop until no calls. Includes the
   `ToolCallResult` timeout envelope.
6. Bash tool — ask-by-default + session allowlist.
7. Validate gate — run checks on "done", feed results back, iterate.
8. Profile manifest — policy + tools + validate + system prompt + model,
   versioned.
9. Subagent spawn — single-turn child loop, `Done`/`Blocked` result, approval
   routed via parent.
10. HITL plumbing — wire `TurnPendingApproval` → session → TUI approve/deny →
    resume.
11. **Workflow engine** (section 6) — deterministic-first: `Node`/`State`
    abstraction, `step` + `sequence`/`parallel`/`branch`/`loop`, then `agent`
    as a node kind, then `--workflow` client flag + headless/CI mode + OTel.

Prototype slices 1–5 as a vertical slice first: one code-path tool
(`read_file`), one `ask` policy, prove the loop, then layer the rest.
