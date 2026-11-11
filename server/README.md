# Proctor server

Proctor is a self-hosted examination and proctoring platform. One installation
serves one educational institution, whether it runs as one process or several
behind a load balancer. This directory is the server: a Go module, licensed
AGPL-3.0-only, that owns the domain rules, the HTTP and WebSocket APIs, and
persistence. PostgreSQL holds every durable fact; the cache and the messages
nodes send each other can always be rebuilt from it.

This file is for people changing the server. To run an installation, read the
[operator guide](../docs/public/operator/index.mdx).

## Run it and test it

From the repository root, with Docker running:

```sh
make bootstrap     # once: check the host, install pinned tools and npm dependencies
make run-server    # build the webapp, start PostgreSQL and friends, run one node
```

The node listens on `127.0.0.1:8065`; the
[local setup guide](../docs/public/developers/local-setup.mdx) covers the
first sign-in and the development data. To run the binary yourself, copy
`config/config.example.json` to `config/config.json`, point it at PostgreSQL,
and run `go run ./server/cmd/proctor serve --config ./server/config/config.json`.

```sh
go test ./server/app/academic -run '^TestClassCreate' -count=1   # one test, while you work
make -C server check          # architecture, error catalog, i18n, OpenAPI, tests, race, vet
make -C server architecture   # only the boundary, security and contract tests (also checks Markdown links)
make -C server generate       # rerun every code generator; a clean tree stays unchanged
```

`go test ./...` and `check` need no network or database. Tests that need
PostgreSQL, Redis, S3 or SMTP carry the `integration` build tag and run
through targets that start the service in Docker, such as
`make -C server integration-postgres`; the
[testing guide](../docs/public/developers/testing.mdx) lists them.

### Write tests

