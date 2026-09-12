# Contributing

Thanks for helping. This project's value is that it is small and safe enough
to audit, so changes are judged first on whether they keep it that way.

## Build and test

You need Go 1.25 or newer.

```bash
go build ./...
go vet ./...
go test -race ./...
go run ./cmd/redash-mcp --env-file ~/.config/redash-mcp/env --check
```

The tests need no network and no Redash: every Redash response in the suite
comes from a fake transport.

## Layout

| Path | Responsibility |
| --- | --- |
| `cmd/redash-mcp` | Flags, `--check` banner, stdio server startup |
| `internal/policy` | The endpoint table, the GET-only guard, URL validation, and the architecture tests. The only package that may touch the network. |
| `internal/config` | Environment and env file parsing. Fails closed. |
| `internal/redash` | Typed Redash client: instance resolution, data source allowlist, rate limit, decoding into safe fields |
| `internal/shape` | Row, cell and byte caps, column redaction, result age |
| `internal/audit` | One JSON line per tool call |
| `internal/server` | MCP tool definitions and handlers |

## Rules

- **Anything in `internal/policy` is a security change.** Say so in the pull
  request and explain why the change cannot widen what the server can reach.
- **A new Redash endpoint is added to `internal/policy/endpoints.go` and
  nowhere else.** Mutating endpoints, ad-hoc SQL and admin endpoints will not
  be accepted; see "What it will never do" in the README.
- **A new module has to be argued for.** CI compares every module in the build
  against the approved list in `.github/workflows/ci.yml`. A pull request that
  adds one, directly or transitively, updates that list and explains why.
- **New response fields are decoded explicitly.** Add the field to a wire
  struct in `internal/redash` and to the output type. Never pass raw Redash
  JSON through to a tool result.
- **Every cut is reported.** Anything that drops rows, cells or fields must say
  so in the output.
- **Tests come with behaviour.** Security properties get a test that fails if
  the property breaks, not only one that passes today.
- **Run `gofmt`.** CI rejects unformatted code.

## Style

- Comments explain why, not what.
- No em dashes anywhere in the repository: code, comments, docs or commit
  messages. Use commas, colons or parentheses.
- Commit subjects follow `type(scope): summary`, one line, for example
  `feat(server): add table_filter to redash_get_schema`. Types in use: `feat`,
  `fix`, `refactor`, `test`, `docs`, `ci`, `chore`.

## Reporting security issues

Please do not open a public issue. See [SECURITY.md](SECURITY.md).
