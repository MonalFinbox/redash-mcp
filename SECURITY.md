# Security policy

## Reporting a vulnerability

Report vulnerabilities privately through GitHub: open the repository's
**Security** tab and choose **Report a vulnerability**. Please do not open a
public issue or pull request for a security problem.

Reports are especially welcome for anything that could let this server:

- send a request other than GET, or reach an endpoint outside
  `internal/policy/endpoints.go`
- connect to a host other than a configured Redash instance
- expose an API key in a request URL, log line, error or tool result
- return data from a data source outside an instance's allowlist
- return a value from a column the redaction pattern matches
- listen on a port or start a process

Include the version or commit, your configuration with keys removed, and the
tool calls or input that reproduce the problem. You should get an
acknowledgement within a week.

## Supported versions

Until the first tagged release, only the latest commit on `main` receives
security fixes.

## Out of scope

- Vulnerabilities in Redash itself; report those to the Redash project
- Data from a data source that was deliberately added to an allowlist
- Personal data in columns whose names do not match the redaction pattern,
  which is a documented limitation (see [the security model](docs/security.md#what-this-does-not-protect-against))
- What an AI provider does with results it has been sent