Tests assert through [testify](https://github.com/stretchr/testify). Use
`require`; use `assert` only where seeing every failure helps (a loop over
many files) or the test must not stop (inside an HTTP handler or a goroutine).
`t.Fatal`, `t.Error` and their formatted forms are not used.

- One assertion per property, expected value first, so a failure names what
  broke. No `require.True(t, a && b)` or `if a || b { require.FailNow(...) }`.
- Call testify directly. A helper earns its place when it checks a domain
  property (`requireConflictConstraint`), not when it forwards one assertion.
- Give every case of a table a name and run it with `t.Run`.
- Assert outcomes: what was persisted, audited and published. When a fake
  records calls in a log named `events`, `order` or `calls`, assert the whole
  log with `require.Equal` and name the order that is the contract in a
  comment (authorization before audit, audit before the write, commit before
  effects). The whole list also proves what did not happen; never check it
  for membership or a subsequence.

`make quality` runs `testifylint`, which checks the testify rules in the
server, cache and VFS modules. The rest are for review. The mail module has no
third-party dependencies, so its tests use the standard `testing` package.

## Map of the packages

Top-level directories:

| Directory | What is in it |
| --- | --- |
| *(module root)* | Builds and runs one node. `server.New` is the only place that picks the real database, cache, mail, file store and cluster adapters. Start with `doc.go`. |
| `app/` | The use cases: what each person may do and what happens when they do it. See the next table. |
| `architecture/` | Tests only. They protect package boundaries (who may import whom), security properties (where authorization and audit happen, what reaches the wire and the Store) and published contracts, and fail on a broken Markdown link. Style is left to the linter. |
| `cluster/` | How nodes send each other messages. `local` is the one-node version, `memberlist` the multi-node one. Delivery is best effort. |
| `cmd/` | The binaries: `proctor` (the server and its operator commands), `proctor-healthcheck` (container readiness probe), `ptool` (developer generators and checks), `mailpreview` (renders sample mail). |
| `config/` | The `config.json` schema, its defaults, validation and `PROCTOR_` environment overrides. `config.example.json` shows every field. |
| `desktoprelease/` | Verifies the signed Proctor Desktop release artifacts this build trusts. |
| `filecontent/` | Reads and writes the bytes of managed files (profile pictures, Exam Resources, Workspaces, exports) through `packages/vfs`. |
| `httpapi/` | The REST API resources. One file per resource declares its routes, request and response types and errors. |
| `httpapi/route/` | The routing kernel the resources are written against: how an endpoint is declared, compiled, authenticated and dispatched, Problem Details, and the public error catalog. Its package comment is the guide to adding an endpoint. |
| `i18n/` | Translation data (`en.json`). No code. |
| `internal/` | Helpers private to this module: Let's Encrypt (`autocert`), certificate reload (`tlscert`), the OpenAPI compiler (`openapidoc`), the error catalog and the error-code analysis the tests use (`errorcatalog`, `errorflow`), `canonicaljson`, `weborigin`. |
| `localization/` | Validates the translations and resolves a message for a locale. |
| `logging/` | The structured logger every package writes to. |
| `metrics/` | The Prometheus registry and scrape listener, and the wrappers that measure the cache, mailer, file store and cluster. |
| `migrations/` | The SQL schema, embedded in the binary. Before the first release it is one baseline under `postgres/`. |
| `model/` | Domain types (Class, Exam, Sitting, Attempt, ...) with the checks a value can make on its own, and the typed IDs. |
| `openapi/` | The hand-written OpenAPI YAML. `openapi.json` beside it is generated; never edit it. |
| `operator/` | Offline commands that only read or migrate an installation: validate and show configuration, migrate, list Jobs, inspect the cluster. |
| `platform/` | Starts, health-checks and closes the shared infrastructure. `externalauth` holds the CAS and OIDC sign-in protocols. |
| `secretseal/` | Encrypts secrets that are stored in the database, such as MFA secrets and mail payloads. |
| `store/` | Persistence contracts (interfaces). `sqlstore` implements them on PostgreSQL, `storetest` holds their conformance suites, and `timerlayer`, `retrylayer` and `localcachelayer` are generated wrappers. |
| `templates/` | Mail templates: MJML source, generated HTML and plain text. |
| `testlib/` | Builds the real server for a test (`testlib.Setup`) with in-memory infrastructure. |
| `tracing/` | Optional OpenTelemetry tracing. |
| `websocket/` | The realtime WebSocket: connections, subscriptions, replay. |
| `webui/` | Serves the built browser application from `webapp/dist`. |

Packages under `app/`. Each area is one package with one `Service` and one
file per entity or flow:

| Package | What is in it |
| --- | --- |
| `app` | Assembly only: `App` names every service in a field, and `New` builds them. No use case lives here. |
| `app/academic` | Institution, Academic Units, Programmes, Programme Levels, Academic Periods, Classes, memberships and Affiliations. |
| `app/access` | Authorization (`Authorizer`), the audit trail (`Auditor`) and `RunAudited`, the wrapper most audited changes use. |
| `app/administration` | Roles, Role Bindings, the Access Policy, retention, and the audit listings. |
| `app/exam` | Exam authoring, publication, records and exports. Its children: `sitting` (scheduling and lifecycle), `attempt` (the candidate side, through Submission), `review` (Integrity Review and result release), `resource`, `workspace` (Starter Workspace), `correction`, `manageraccess`, `safemarkdown`. |
| `app/identity` | Sign-in, Sessions, MFA, personal access tokens, Desktop authorization, account recovery and first-time bootstrap. |
| `app/onboarding` | Invitations, self-registration, onboarding imports and Student Progression. |
| `app/user` | The User directory and profile, the settings document and the profile picture. |
| `app/search` | One search across Exams, Classes and Users, each filtered as its own list is. |
| `app/job`, `app/jobs` | `job` is the engine that claims, retries and schedules Jobs; `jobs` holds the handlers, one file per Job. |
| `app/mail` | Composes transactional mail, and the operator's delivery controls. Sending happens later, in a Job. |
| `app/realtime` | The events sent to connected clients and the `Publisher` areas call after a commit. |
| `app/apperror`, `app/invocation`, `app/idempotency`, `app/paging` | Small shared pieces: the one error type, the caller context every use case takes, retry-safe commands, and list pages. |

## Follow one request

Creating a Class, `POST /api/v1/programme-levels/{programme_level_id}/classes`:

1. **Route.** `httpapi/class.go`: `classResource` declares the route as an
   `route.Endpoint{...}` literal. `Auth: route.AuthPrincipalRequired` means
   the caller must be signed in; `Errors` lists what the route may return.
   `httpapi/route` checks the credential before the handler runs.
2. **Handler.** `classHandlers.create` in the same file decodes the body into
   `createClassRequest` and calls the application with an
   `academic.CreateClassCommand`.
3. **Use case.** `Service.CreateClass` in `app/academic/class.go` authorizes
   `model.ActionClassManage` on the Programme Level, builds a `model.Class`,
   calls its `Validate`, and runs the write through `access.RunAudited`
   (`app/access/mutation_attempt.go`), which records the audit attempt first.
4. **Store contract.** `ClassStore.Create` in `store/academic.go` takes a
   `store.ClassCreation`: the Class and its audit reference.
5. **SQL.** `SQLClassStore.Create` in `store/sqlstore/class_store.go` inserts
   the row and completes the audit event in one transaction.
6. **Back out.** A Store error becomes an application error through
   `classStoreErrors`; `httpapi/route` turns its code into a Problem Details
   response using `httpapi/route/error_catalog.yaml`.

Creating a Class publishes nothing. A use case that does, such as
`CreateAcademicUnit` in `app/academic/academic_unit_commands.go`, calls a
helper in the area's `effects.go` after the Store returns, never before. The
[architecture guide](../docs/public/developers/server/architecture-composition.mdx)
has the full version and a second trace through Submission.

## Add a feature

A typical change touches these places, in this order:

1. **Model**: `model/<entity>.go` for the type and its `Validate`. A new ID
   type goes in `model/id.go` and `model/internal/idgen/catalog.go`.
2. **Store contract and SQL**: the method and its input type in
   `store/<topic>.go`, the implementation in
   `store/sqlstore/<model>_store.go`, schema in
   `migrations/postgres/000001_baseline.up.sql`. The `store` package comment
   explains the naming.
3. **Regenerate**: `make -C server generate`.
4. **Use case**: a method on the area's `Service` in `app/<area>/<entity>.go`.
   A new Action is a `model.Action` constant with its definition in
   `model/authorization.go`. A read calls `Authorize` (which records the
   decision) and then the Store; a change also runs through
   `access.RunAudited`, which audits it. New error codes go in
   `httpapi/route/error_catalog.yaml`.
5. **Route and OpenAPI**: a `route.Endpoint{...}` in `httpapi/<resource>.go`
   (a new resource is also listed in `httpapi/resources.go`). Each resource
   declares the application methods it calls in a small interface at the
   bottom of its file (`ClassApplication` in `httpapi/class.go`); add the new
   method there, and to the fakes its handler tests pass in. The OpenAPI path
   goes in `openapi/fragments/<area>/`, where `<area>` is the operation's one
   tag from `openapi/base.yaml` in kebab case (`Academic structure` is
   `academic-structure/`), in the file named for the resource in its route
   (`openapi/README.md` has the rule). Then `make -C server openapi-build`.
6. **Tests**: `app/<area>/<entity>_test.go`, `httpapi/<resource>_test.go` and
   the contract's suite in `store/storetest/`. Run `make -C server check`.

The [server workflow](../docs/public/developers/server-workflow.mdx) explains
which layer owns which decision and links a complete worked example.

## Where the rules live

[`AGENTS.md`](AGENTS.md) has the conventions table: one answer per recurring
question, and what enforces it: a test, a linter rule, or review. The tests in
`architecture/` protect package boundaries, security properties and published
contracts; style is left to the linter. [`httpapi/CONTRACT.md`](httpapi/CONTRACT.md) is the HTTP
contract, [`openapi/README.md`](openapi/README.md) the OpenAPI authoring
workflow, [`cluster/GUARANTEES.md`](cluster/GUARANTEES.md) what cluster
delivery promises, and [`templates/README.md`](templates/README.md) the mail
template workflow. Installing, configuring and operating a server is covered
by the [operator guide](../docs/public/operator/index.mdx) and the
[configuration reference](../docs/public/reference/configuration.mdx).
