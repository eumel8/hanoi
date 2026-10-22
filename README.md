# hanoi

Basic AI harness in Go.

## Features

- CLI prompt interface
- streaming output with `-stream`
- conversation memory with `-memory`
- config via `.env`
- provider override via `OPENAI_BASE_URL`
- multiple OpenAI-compatible providers in `hanoi.json[c]`, with CLI and TUI model selection
- works as a simple OpenAI-compatible harness
- includes OpenAI-style tool calling with a local `bash` tool
- optional API logging with `-log <file>`
- MCP tools from local and remote servers, with OpenCode-compatible config and `/mcps` management
- tag-based `-version` output and GitHub Actions builds for Linux, macOS, and Windows

## Setup

Create a `.env` file:

```bash
OPENAI_API_KEY=your_key_here
OPENAI_MODEL=gpt-4o-mini
OPENAI_BASE_URL=https://api.openai.com/v1/chat/completions
```

Environment variables still work too.

## Run

Single prompt:

```bash
go run . "Sag hallo auf Deutsch"
```

Streaming:

```bash
go run . -stream "Erzähl einen kurzen Witz"
```

Conversation memory:

```bash
go run . -memory chat1 "Mein Name ist Eva"
go run . -memory chat1 "Wie heiße ich?"
```

TUI chat mode:

```bash
go run . -tui
go run . -tui -memory chat1
```

Inside TUI:

- `Enter` sends
- `Alt+Enter` inserts a newline
- `↑`/`↓` recalls earlier prompts and slash commands at the first/last input line;
  `Alt+↑`/`Alt+↓` recalls them from anywhere, preserving the current draft
  (session history, seeded from saved user messages when using `-memory`)
- `PgUp`/`PgDown` or the mouse wheel scrolls the chat; `Ctrl+Home`/`Ctrl+End`
  jumps to the beginning/end, with a scrollbar showing the current position
- assistant responses stream live in the TUI
- scrolling up pauses automatic following until you return to the bottom
- Markdown headings, lists, tables, and syntax-highlighted code blocks render live,
  with wrapping adjusted to the terminal width and colors for light or dark backgrounds
- `/exit` or `/quit` ends the session
- `/clear` resets the current conversation
- `/save` writes memory to disk
- `/model <provider/model>` switches provider and model (`/model <name>` switches within the active provider)
- `/providers`, `/models`, or `/model` opens the provider/model picker
- `/system <text>` sets the system prompt
- `/mcps` opens MCP server management
- `/help` shows commands

Build:

```bash
go build
./hanoi -version
./hanoi -tui -memory chat1
```

## Versions and release builds

`hanoi -version` prints the embedded version and exits without loading `.env`,
requiring an API key, or connecting to MCP servers. For example, a build from tag
`v1.2.3` prints `hanoi v1.2.3`. A plain `go build` without version injection prints
`hanoi dev`.

To embed the current Git version in a local build (Bash):

```bash
go build -trimpath -ldflags "-X main.version=$(git describe --tags --always --dirty)" -o hanoi .
```

`.github/workflows/build.yml` runs on branch/tag pushes, pull requests, and manual
dispatches. It runs race-enabled tests and `go vet` on Linux, then builds:

| Platform | Architectures | Binaries |
| --- | --- | --- |
| Linux | amd64, arm64 | `hanoi-linux-amd64`, `hanoi-linux-arm64` |
| macOS | amd64, arm64 | `hanoi-darwin-amd64`, `hanoi-darwin-arm64` |
| Windows | amd64, arm64 | `hanoi-windows-amd64.exe`, `hanoi-windows-arm64.exe` |

Builds use the Go version specified in `go.mod`, with CGO disabled. Each binary
and its `.sha256` checksum are available as workflow artifacts. Tag pushes embed
the exact tag and publish the files to a GitHub Release. Other builds use
`git describe --tags --always --dirty`, for example `v1.2.3-4-gabcdef0`.

To trigger a release after pushing the workflow:

```bash
git tag v1.2.3
git push origin v1.2.3
```

Downloaded Linux/macOS binaries may need `chmod +x` before running. The built-in
`bash` tool requires Bash at runtime, including on Windows (for example from Git
for Windows).

## MCP servers

