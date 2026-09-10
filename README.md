# redash-mcp

A Model Context Protocol server that gives an AI assistant **read access** to
[Redash](https://redash.io) — and is structurally incapable of writing to it.

Every other Redash MCP server I could find exposes `create_query`,
`update_query`, `archive_query` and arbitrary ad-hoc SQL, with no read-only
mode, no row caps and no redaction. Pointing one of those at a company Redash
puts a production warehouse one prompt injection away from a mutated dashboard
or a dumped table. This one is built the other way round: the safety property
comes first, and it is small enough to audit in an afternoon.

> **Status: early.** The policy core, configuration and validation are done and
> tested. The MCP server itself is not wired up yet — see [Milestones](#milestones).
> `redash-mcp --check` already works and is useful on its own.

## How the read-only guarantee works

Two independent layers, either of which alone would stop a write:

**1. The Redash account.** Redash has no read-only API key — a User API Key
carries every permission its owner has. What *can* be scoped is the account, so
this server is meant to run as a dedicated Redash user in a group with
**View Only** access to its data sources. Redash then refuses writes server-side
no matter what any client sends. Setting this up is
[step one](#1-create-a-view-only-redash-account), not an optional hardening step.

**2. The policy guard.** Inside the binary, tools never build URLs and never
hold an HTTP client. They name an endpoint from a closed table in
[`internal/policy/endpoints.go`](internal/policy/endpoints.go), and
`policy.Guard` is the only code in the program that issues a request. It
exposes exactly one method — `Get`. There is no `Do`, no `Post`, no `Delete`.

Three tests keep that honest, and all three fail loudly if it stops being true:

| Test | What it catches |
| --- | --- |
| `TestOnlyGETIsEverIssued` | A transport that fails the build on any non-GET request, driven against every endpoint |
| `TestOnlyPolicyPackageReachesTheNetwork` | Any package outside `internal/policy` importing `net/http`, `net`, or `os/exec` |
| `TestPathBindingRejectsHostileValues` | Traversal, encoded traversal, and query-string smuggling through path parameters |

## What it will never do

These are design constraints, not a roadmap. A PR adding one should be closed.

- Ad-hoc SQL of any kind
- Creating, editing, archiving or deleting queries, dashboards, visualizations or alerts
- Any access to `/api/users`, `/api/groups`, `/api/destinations`, or data source credentials
- Any HTTP transport listening on a port — stdio only
- Telemetry, analytics, or any outbound connection to a host other than your configured Redash

## Setup

### 1. Create a View Only Redash account

In Redash, as an admin:

1. Create a new user, e.g. `mcp-readonly@yourcompany.com`.
2. Create a group, e.g. `mcp-readonly`, and add that user to it.
3. Give the group **View Only** access to each data source it should reach.
   Not Full Access — View Only is what blocks creating and running queries.
4. Sign in as that user and copy its API key from the profile page.

This also blocks Redash's text-type query parameters, which are its SQL
injection vector, since Redash refuses unsafe parameters under View-Only access.

If you cannot get such an account issued, the server still works, but it drops
to one enforcement layer and says so on every startup.

### 2. Store the key

```bash
mkdir -p ~/.config/redash-mcp
printf '%s' 'your-redash-api-key' > ~/.config/redash-mcp/prod.key
chmod 600 ~/.config/redash-mcp/prod.key
```

The server refuses to start if a key file is readable by group or others.

### 3. Configure

```bash
export REDASH_INSTANCES=prod,uat

export REDASH_PROD_URL=https://redash.example.com
export REDASH_PROD_API_KEY_FILE=~/.config/redash-mcp/prod.key
export REDASH_PROD_ENFORCED_READONLY=true      # the account is View Only
export REDASH_PROD_DATA_SOURCES=7,12           # optional allowlist by id

export REDASH_UAT_URL=https://redash-uat.example.com
export REDASH_UAT_API_KEY_FILE=~/.config/redash-mcp/uat.key
```

Then check it:

```bash
redash-mcp --check
```

This prints the tier, the limits, each instance and how many layers protect it,
every endpoint and whether it is reachable, and any warnings — without making a
single request to Redash.

### All settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `REDASH_INSTANCES` | *required* | Comma-separated instance names, e.g. `prod,uat` |
| `REDASH_<NAME>_URL` | *required* | Base URL. Must be `https`. |
| `REDASH_<NAME>_API_KEY_FILE` | — | Path to a `chmod 600` file holding the key. Preferred. |
| `REDASH_<NAME>_API_KEY` | — | The key inline. Used only if no key file is set. |
| `REDASH_<NAME>_ENFORCED_READONLY` | `false` | Declares the Redash account is View Only |
| `REDASH_<NAME>_DATA_SOURCES` | all | Restrict to these data source ids |
| `REDASH_TIER` | `read` | `read` (GET only) or `execute` |
| `REDASH_MAX_ROWS` | `200` | Row cap per result |
| `REDASH_MAX_BYTES` | `262144` | Byte cap per tool response |
| `REDASH_MAX_CELL_CHARS` | `512` | Per-cell character cap |
| `REDASH_REDACT_COLUMNS` | see below | Regex of column names to mask |
| `REDASH_RATE_LIMIT` | `30` | Requests per minute |
| `REDASH_TIMEOUT` | `20s` | Per-request timeout |
| `REDASH_ALLOW_PRIVATE_ADDRS` | `false` | Permit a Redash on a private address |
| `REDASH_AUDIT_LOG` | `stderr` | Audit destination |

### Redash behind a VPN

If your Redash resolves to a private address — normal for self-hosted or
VPN-only deployments — startup refuses it by default, because that is also
what an SSRF attempt looks like. Opt in:

```bash
export REDASH_ALLOW_PRIVATE_ADDRS=true
```

A host that does not resolve at all is only a warning, not an error, since
that is the ordinary state of an office-only Redash when the VPN is down. You
get a message naming the VPN when a call actually fails.

### Redaction

The default pattern masks common PII column names, anchored on word boundaries
so it does not eat ordinary columns — a naive substring match for `pan` would
also redact `company`:

```
(?i)(^|_)(pan|aadhaar|aadhar|email|phone|mobile|card_no|card_number|
account_no|acct_no|dob|ifsc|upi|ssn|passport|cvv)($|_)
```

Replace it with the column names your warehouse actually uses.

## Capability tiers

| Tier | Enabled by | Adds | Warehouse load |
| --- | --- | --- | --- |
| `read` | default | Cached results, query and dashboard metadata, schema | **None, ever** |
| `execute` | `REDASH_TIER=execute` | Re-run an already-saved query with type-checked parameters | Yes, rate limited |

In the default tier the server reads Redash's stored results and executes
nothing, so it cannot add load to a production database. Only `POST` runs SQL,
and no `POST` code path exists.

## Milestones

- [x] **M0** — repo, CI, vulnerability scanning, secret scanning, dependency budget
- [x] **M1** — policy table, tier gate, guard, config validation, `--check`
- [ ] **M2** — the MCP server and the seven read tools
- [ ] **M3** — docs, signed release binaries, Homebrew tap
- [ ] **M4** — execute tier behind its flag
- [ ] **M5** — audit log, fuzzing, MCP registry

## Contributing

The dependency budget is enforced in CI: this server reaches the network with
the standard library alone. A pull request that adds a module has to argue for
it. Anything touching `internal/policy` is a security change and is reviewed as
one.

## License

[Apache-2.0](LICENSE)
