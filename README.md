# redash-mcp

A Model Context Protocol server that gives an AI assistant **read access** to
[Redash](https://redash.io), and is structurally incapable of writing to it.

Most Redash MCP servers expose `create_query`, `update_query`,
`archive_query` and arbitrary ad-hoc SQL, with no read-only mode, no row caps
and no redaction. Pointing one of those at a company Redash puts a production
warehouse one prompt injection away from a mutated dashboard or a dumped
table. This one is built the other way round: the safety property comes
first, and it is small enough to audit in an afternoon.

> **Status: pre-release.** All eight tools work end to end and have been run
> against a live Redash deployment. There are no tagged releases or signed binaries
> yet, so install from source. Read [Limitations](#limitations) before
> pointing it at anything sensitive.

## What your assistant can do

| Tool | Returns |
| --- | --- |
| `redash_list_instances` | Configured instances, the default, how many layers enforce read-only on each, allowlists and limits. Makes no request to Redash. |
| `redash_list_data_sources` | Data sources with id, name, type, and whether the key's account is View Only on each |
| `redash_get_schema` | Tables and columns of one data source, optionally filtered by table name |
| `redash_list_queries` | Saved queries by search or page, with whether a stored result exists and when it was retrieved |
| `redash_get_query` | One query's SQL, parameter names and types, and visualizations |
| `redash_get_query_results` | A stored result: row and cell capped, sensitive columns masked, with its age |
| `redash_list_dashboards` | Dashboards by search or page |
| `redash_get_dashboard` | A dashboard's widgets and the query behind each |

Every tool is annotated `readOnlyHint: true`, and none of them runs a query.
Results are what Redash has already stored, so the default configuration
cannot add load to any database.

## Quick start

You need Go 1.25 or newer, and network access to your Redash.

```bash
go install github.com/MonalFinbox/redash-mcp/cmd/redash-mcp@latest
```

Copy your API key from your Redash profile page into an owner-only file:

```bash
mkdir -p ~/.config/redash-mcp
printf '%s' 'your-redash-api-key' > ~/.config/redash-mcp/prod.key
chmod 600 ~/.config/redash-mcp/prod.key
```

Put the settings in `~/.config/redash-mcp/env`:

```bash
REDASH_INSTANCES=prod
REDASH_PROD_URL=https://redash.example.com
REDASH_PROD_API_KEY_FILE=~/.config/redash-mcp/prod.key
REDASH_PROD_ENFORCED_READONLY=true
```

Lock it down and check it:

```bash
chmod 600 ~/.config/redash-mcp/env
redash-mcp --env-file ~/.config/redash-mcp/env --check
```

`--check` prints the tier, the limits, each instance and how many layers
protect it, every endpoint and whether it is reachable, and the tools,
without making a single request to Redash.

Then connect a client. For Claude Code:

```bash
claude mcp add --scope user redash -- "$(go env GOPATH)/bin/redash-mcp" --env-file ~/.config/redash-mcp/env
```

Claude Desktop, Cursor, VS Code and other clients are covered in
[Connecting a client](docs/clients.md).

## Documentation

- [Connecting a client](docs/clients.md): setup for each AI client, and troubleshooting
- [Configuration](docs/configuration.md): every setting, the env file, allowlists, redaction, the audit log
- [Security model](docs/security.md): what is enforced, how it is tested, and what it does not protect against

## How the read-only guarantee works

Two independent layers, either of which alone would stop a write.

**1. The Redash account.** Redash has no read-only API key: a User API Key
carries every permission its owner has. What *can* be scoped is the account,
so this server is meant to run as a Redash user in a group with **View Only**
access to its data sources. Redash then refuses writes server-side no matter
what any client sends. See
[Create a View Only Redash account](docs/configuration.md#create-a-view-only-redash-account).

**2. The policy guard.** Inside the binary, tools never build URLs and never
hold an HTTP client. They name an endpoint from a closed table in
[`internal/policy/endpoints.go`](internal/policy/endpoints.go), and
`policy.Guard` is the only code in the program that issues a request. It
exposes exactly one method: `Get`. There is no `Do`, no `Post`, no `Delete`.

Tests keep that honest, and each fails loudly if it stops being true:

| Test | What it catches |
| --- | --- |
| `TestOnlyGETIsEverIssued` | A transport that fails on any non-GET request, driven against every endpoint |
| `TestOnlyPolicyPackageReachesTheNetwork` | Any package outside `internal/policy` importing `net/http`, `net`, `net/rpc` or `os/exec` |
| `TestNoNetworkOrSubprocessTransportFromTheSDK` | Any use of the MCP SDK's HTTP, SSE or subprocess transports |
| `TestPathBindingRejectsHostileValues` | Traversal, encoded traversal and query-string smuggling through path parameters |
| `TestArgumentsOutsideTheSchemaNeverReachRedash` | Undeclared tool arguments, unknown instances and malformed ids |
| `TestSecretFieldsAreDroppedAtTheDecoder` | A query's `api_key`, an owner's email, or a parameter's default value reaching a tool result |

The full list is in [the security model](docs/security.md#enforcement).

## What reaches the model

- **Never:** API keys, each query's own `api_key`, owners' email addresses and
  group ids, parameter default values, and the SQL text Redash attaches to a
  stored result. These are dropped when the response is decoded, not filtered
  afterwards.
- **Only from allowlisted data sources**, when `REDASH_<NAME>_DATA_SOURCES` is
  set. Anything else is left out of listings and refused by id.
- **Capped:** 200 rows, 512 characters per cell and 256 KiB per response by
  default. Every cut is listed in the result's `truncated` field.
- **Masked:** columns whose name matches the redaction pattern, including SQL
  aliases such as `Customer PAN`.
- **Labelled:** every result carries `retrieved_at`, its `age`, and a notice
  that cell values are data written by third parties, not instructions.

## What it will never do

These are design constraints, not a roadmap. A pull request adding one should
be closed.

- Ad-hoc SQL of any kind
- Creating, editing, archiving or deleting queries, dashboards, visualizations or alerts
- Any access to `/api/users`, `/api/groups`, `/api/destinations`, or data source credentials
- Listening on a port, or starting a subprocess (stdio only)
- Telemetry, analytics, or any connection to a host other than your configured Redash

## Limitations

- **Verified live on one Redash deployment.** The client handles the response
  formats of both older and current Redash versions (dashboards addressed by
  slug or by id, schema columns as names or objects), but not every version
  has been run against a live instance.
- **Stored results only.** A stale result has to be refreshed in Redash. The
  `execute` tier, which would re-run saved queries, is not implemented.
- **The allowlist is applied after Redash pages results**, because Redash
  cannot filter queries by data source. A page can come back mostly hidden;
  `hidden_by_allowlist` says how many were dropped.
- **Redaction matches column names, not values.** A PAN stored in a column
  named `notes` is not masked. The allowlist is the stronger control; see
  [the security model](docs/security.md#what-this-does-not-protect-against).
- **No signed release binaries yet.**

## Milestones

- [x] **M0**: repo, CI, vulnerability scanning, secret scanning, dependency policy
- [x] **M1**: policy table, tier gate, guard, config validation, `--check`
- [x] **M2**: the MCP server, eight read tools, allowlists, result shaping, audit log, docs
- [ ] **M3**: tagged releases with signed binaries, Homebrew tap
- [ ] **M4**: execute tier behind its flag
- [ ] **M5**: fuzzing, MCP registry listing

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Anything touching `internal/policy` is
a security change and is reviewed as one. To report a vulnerability, see
[SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE)