Hanoi reads the `mcp` section of a config following
[the OpenCode schema](https://opencode.ai/config.json). It searches the current
workspace for `hanoi.json`, `hanoi.jsonc`, `opencode.json`, then `opencode.jsonc`
and uses the first file found. JSONC comments and trailing commas are supported.
You can select a file explicitly with `-config path/to/config.json`.
The same file can configure `provider` and `model` as described below.
Other OpenCode settings are ignored.

Start by copying `hanoi.json.example` to `hanoi.json`. Its example servers are
disabled initially and can be enabled through `/mcps` after configuration.

Current schema format:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "timeout": { "startup": 10000, "catalog": 5000, "execution": 60000 },
    "servers": {
      "filesystem": {
        "type": "local",
        "command": ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."],
        "disabled": false
      }
    }
  }
}
```

Local servers use MCP stdio. `command` is an executable/argument array, not a shell
string. Optional `cwd` resolves relative to the workspace, and `environment`
overrides inherited environment variables. The filesystem example requires
Node.js and `npx`.

Remote servers use `"type": "remote"`, `"url": "https://host/mcp"`, and optional
`headers` or OAuth authentication. Hanoi tries Streamable HTTP, then legacy SSE.
Use `{env:VARIABLE}` in commands, paths, environment values, URLs, or headers
to reference environment variables (including variables loaded from `.env`).
Set `"oauth": false` when using a static Authorization header. `codemode: true`
is not implemented; use direct MCP tools instead.

The legacy OpenCode format is also accepted:

```json
{
  "mcp": {
    "filesystem": {
      "type": "local",
      "command": ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."],
      "enabled": true,
      "timeout": 10000
    }
  }
}
```

Timeouts are milliseconds. Per-server timeout objects override the global
`mcp.timeout` values. A legacy integer applies to startup, catalog, and execution.
Hanoi's defaults are 5000 ms for startup/catalog and 60000 ms for execution.
`protocol` supports `legacy` (default), `auto`, and `2026-07-28`.

### Managing servers with `/mcps`

```bash
./hanoi -tui -config hanoi.json -log debug.log
```

Type `/mcps` to open the server list with connection status, tool counts, and errors:

- `↑` / `↓`: select a server
- `Enter`: enable/connect or disable/disconnect the selected server
- `r`: reconnect and refresh the selected server's tools
- `t`: inspect tool names and descriptions
- `a`: authenticate with OAuth; `l`: log out
- `Ctrl+R`: reload the config and connections
- `Esc`: return to the chat

Commands are also available directly from the chat input:

```text
/mcps list
/mcps enable filesystem
/mcps disable filesystem
/mcps reconnect filesystem
/mcps tools filesystem
/mcps reload
/mcps auth remote-oauth-example
/mcps logout remote-oauth-example
```

Enable/disable changes apply to the current process. Edit `disabled` (or legacy
`enabled`) in the config to persist the startup state. Reloading applies the
file's settings; a config syntax error preserves existing connections.

Connected servers' tools are automatically advertised alongside `bash` in CLI,
streaming, and TUI chat requests. Tools receive stable, namespaced API names to
avoid collisions. MCP results, including structured content and tool errors,
are sent back to the model. Startup failures are visible in `/mcps` and do not
prevent other servers from being used. Connections and local processes are closed
when Hanoi exits. With `-log`, MCP connection events, local stderr, tool calls,
and results are logged as well as the model API traffic.

### Interactive OAuth

For a remote MCP server requiring OAuth, omit `oauth` or set it to `{}`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "my-service": {
        "type": "remote",
        "url": "https://your-service.example/mcp",
        "oauth": {}
      }
    }
  }
}
```

An unauthorized server shows `needs_auth` in `/mcps`. Select it and press
`Enter` or `a`, or run `/mcps auth my-service`. Hanoi discovers the OAuth
authorization server, dynamically registers a public client if needed, opens
the browser, and completes the Authorization Code flow with PKCE. After login,
Hanoi connects the MCP server and discovers its tools. OAuth works with both
Streamable HTTP and legacy SSE.

The login URL is also shown in `/mcps` if the browser cannot be opened. Login
waits up to three minutes and can be cancelled with `Esc`. The callback defaults
to `http://127.0.0.1:19876/mcp/oauth/callback`; the browser must be able to reach
that loopback address (use port forwarding when running Hanoi over SSH).

If the authorization provider needs an existing client registration, configure:

```json
"oauth": {
  "client_id": "{env:MCP_CLIENT_ID}",
  "client_secret": "{env:MCP_CLIENT_SECRET}",
  "scope": "read write",
  "callback_port": 19876
}
```

`client_secret` is optional for public clients. `redirect_uri` can specify a
different loopback HTTP callback URL and takes precedence over `callback_port`;
register that exact URI with your provider. `auth_server_metadata_url` optionally
overrides the discovery document location. Legacy OpenCode `clientId`,
`clientSecret`, `callbackPort`, and `redirectUri` names are accepted too.

Tokens and refresh configuration are stored under
`$XDG_CONFIG_HOME/hanoi/mcp-oauth/` (normally `~/.config/hanoi/mcp-oauth/` on Linux).
The directory uses `0700`, token files use `0600`, and updates are atomic.
Sessions are scoped to the configured server URL and OAuth settings. Subsequent
starts reuse the login and refresh tokens automatically. If refresh fails or the
session expires, use `/mcps auth my-service` again. Tokens, client secrets,
authorization codes, and token endpoint responses are not written to the debug
log by the OAuth implementation.

`/mcps logout my-service` disconnects the server and deletes its local OAuth
session. It does not revoke authorization at the provider. Disable the server
separately if you also want to remove it from startup connections.

## API logging

Enable logging when debugging requests or tool-call follow-ups:

