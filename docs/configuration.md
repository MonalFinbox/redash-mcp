# Configuration

redash-mcp reads its settings from `REDASH_` environment variables. You can
set them in the environment your AI client launches the server with, or put
them in an env file and pass `--env-file`. The env file is the recommended
way: one file, owner-only, referenced from every client.

- [Create a View Only Redash account](#create-a-view-only-redash-account)
- [Store the API key](#store-the-api-key)
- [The env file](#the-env-file)
- [All settings](#all-settings)
- [Several instances and the default](#several-instances-and-the-default)
- [Data source allowlist](#data-source-allowlist)
- [Redaction](#redaction)
- [Limits](#limits)
- [Audit log](#audit-log)
- [Capability tiers](#capability-tiers)
- [Redash behind a VPN or on a private network](#redash-behind-a-vpn-or-on-a-private-network)

## Create a View Only Redash account

Redash has no read-only API key, so read-only has to be a property of the
account behind the key. As a Redash admin:

1. Create a user for the server, for example `mcp-readonly@yourcompany.com`.
2. Create a group, for example `mcp-readonly`, and add the user to it.
3. Give the group **View Only** access to each data source it should reach.
   Not Full Access: View Only is what blocks creating, editing and running
   queries.
4. Sign in as that user and copy the API key from its profile page.

View Only also makes Redash refuse text-type query parameters, which are its
SQL injection vector.

Then set `REDASH_<NAME>_ENFORCED_READONLY=true` for that instance. The flag
changes nothing about what the server does; it records that a second layer
exists, and `--check` and `redash_list_instances` report it.

If you cannot get such an account, the server still works using your own key.
It then relies on one enforcement layer, its own policy guard, and warns about
it at every startup.

## Store the API key

```bash
mkdir -p ~/.config/redash-mcp
printf '%s' 'your-redash-api-key' > ~/.config/redash-mcp/prod.key
chmod 600 ~/.config/redash-mcp/prod.key
```

`printf '%s'` writes the key without a trailing newline (one would be trimmed
anyway). The server refuses to start if a key file is readable by group or
others, and the error tells you the `chmod` that fixes it.

`REDASH_<NAME>_API_KEY` also accepts a key inline, but a file keeps the key
out of client configuration files, shell history and process listings.

## The env file

```bash
# ~/.config/redash-mcp/env
REDASH_INSTANCES=prod,uat
REDASH_DEFAULT_INSTANCE=uat

REDASH_PROD_URL=https://redash.example.com
REDASH_PROD_API_KEY_FILE=~/.config/redash-mcp/prod.key
REDASH_PROD_ENFORCED_READONLY=true
REDASH_PROD_DATA_SOURCES=3,7

REDASH_UAT_URL=https://redash-uat.example.com
REDASH_UAT_API_KEY_FILE=~/.config/redash-mcp/uat.key
REDASH_UAT_DATA_SOURCES=11,12
```

```bash
chmod 600 ~/.config/redash-mcp/env
redash-mcp --env-file ~/.config/redash-mcp/env --check
```

The format is deliberately narrow:

- One `KEY=VALUE` per line. A leading `export ` is allowed, so the file can
  also be sourced by a shell.
- Lines starting with `#` are comments. There are no inline comments, because
  a redaction pattern can legitimately contain `#`.
- A value may be wrapped in matching single or double quotes.
- Only `REDASH_` keys are accepted, and each may appear once. Anything else is
  an error, and errors name the line and key but never echo a value.
- The file must be `chmod 600`, like a key file, since it can hold an inline
  key.
- A variable already set in the process environment wins over the file.
- `~` is expanded in the `--env-file` path, in key file paths and in the audit
  log path, so client configurations that do not go through a shell still
  work.

## All settings

`<NAME>` is an instance name from `REDASH_INSTANCES` in upper case: instance
`uat` is configured by `REDASH_UAT_URL` and so on.

| Variable | Default | Meaning |
| --- | --- | --- |
| `REDASH_INSTANCES` | *required* | Comma-separated instance names. Lowercase letters, digits and underscores, starting with a letter. |
| `REDASH_DEFAULT_INSTANCE` | the only instance, if there is one | Instance a tool call uses when it names none. See [below](#several-instances-and-the-default). |
| `REDASH_<NAME>_URL` | *required* | Base URL. Must be `https` (plain `http` is accepted for localhost only), with no query string, fragment or embedded credentials. |
| `REDASH_<NAME>_API_KEY_FILE` | none | Path to a `chmod 600` file holding the key. Preferred. |
| `REDASH_<NAME>_API_KEY` | none | The key inline. Used only when no key file is set. |
| `REDASH_<NAME>_ENFORCED_READONLY` | `false` | Declares that the key's account is View Only. Reported, never relied on. |
| `REDASH_<NAME>_DATA_SOURCES` | every data source | Comma-separated data source ids this instance may expose. |
| `REDASH_TIER` | `read` | `read` or `execute`. `execute` currently adds no tools. |
| `REDASH_MAX_ROWS` | `200` | Rows per result, 1 to 100000. |
| `REDASH_MAX_BYTES` | `262144` | Bytes per tool response, 1024 to 16777216. |
| `REDASH_MAX_CELL_CHARS` | `512` | Characters per cell, 16 to 1048576. |
| `REDASH_REDACT_COLUMNS` | see [Redaction](#redaction) | Regular expression of column names to mask. |
| `REDASH_RATE_LIMIT` | `30` | Requests to Redash per minute, across all instances. |
| `REDASH_TIMEOUT` | `20s` | Per-request timeout, 1s to 5m. |
| `REDASH_ALLOW_PRIVATE_ADDRS` | `false` | Permit a Redash whose hostname resolves to a private address. |
| `REDASH_AUDIT_LOG` | `stderr` | `stderr`, `off`, or a file path. |

Every setting fails closed: a value that cannot be parsed or is out of range
stops the server from starting, rather than silently falling back to a
default.

## Several instances and the default

With one instance, it is the default. With several, set
`REDASH_DEFAULT_INSTANCE` to the one a tool call should use when it does not
name an instance. Every tool takes an optional `instance` argument, limited
to the configured names, so the assistant can always choose explicitly.

With several instances and no default, `instance` becomes a required argument
on every tool. That is the stricter choice: a production instance is never
reached by omission.

## Data source allowlist

`REDASH_<NAME>_DATA_SOURCES` limits an instance to the listed data source ids.
It is the strongest data control this server has, because it decides which
databases' results can reach the model at all, whatever their column names.

With an allowlist set:

- `redash_list_data_sources` and `redash_list_queries` leave out everything
  else, and report how many they dropped in `hidden_by_allowlist`.
- `redash_get_query`, `redash_get_query_results` and `redash_get_schema`
  refuse anything on another data source. The schema check happens before
  any request is made.
- `redash_get_dashboard` keeps every widget in place but empties the ones
  built on other data sources and marks them `withheld`.

To find the ids, list the data sources your key can see:

```bash
curl -s -H "Authorization: Key $(cat ~/.config/redash-mcp/prod.key)" \
  https://redash.example.com/api/data_sources
```

Pick ids from that output, not from the names shown in the Redash UI. Two
things to watch for:

- **A number in a data source's name is not its id.** A data source named
  `Warehouse 5` can have id 31.
- **Names are not unique.** Two data sources can share a name exactly, and a
  masked and an unmasked copy of the same database often differ by a suffix
  such as `[MASKED]`. Prefer the masked one.

Because Redash cannot filter queries by data source, filtering happens after
Redash has paged the results. A page of 100 queries can come back with only a
few visible. Page on, or search more specifically.

## Redaction

Cells in columns whose name matches `REDASH_REDACT_COLUMNS` are replaced with
`[redacted]`, and the result lists them in `redacted_columns`. The default is:

```
(?i)(^|_)(pan|aadhaar|aadhar|email|phone|mobile|card_no|card_number|account_no|acct_no|dob|ifsc|upi|ssn|passport|cvv)($|_)
```

Each term must stand as its own word between underscores or the ends of the
name, so `pan` masks `pan`, `pan_number` and `customer_pan`, but not
`company` or `panel_id`.

Details worth knowing:

- **Aliases are normalised before matching.** Redash column names are often
  SQL aliases, so `Customer PAN`, `customer-pan` and `customerPan` are
  matched as `customer_pan` would be. A custom pattern is also tried against
  the name exactly as written.
- **Both the column name and its display name are checked.**
- **Nulls in a masked column are masked too**, so the mask does not reveal
  which rows had a value.
- **It errs towards masking.** `is_email_verified` is masked by the default,
  since it contains the word `email`.
- **It matches names, never values.** Personal data in a column with an
  innocent name is not masked. Use the allowlist for anything where that
  matters.

Setting `REDASH_REDACT_COLUMNS` replaces the default rather than adding to
it, so start from the default and extend it with the names your databases
use.

## Limits

| Limit | On breach |
| --- | --- |
| `REDASH_MAX_ROWS` | Leading rows are returned and `truncated` says how many of how many. |
| `REDASH_MAX_CELL_CHARS` | Longer values, including nested JSON, are cut and end in `...`; `truncated` counts them. |
| `REDASH_MAX_BYTES` | A result keeps the most rows that fit, and says so. Other responses fail with a hint, such as passing `table_filter` to `redash_get_schema`. |
| `REDASH_RATE_LIMIT` | The call fails with the number of seconds to wait. Calls are refused rather than queued, so the assistant never appears to hang. |
| `REDASH_TIMEOUT` | The call fails and suggests checking your VPN if the host is unreachable. |

Responses from Redash itself are capped at 8 MiB before parsing. A larger
response is refused rather than truncated, since half a JSON document cannot
be parsed.

## Audit log

Every tool call writes one JSON line:

```json
{"time":"2026-01-15T09:30:12.401233Z","tool":"redash_get_schema","instance":"uat","args":{"data_source_id":11,"table_filter":"loan"},"outcome":"ok","duration_ms":45,"bytes":5120}
```

Records hold the tool, instance, arguments, outcome, error text, duration, row
count and response size. They never hold result data or keys.

| `REDASH_AUDIT_LOG` | Behaviour |
| --- | --- |
| `stderr` (default) | Written to stderr, which most clients keep in their MCP server log |
| `off` | Discarded |
| a path | Appended to that file, created `chmod 600`; an existing file readable by others is refused |

`stdout` is refused: under the stdio transport it carries the protocol, and
anything else written there breaks the session.

## Capability tiers

| Tier | Enabled by | Adds | Load on databases |
| --- | --- | --- | --- |
| `read` | default | Stored results, query and dashboard metadata, schema | None |
| `execute` | `REDASH_TIER=execute` | Nothing yet. Reserved for re-running an already-saved query with type-checked parameters. | Would add load |

At `read`, the server reads what Redash has stored and executes nothing. Only
a `POST` runs SQL in Redash, and the program has no code path that can send
one.

## Redash behind a VPN or on a private network

If your Redash hostname resolves to a private address, as self-hosted and
internal deployments often do, startup refuses it by default, because that is
also what a server-side request forgery attempt looks like. Opt in:

```bash
REDASH_ALLOW_PRIVATE_ADDRS=true
```

A hostname that does not resolve at all is only a warning, since that is the
ordinary state of an internal Redash while a VPN is disconnected. A tool call
that cannot reach Redash says so and suggests checking the VPN.
