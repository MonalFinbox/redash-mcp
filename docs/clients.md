# Connecting a client

redash-mcp is a local stdio server: your AI client starts it as a
subprocess and talks to it over stdin and stdout. Nothing listens on a port,
and there is no hosted version. Any client that supports stdio MCP servers can
use it.

- [Before you start](#before-you-start)
- [Claude Code](#claude-code)
- [Claude Desktop](#claude-desktop)
- [Cursor](#cursor)
- [VS Code](#vs-code)
- [Any other client](#any-other-client)
- [Check that it works](#check-that-it-works)
- [Troubleshooting](#troubleshooting)

## Before you start

1. **Install the binary** and note its absolute path:

   ```bash
   go install github.com/MonalFinbox/redash-mcp/cmd/redash-mcp@latest
   echo "$(go env GOPATH)/bin/redash-mcp"
   ```

   Desktop applications do not read your shell's `PATH`, so give them this
   absolute path rather than `redash-mcp`.

2. **Configure it** with a key file and an env file, as in
   [Configuration](configuration.md#the-env-file).

3. **Run the same command your client will run**, with `--check` added:

   ```bash
   /Users/you/go/bin/redash-mcp --env-file ~/.config/redash-mcp/env --check
   ```

   If this fails, the client will fail the same way, with a less helpful
   message. Fix it here first.

The examples below use `/Users/you/go/bin/redash-mcp` and
`/Users/you/.config/redash-mcp/env`. Replace both with your own paths.

## Claude Code

```bash
claude mcp add --scope user redash -- /Users/you/go/bin/redash-mcp --env-file /Users/you/.config/redash-mcp/env
claude mcp list
```

`--scope user` makes the server available in every project on your machine.
The `--` separates Claude Code's own options from the server's command line.

Avoid adding it at project scope in a shared repository: the paths are
specific to your machine, and a project `.mcp.json` is usually committed.

## Claude Desktop

Open the configuration file:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

Add the server under `mcpServers`, keeping any servers already there:

```json
{
  "mcpServers": {
    "redash": {
      "command": "/Users/you/go/bin/redash-mcp",
      "args": ["--env-file", "/Users/you/.config/redash-mcp/env"]
    }
  }
}
```

Quit and reopen Claude Desktop. The server's stderr, including the audit log,
is written to Claude Desktop's MCP log files.

## Cursor

Add the same block to `~/.cursor/mcp.json` for every project, or to
`.cursor/mcp.json` inside one project:

```json
{
  "mcpServers": {
    "redash": {
      "command": "/Users/you/go/bin/redash-mcp",
      "args": ["--env-file", "/Users/you/.config/redash-mcp/env"]
    }
  }
}
```

## VS Code

Add the server to `.vscode/mcp.json` in a workspace, or to your user MCP
configuration. VS Code uses a `servers` key:

```json
{
  "servers": {
    "redash": {
      "type": "stdio",
      "command": "/Users/you/go/bin/redash-mcp",
      "args": ["--env-file", "/Users/you/.config/redash-mcp/env"]
    }
  }
}
```

## Any other client

Configure a stdio server with:

| Field | Value |
| --- | --- |
| command | absolute path to `redash-mcp` |
| arguments | `--env-file`, then the absolute path to your env file |
| environment | none needed when you use an env file |

If your client can only pass settings as environment variables, set the
`REDASH_` variables from [Configuration](configuration.md#all-settings) in its
environment block instead, and keep using `REDASH_<NAME>_API_KEY_FILE` so the
key itself stays out of the client's configuration file.

## Check that it works

Ask the assistant:

> Which Redash instances can you see?

It should call `redash_list_instances` and describe each instance, the default,
and how many layers enforce read-only access on it. That call makes no request
to Redash, so it works even when Redash is unreachable.

Then try something that reaches Redash:

> Find saved queries about disbursals and show me the latest result of the most relevant one.

Expect `redash_list_queries` followed by `redash_get_query_results`, and an
answer that mentions how old the result is.

## Troubleshooting

| Message | Cause and fix |
| --- | --- |
| `cannot reach Redash instance ... check that your VPN is connected` | The host is unreachable from this machine. Connect the VPN or network your Redash needs. |
| `... is readable by group or others ... chmod 600 ...` | A key file, env file or audit log has loose permissions. Run the `chmod` in the message. |
| `REDASH_INSTANCES is empty` | The server did not receive its settings. Check the `--env-file` path in the client configuration is absolute and correct. |
| The client says the server failed to start | Run the client's exact command in a terminal with `--check` added. |
| `no instance given and no default instance is configured` | Set `REDASH_DEFAULT_INSTANCE`, or ask the assistant to name an instance. |
| `data source is not in this instance's allowlist` | Working as intended. Add the id to `REDASH_<NAME>_DATA_SOURCES` if it should be reachable. |
| `has no stored result, or does not exist` | The query has never been run, or its result has expired. Run it in Redash; this server never runs queries. |
| `refused ... with 403` | The key's account has no access to that object. |
| `rate limit reached: retry in 2s` | More than `REDASH_RATE_LIMIT` requests in a minute. Wait, or raise the limit. |
| `response exceeds the byte cap` | The response is larger than `REDASH_MAX_BYTES`. Follow the hint in the message, such as passing `table_filter`. |
| `unexpected additional properties` | The assistant passed an argument the tool does not take. It usually corrects itself on the next call. |