```bash
./hanoi -log hanoi.log "Nutze bash und zeige das aktuelle Verzeichnis"
./hanoi -tui -log hanoi.log
```

The log appends UTC timestamps, request JSON, HTTP status codes, and response
body chunks (including streaming responses and API error bodies). Response chunks
are quoted to preserve newlines. Authorization headers are not logged. Prompts,
conversation history, and tool output are included and can contain sensitive data.
New log files are created with owner-only read/write permissions (`0600`).
Logging is disabled unless `-log` is supplied.

## Providers

Configure multiple providers using the
[OpenCode custom-provider format](https://opencode.ai/docs/providers/#custom-provider).
Providers share the same JSON/JSONC file as MCP servers (`-config` also applies to
providers). For example:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "openai/gpt-4o-mini",
  "provider": {
    "openai": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "OpenAI",
      "options": {
        "baseURL": "https://api.openai.com/v1",
        "apiKey": "{env:OPENAI_API_KEY}"
      },
      "models": {
        "gpt-4o-mini": { "name": "GPT-4o mini" }
      }
    },
    "ollama": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Ollama (local)",
      "options": { "baseURL": "http://localhost:11434/v1" },
      "models": {
        "qwen3-coder": { "name": "Qwen3 Coder" }
      }
    }
  }
}
```

`model` selects the startup provider/model. Override it for a single run:

```bash
go run . -model ollama/qwen3-coder "Hello"
go run . -tui -config hanoi.json
```

In the TUI, `/providers` or `/models` opens a sorted list with provider/model
display names and the active selection. Use `↑`/`↓` and `Enter` to switch, or
`Esc` to close. `/model ollama/qwen3-coder` switches directly. Switching applies
to subsequent requests and keeps the conversation; edit `model` in the file to
persist a default. If exactly one model is configured, it is selected automatically;
otherwise `model` or `-model` is required.

- Provider IDs are the keys of `provider`; model keys must match a configured model.
  A model's optional `id` overrides the model ID sent to the API. Model IDs can
  contain slashes, for example `gateway/vendor/model`.
- `options.baseURL` is an API base URL; Hanoi appends `/chat/completions`.
  A full Chat Completions endpoint is also accepted, including query parameters.
  The `openai` provider defaults to `https://api.openai.com/v1`.
- `options.apiKey` sets Bearer authentication. Omit it for unauthenticated local
  servers. The `openai` provider falls back to `OPENAI_API_KEY` when omitted.
- `options.headers` adds custom request headers and can override Authorization.
  Keys and header values support `{env:VARIABLE}`, including `.env` variables.
  Only the selected provider's variables are resolved, so an unused provider's
  missing credentials do not prevent startup.
- Hanoi uses OpenAI Chat Completions for all providers. `npm` may be omitted,
  `@ai-sdk/openai-compatible`, or `@ai-sdk/openai`; it is a compatibility marker,
  not an installed adapter. Native Anthropic, Bedrock, and other API protocols,
  OpenCode's credential store, and provider OAuth are not supported.

Without a `provider` section, the original `OPENAI_API_KEY`, `OPENAI_MODEL`, and
`OPENAI_BASE_URL` settings still work. `OPENAI_BASE_URL` continues to mean the
full endpoint URL in this mode. For example:

```bash
OPENAI_API_KEY=... OPENAI_MODEL=... OPENAI_BASE_URL=https://host/v1/chat/completions go run . "Hello"
```

## Tool calling

`hanoi` now sends an OpenAI-compatible `bash` tool with each request. If the model decides to call it, the command is executed locally via `bash -lc`, and the tool result is sent back to the model as a follow-up tool message.

Tool calling works in normal CLI mode, with `-stream`, and in the TUI. Streamed
tool-call fragments are assembled before execution, including multiple calls in
one response. The model's answer after tool execution appears in the chat;
the raw tool results can be inspected in the follow-up request in the API log.

Bash commands run in `/tmp/hanoi` by default (`os.TempDir()/hanoi` on other
platforms or when `TMPDIR` is set). The directory is created with `0700`
permissions. Configuration and conversation memory still use the launch directory.

Bash commands have a 30-second timeout. On Linux and macOS, a timeout stops the
whole command process group, including pipelines. The TUI status distinguishes
waiting for the model, receiving a response, and running a tool. Tool start/end
times are recorded with `-log`. The Bash tool description includes the working
directory and saved-results directory for follow-up questions.

Tool results larger than 32 KiB are saved in full under `/tmp/hanoi/tool-results/`
with owner-only file permissions (`0600`). The model receives a limited preview,
an explicit truncation notice, and the absolute file path so it can use `bash` to filter
or aggregate the full result. This applies to both MCP and Bash tools. Saved
results remain available after exit; remove them when no longer needed.

Example prompt:

```bash
go run . "Nutze das bash tool und gib mir die Dateien im aktuellen Ordner aus"
```

Conversation memory is stored in:

```bash
.hanoi/
```
