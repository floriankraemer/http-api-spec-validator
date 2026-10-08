# Validator

`http-api-spec` is a command line validator that checks OpenAPI documents and HTTP responses against the [HTTP API Design Standard](../specification/specification.md).
`http-api-spec` has two commands.
`lint` checks an OpenAPI 3.0 or 3.1 document.
`check` checks an HTTP response or a HAR (HTTP Archive) recording of responses.
`check` reads a response or recording from a file, from stdin with `-`, or fetches a response live with `--url`.

## Installation

### Release binaries

Versioned releases use the tag form `vX.Y.Z`.
The rolling `latest` pre-release is built from `main`.
Archives exist for Linux, macOS and Windows on amd64 and arm64.
The archive names follow `http-api-spec_<version>_<os>_<arch>`, and `http-api-spec_latest_<os>_<arch>` for the rolling release.
Verify the download against `checksums.txt`.

The repository is currently private, so downloads need an authenticated GitHub CLI:

```sh
gh release download latest -R floriankraemer/http-api-spec -p '*linux_amd64*' -p checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

### Go

```sh
GOPRIVATE=github.com/floriankraemer/* go install github.com/floriankraemer/http-api-spec/validator/cmd/http-api-spec@latest
```

`GOPRIVATE` is required while the repository is private.
`@latest` installs the newest commit on `main`, because release tags are not module-prefixed (`validator/vX.Y.Z`).
Versioned binaries come from GitHub Releases.

### Docker

The Compose file builds a `validator` image.
Only the `validator/` directory is mounted, at `/work`, so the input file must sit inside it.

```sh
docker compose run --rm validator lint /work/openapi.yaml
```

## Usage

```text
Usage:
  http-api-spec lint <openapi-file> [--format text|json] [--strict]
  http-api-spec check <response-file|-> [--method M] [--format text|json] [--strict]
  http-api-spec check <recording.har|-> [--url-filter REGEX] [--all] [--no-group] [--format text|json] [--strict]
  http-api-spec check --url URL [--method GET] [--header "K: V"]... [--timeout 30s] [--format text|json] [--strict]
  http-api-spec version
  http-api-spec help
```

`--format` selects `text` (default) or `json`.
`--strict` also fails the run on SHOULD findings.
Without it, only MUST findings fail the run.
`--method` sets the request method of a recorded response.
With `--url`, it defaults to `GET`.
`--method` cannot be combined with a HAR file, because each HAR entry carries its own method.
`--url` cannot be combined with a HAR file or any other input file.
`--header` adds a request header and can be repeated.
`--timeout` limits a live request and defaults to 30 seconds.

A file is treated as a HAR recording when its name ends in `.har`, or when its first non-whitespace byte after an optional UTF-8 byte order mark is `{`.
Stdin (`-`) is detected by content only.
The HAR flags are described in [Recording responses](#recording-responses).

Lint an OpenAPI document:

```sh
http-api-spec lint testdata/openapi/bad-naming-camel-case.yaml
```

```text
testdata/openapi/bad-naming-camel-case.yaml:56: MUST naming-camel-case #/components/schemas/Order/properties/customer_id: property "customer_id" is not camelCase
1 finding(s): 1 MUST, 0 SHOULD
```

Check a recorded response as JSON:

```sh
http-api-spec check --format json testdata/responses/bad-no-debug-field.http
```

```json
{
  "source": "testdata/responses/bad-no-debug-field.http",
  "findings": [
    {
      "ruleId": "no-debug-field",
      "level": "MUST",
      "location": "#/debug",
      "message": "error responses must not contain a debug member"
    }
  ]
}
```

Check a live endpoint:

```sh
http-api-spec check --url https://api.example.com/v1/orders --header "Authorization: Bearer $TOKEN"
```

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success, no failing findings. |
| 1 | Findings failed the run. |
| 2 | Usage or I/O error, or a HAR recording with no API entries. |

## Recording responses

HAR 1.2 is the de facto standard for recorded HTTP traffic.
It is a W3C Web Performance Working Group draft that was never finalised, but browsers and HTTP tools export it.
A HAR file is JSON, and the recorded exchanges sit in `log.entries`.

### Exporting a HAR file

- Chrome or Edge: open DevTools, go to Network, and choose "Export HAR (sanitized)…" (the download icon).
- Firefox: open the Network tab, open the gear menu, and choose "Save All As HAR".
- Safari: open Web Inspector, go to Network, and choose Export.
- mitmproxy: run `mitmdump --set hardump=./traffic.har`.
- Playwright: create the context with `browser.newContext({ recordHar: { path: 'traffic.har' } })`.
- Charles, Proxyman and Fiddler export HAR from their session menus.

For a single response, save the status line and headers with the body:

```sh
curl -si https://api.example.com/v1/orders > response.http
```

HTTP/2 and HTTP/3 status lines are accepted.

### What gets checked

By default, `check` looks only at API entries.
An entry counts as an API entry when one of these holds:

- The response is JSON or `application/problem+json`.
- The browser marked the request as `fetch` or `xhr`.
- The method is PUT, PATCH or DELETE.

Use `--url-filter REGEX` to narrow the check to one API, for example `--url-filter '/v1/orders'`.
The regular expression matches the full request URL.
Use `--all` to check every entry that has a recorded response.
HAR entries go through the same rules as a response file.

If no entry matches, the run exits with code 2 and suggests `--url-filter` or `--all`.

Run the check on a Chrome DevTools export:

```sh
http-api-spec check testdata/har/good-chrome-devtools.har
```

```text
checked 3 of 7 entries; skipped 2 non-API, 1 without recorded body, 1 without response
testdata/har/good-chrome-devtools.har: no findings
```

The first line goes to stderr, and the second goes to stdout.
The summary counts every entry that was not checked, grouped by reason.

Run the check on a recording with a repeated violation:

```sh
go run ./cmd/http-api-spec check testdata/har/bad-naming-camel-case-chromium.har
```

```text
checked 5 of 8 entries; skipped 3 non-API
testdata/har/bad-naming-camel-case-chromium.har: MUST naming-camel-case GET http://127.0.0.1:8080/v1/bad-naming-camel-case-snake #/data/order/customer_id: property "customer_id" is not camelCase (3 entries)
1 finding(s): 1 MUST, 0 SHOULD
```

`go run` also prints `exit status 1` because the run failed.

The same violation occurred in three entries, so the output shows one finding with `(3 entries)`.
Findings merge when they share the subject, the rule, the level, the location and the message.
The subject has the form `METHOD scheme://host/path`.
The validator drops userinfo, the query string and the fragment from the subject, so tokens in URLs do not reach the output.
A URL that cannot be parsed appears as `<invalid URL>`.

Use `--no-group` to list each finding on its own line.
Each line ends with `(entry #N)`, where N is the 0-based index of the entry in `log.entries`.

```text
testdata/har/bad-naming-camel-case-chromium.har: MUST naming-camel-case GET http://127.0.0.1:8080/v1/bad-naming-camel-case-snake (entry #4) #/data/order/customer_id: property "customer_id" is not camelCase
testdata/har/bad-naming-camel-case-chromium.har: MUST naming-camel-case GET http://127.0.0.1:8080/v1/bad-naming-camel-case-snake (entry #5) #/data/order/customer_id: property "customer_id" is not camelCase
testdata/har/bad-naming-camel-case-chromium.har: MUST naming-camel-case GET http://127.0.0.1:8080/v1/bad-naming-camel-case-snake (entry #6) #/data/order/customer_id: property "customer_id" is not camelCase
3 finding(s): 3 MUST, 0 SHOULD
```

Grouping compares paths literally.
Entries such as `/orders/1` and `/orders/2` do not merge, because their IDs differ.

### Skipped entries

The validator skips these entries and names each reason in the stderr summary:

- Entries without a response, such as blocked or failed requests (`without response`).
- Entries whose body the tool did not record (`without recorded body`).
  This includes an entry with an empty `content.text` while `content.size` is above zero, which Firefox writes.
  Responses with status 204 or 304 are exempt, because they have no body.
- Entries whose body is larger than 10 MiB (`body exceeds 10 MiB`).
- Entries with an invalid base64 body (`invalid base64 body`).

The summary lists the reasons in this order: `non-API`, `without recorded body`, `without response`, then the rest alphabetically.
For a recording with one checked entry and five skipped ones, the summary reads:

```text
checked 1 of 6 entries; skipped 1 without recorded body, 1 without response, 1 body exceeds 10 MiB, 2 invalid base64 body
```

### Privacy

HAR files can contain cookies, tokens and personal data in headers and bodies.
Chrome's and Edge's "Export HAR (sanitized)" removes `Cookie`, `Set-Cookie` and `Authorization` headers.
The unsanitized export requires the DevTools setting "Allow to generate HAR with sensitive data".
Sanitizing does not remove secrets in URLs, request bodies or response bodies.
Review a HAR file before you share it.
The validator reads HAR files locally and sends nothing anywhere.
Do not commit raw HAR files from real traffic.

### Limits

- A HAR file may be up to 256 MiB.
  The validator decodes `log.entries` one entry at a time and releases each body after conversion.
- A response body may be up to 10 MiB.
  Larger bodies are skipped, not rejected.

## Rules

Each rule reports one of two levels.
A MUST finding violates a requirement.
A SHOULD finding violates a recommendation.
The Level column below shows the level a rule reports.
Some rules report both levels, which the table notes.

### Lint

| Rule ID | Level | What it checks | Spec section |
|---------|-------|----------------|--------------|
| `version-path-prefix` | MUST | Paths start with a version segment such as `/v1`, or every applicable server URL ends in one (path-item `servers`, else top-level `servers`, with variables resolved to their defaults). | [Versioning](../specification/specification.md#versioning) |
| `locale-not-in-url` | MUST | Paths or query parameters carry a locale. | [Localization](../specification/specification.md#localization) |
| `success-data-envelope` | MUST | 2xx JSON response schemas are objects with an object `data` property. | [Response Structure](../specification/specification.md#response-structure) |
| `error-problem-json` | MUST | 4xx, 5xx and `default` responses use `application/problem+json`. | [Response Categories](../specification/specification.md#response-categories) |
| `idempotent-tag` | MUST | GET, PUT and DELETE operations carry the `idempotent` tag. | [Idempotency](../specification/specification.md#idempotency) |
| `no-batch-requests` | MUST | Request bodies use `multipart/mixed`. | [Batch and Bulk Requests](../specification/specification.md#batch-and-bulk-requests) |
| `bearer-jwt-auth` | MUST, SHOULD | An HTTP bearer scheme exists and the top-level `security` references it. A bearer scheme without `bearerFormat: JWT` is a SHOULD finding. | [Authentication](../specification/specification.md#authentication) |
| `naming-camel-case` | MUST | Schema property names are camelCase. | [Naming](../specification/specification.md#naming) |
| `no-underscore-fields` | MUST | Schema property names start with `_` other than `_links` and `_actions`. | [Reserved Fields](../specification/specification.md#reserved-fields) |
| `hypermedia-shape` | MUST | `_links` and `_actions` have the allowed members and required fields. | [HATEOAS](../specification/specification.md#hateoas) |
| `pagination-shape` | MUST | Operations do not combine `page` and `offset`. Paginated 200 responses have a `pagination` schema with the required properties. | [Pagination](../specification/specification.md#pagination) |

### Check

| Rule ID | Level | What it checks | Spec section |
|---------|-------|----------------|--------------|
| `status-category` | MUST | Status is 100 to 599. Bodies match the category: a success body is a JSON object with `data`, an error body is problem JSON, and `Content-Type` matches. | [Response Categories](../specification/specification.md#response-categories) |
| `deprecation-headers` | MUST | `Deprecation` is `@<unix-timestamp>` and `Sunset` is an HTTP date. | [Deprecation and Sunset Headers](../specification/specification.md#deprecation-and-sunset-headers) |
| `delete-status` | MUST | A successful DELETE answers 200, 202 or 204. | [Delete Operations](../specification/specification.md#delete-operations) |
| `obfuscated-ids` | SHOULD | Members named `id`, `*Id` or `*ID` are not JSON numbers. | [ID Obfuscation](../specification/specification.md#id-obfuscation) |
| `naming-camel-case` | MUST | Body property names are camelCase. | [Naming](../specification/specification.md#naming) |
| `no-underscore-fields` | MUST | Body property names start with `_` other than `_links` and `_actions`. | [Reserved Fields](../specification/specification.md#reserved-fields) |
| `hypermedia-shape` | MUST, SHOULD | `_links` and `_actions` have the allowed members and required fields. An empty `_links` or `_actions` object is a SHOULD finding. | [HATEOAS](../specification/specification.md#hateoas) |
| `pagination-shape` | MUST | Pagination members have the required integer properties and no unexpected ones. Pagination is top-level only. | [Pagination](../specification/specification.md#pagination) |
| `problem-details` | MUST | Error bodies have a string `title`, a string `type` (a missing `type` is only a SHOULD finding, as RFC 9457 defaults it to `about:blank`), an integer `status` equal to the HTTP status, and valid `detail`, `instance` and `errors` members. | [Error and Failure Messages](../specification/specification.md#error-and-failure-messages) |
| `error-code-format` | MUST | The `code` member has the form `<component>-<error>`. | [Error Codes](../specification/specification.md#error-codes) |
| `no-debug-field` | MUST | Error bodies have no `debug` member. | [Error and Failure Messages](../specification/specification.md#error-and-failure-messages) |

## Limitations

The validator does not check rules that need human judgement.
Examples are the wording of error messages, atomicity of bulk operations, whether a decision needs an ADR, and authorization.

Some checks use heuristics.

- Locale detection flags only `{lang}`, `{locale}` and `{language}` templates, `xx-YY` path segments, and `lang`, `locale` or `language` query parameters.
- A bare `/en/` segment is not flagged, to avoid false positives such as `/v1/me`.
- Bearer authentication is checked per operation on its effective security: the operation's own `security` if set, else the top-level one.
  An operation with an explicit empty `security: []` is treated as public and accepted.
- Operation-level `servers` are ignored when looking for a version in the server URL.
- In `lint`, keys of maps (`additionalProperties`, `patternProperties`) are not checked for naming.
  In `check`, every key of a response is checked, because a raw response cannot tell maps from objects.
  Prefer arrays of objects over dynamic-key maps.
- Pagination checks the 200 response only.
- `check` accepts HTTP/1.1, HTTP/2 and HTTP/3 status lines, such as the `HTTP/2 200` that `curl -i` saves.
- Response files and OpenAPI documents larger than 10 MiB are rejected with exit code 2.
- `check --url` follows up to 10 redirects within the same host.
  A redirect to another host is not followed, so custom `--header` values are never sent there.
  The redirect response itself is checked.
- Playwright's WebKit recorder splits comma-separated header values.
  The validator rejoins split HTTP-dates, such as `Thu` and `31 Dec 2026 23:59:59 GMT`, into one value.
  Other split values, such as two `Vary` entries, stay separate values.

## Development

Development runs through Docker Compose.
Run the commands from the `validator/` directory.

```sh
cd validator
docker compose run --rm dev                        # go test -race ./...
docker compose run --rm dev go test ./internal/openapi/
docker compose run --rm lint                       # golangci-lint run ./...
docker compose build validator
```

The services run as the host user.
They use `UID` and `GID`, which default to 1000.
If your IDs differ, export them before running Compose:

```sh
export UID GID=$(id -g)
```

Write the test first.
The fixtures under `testdata/` follow a naming convention that the tests enforce.

- Files named `testdata/{openapi,responses,har}/good-*` must produce no findings.
- Files named `testdata/{openapi,responses,har}/bad-<rule-id>-*` must produce findings for that rule only.
- `testdata/har/empty-no-api.har` is the exception.
  It has no API entries, so the tests expect exit code 2 for it.

HAR fixtures in `testdata/har/` come from two sources.
`testdata/har/record/record.sh` regenerates the Playwright recordings with Docker.
It drives Chromium, Firefox and WebKit, and it overwrites the `good-*` and `bad-*-chromium` fixtures.
The container images are pinned by digest.
The script waits until the recorder server accepts connections on `127.0.0.1:8080` before it starts the browsers.
The `*-devtools.har` fixtures and `empty-no-api.har` come from manual exports, so the script does not touch them.

## Releases

A push to `main` updates the rolling `latest` pre-release.
A SemVer tag `vX.Y.Z` creates a release through GoReleaser.
Pre-release tags such as `v1.2.0-rc.1` are allowed.
Any other `v*` tag fails the workflow.

```sh
git tag v0.1.0
git push origin v0.1.0
```
