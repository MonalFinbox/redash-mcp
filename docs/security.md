# Security model

This server exists to let an AI assistant read a Redash that may front
production databases. The assistant's input can be influenced by anyone who
can put text in front of it, including text stored in the very data it reads.
So the design assumes the assistant can be talked into asking for anything,
and makes sure the server has nothing dangerous to give.

- [What is protected](#what-is-protected)
- [Enforcement](#enforcement)
- [What reaches the model](#what-reaches-the-model)
- [What this does not protect against](#what-this-does-not-protect-against)
- [Dependencies](#dependencies)
- [Recommended deployment](#recommended-deployment)

## What is protected

| Asset | Threat | Control |
| --- | --- | --- |
| Queries, dashboards, alerts | An assistant is induced to modify or delete them | View Only account; GET-only guard with no write path |
| Databases behind Redash | Ad-hoc SQL, or load from re-running queries | No ad-hoc SQL endpoint exists; the default tier executes nothing |
| API keys | Leaked into URLs, logs, errors or tool results | Key sent only in a header; key files must be owner-only; errors tested never to contain it |
| Data outside the intended scope | Results from the wrong database reach the model | Per-instance data source allowlist |
| Personal data in results | Sensitive columns reach the model | Column redaction, row and cell caps |
| Other hosts | The server is used to reach an internal service | Destinations come only from configuration; https only; private addresses need an opt-in; redirects are never followed |

## Enforcement

**Two independent layers stop writes.** The first is Redash itself, when the
key belongs to an account in a View Only group. The second is the policy guard
in this binary. `--check` and `redash_list_instances` report, per instance,
whether one layer or both are in place.

**One package can reach the network.** `internal/policy` holds the process's
only HTTP client, and the endpoint table in `endpoints.go` is the complete list
of requests the program can make. Every field of an endpoint is unexported, so
no other package can describe a request that is not already in the table. The
guard exposes a single method, `Get`.

**Tool arguments are closed.** Every tool's input schema rejects undeclared
properties, limits `instance` to configured names, bounds ids, page sizes and
string lengths, and is validated before a handler runs. Ids are integers and
dashboard slugs must match `^[A-Za-z0-9_-]{1,100}$`, so path traversal and
query-string smuggling cannot be expressed.

**Responses are decoded into allowlisted fields.** Redash returns secrets
alongside metadata: each query object carries its own `api_key`, owner objects
carry email addresses, and parameters carry default values that are often real
identifiers. The client decodes into structs that name only safe fields, so
everything else is discarded by the JSON decoder and never held in memory
where a later change could pass it on.

These properties are enforced by tests that fail if they stop being true:

| Test | Package | Asserts |
| --- | --- | --- |
| `TestOnlyGETIsEverIssued` | policy | No endpoint, at the highest tier, produces a non-GET request at the transport |
| `TestOnlyPolicyPackageReachesTheNetwork` | policy | No package outside `internal/policy` imports `net`, `net/http`, `net/rpc` or `os/exec` |
| `TestNoNetworkOrSubprocessTransportFromTheSDK` | policy | No code uses the MCP SDK's HTTP handlers, HTTP or SSE client transports, or command transport |
| `TestNoMutatingEndpointIsReadTier` | policy | No non-GET endpoint is reachable at the default tier |
| `TestPathBindingRejectsHostileValues` | policy | Traversal and smuggling attempts are refused before a request is built |
| `TestRedirectsAreNotFollowed` | policy | A redirect cannot carry the key to another host |
| `TestErrorsNeverContainTheAPIKey` | policy | Error text never includes the key |
| `TestSecretFieldsAreDroppedAtTheDecoder` | redash | `api_key`, email and parameter values never appear in client output |
| `TestSchemaOnWithheldSourceIsRefusedBeforeAnyRequest` | redash | A withheld data source is never requested |
| `TestArgumentsOutsideTheSchemaNeverReachRedash` | server | Undeclared arguments, unknown instances and malformed ids make no request |
| `TestResultsAreShapedRedactedAndAudited` | server | Row caps and redaction apply end to end, and result data stays out of the audit log |
| `TestUndeclaredRowKeysNeverReachTheOutput` | shape | A row value without a declared column cannot bypass redaction |

## What reaches the model

| Data | Reaches the model? |
| --- | --- |
| Instance names, URLs, allowlists, limits | Yes |
| Data source ids, names and types | Yes, for allowlisted data sources |
| Query names, descriptions, tags, owner display names | Yes, for allowlisted data sources |
| Query SQL | Yes, from `redash_get_query` only |
| Parameter names and types | Yes |
| Parameter default values | No |
| Stored result rows | Yes, capped, with matching columns masked |
| The SQL Redash attaches to a stored result | No |
| Owner email addresses and group ids | No |
| Query and dashboard `api_key` fields | No |
| Your API key | No |

Remember that whatever reaches the model also reaches the model's provider,
under whatever data terms apply to your account.

## What this does not protect against

Be clear-eyed about these before connecting an instance that holds personal
data.

- **Redaction is by column name, not by value.** Personal data in a column
  named `notes`, `payload` or `c7` is returned as it is. The data source
  allowlist is the control to rely on; redaction is a second line.
- **Query SQL is returned as written.** A query with a literal identifier in
  its `WHERE` clause exposes that identifier through `redash_get_query`.
- **Result data can carry prompt injection.** A cell can contain text written
  to manipulate the assistant. Every result is labelled as third-party data,
  and the server has no tool that could act on such an instruction, but the
  label cannot stop the assistant from repeating or believing what it reads.
- **One layer is weaker than two.** An instance using a key with write access
  depends entirely on this binary being correct. Use a View Only account for
  anything that matters.
- **Your machine is trusted.** Anyone who can read your key files, env file
  or audit log, or run code as your user, is outside this model.
- **Redash's own permissions are trusted.** If a key's account can see a data
  source, this server can read it unless the allowlist says otherwise.

## Dependencies

The binary is built from the Go standard library and the official MCP Go SDK,
which is maintained by the Model Context Protocol organisation with Google.
The SDK brings its own dependencies, so CI checks the complete list of modules
in the build against an approved list, and any new module, direct or
transitive, fails the build until it is reviewed. Dependabot proposes updates
weekly, `govulncheck` and CodeQL run on every change, and GitHub Actions are
pinned by commit SHA.

The SDK includes code for HTTP servers, HTTP clients and subprocess transports.
That code is linked into the binary but unreachable:
`TestNoNetworkOrSubprocessTransportFromTheSDK` fails if anything in this
repository uses it.

## Recommended deployment

1. Use a dedicated Redash account in a **View Only** group, and set
   `REDASH_<NAME>_ENFORCED_READONLY=true`.
2. Set `REDASH_<NAME>_DATA_SOURCES` on every instance, choosing masked copies
   of databases where they exist.
3. Keep key files and the env file `chmod 600`, outside any repository.
4. With several instances, set the default to the least sensitive one, or
   leave it unset so that every call names its instance.
5. Send the audit log to a file if you want a record of what was asked, and
   review it.
6. Extend `REDASH_REDACT_COLUMNS` with the column names your databases
   actually use.
