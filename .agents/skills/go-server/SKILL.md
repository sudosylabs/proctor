---
name: go-server
description: Write or change Go code in the Proctor server. Use for application services, packages, interfaces, composition, authorization and audit, jobs, runtime and lifecycle, security-sensitive code, comments and tests.
---

# Writing Proctor's Go server

You are a senior Go engineer on a long-lived open-source server. Strangers
will read this code to decide whether to trust it with their exams. Write code
an experienced reviewer would call plain, obvious and careful.

## How to work

- **Read the area before you change it.** Read its package doc, its service
  file and one complete use case end to end, from handler to SQL.
- **Look for prior art.** When a problem is new to Proctor, look at how
  Mattermost (the upstream reference) or the Go standard library solves it.
  Copy the idea, not the shape.
- **Ask before deciding for the owner.** If a change needs a product or
  security decision the code doesn't already settle, ask. Don't pick one and
  bury it in the code.
- **Prefer the smallest change that fits.** A clever abstraction that saves
  ten lines today costs every future reader.

## House style

**Navigation comes first.** From an HTTP route, go-to-definition reaches the
code that runs, at every hop. Hold concrete types: handlers hold
`*user.Service`, and services hold `*access.Authorizer`. Earn an interface:
- it is a Store contract (there are real sql, cache, retry and timer layers);
- or two or more production types implement it;
- or it is a port to infrastructure the package must not import (file
  content, mail transport, cluster);
- or it is an optional capability checked with a type assertion.

Declare each interface once, in one obvious place. A test fake alone is never
a reason. The pilot commit `25781445` (the User directory) shows the target
shape.

**One home per feature.** A feature's code lives under one package tree. The
root packages hold composition only:
- `server` holds `server.New`, lifecycle and the HTTP runtime;
- `app` holds `app.New`.

Don't add glue files to either root. If two packages can't import each other,
fix the dependency direction or move the type the port exchanges.

**Packages follow ownership**, not symmetry, file size or another project's
taxonomy. A child never imports its parent.

**Files split by responsibility, not length.** A store file serving one model
can be long. A file doing three jobs should be three files.

**One owner per rule.**
- The handler parses syntax: types, cursors, query parameters.
- The service enforces meaning: authorization, limits, invariants.
- The store trusts typed input.

Don't validate the same rule in three layers.

**Comments say why.** A comment earns its place by telling the reader
something the code can't:
- a reason;
- an ordering guarantee;
- a security consequence;
- a trap.

Don't restate the code, don't narrate the design, and don't write
Capitalized Domain Terms as decoration. A package doc is a few sentences.

**Errors.** A failure is one `*apperror.Error` (`apperror.New` or
`apperror.Invalid`). Store errors become codes only through a declared
`apperror.StoreTable`. Exam modules conceal denials as `resource.not_found` so
existence doesn't leak.

**Use cases** take `(ctx, inv invocation.Invocation, ...)`. Actorless work
uses `invocation.Job` or `invocation.Task`, which cannot impersonate a User.
The ctx carries cancellation, never security state.

**Composition is manual**: no reflection containers, generated DI or globals.
`server.New` is the only composition root and the only place that picks
concrete infrastructure. `platform.Service` owns infrastructure lifecycle and
is never a service locator. Services never receive `config.Config`; give them
small immutable policies.

## Security rules that must hold

- **Authorization belongs to the use case.** Authentication gives an immutable
  Principal with no role snapshot. The owning use case authorizes the action
  on the resource right before the work. Results are never cached, and a
  resolution failure fails closed. Lists apply authorized scope in SQL with
  keyset pagination, never fetch-then-filter.
- **Authorization helper shapes:** `Authorize<X>` returns `error` or a named
  struct; `Can<X>` returns `(bool, error)` and records nothing. A
  `(bool, error)` checked only for its error fails open.
- **Actions** are `model.Action` constants. Never spell one as a string
  elsewhere.
- **Audit:**
  - A security-sensitive mutation begins an audit attempt, passes it to one
    named Store operation that completes it in the same transaction, and
    fails it on error.
  - Completion ignores request cancellation.
  - Audit fields never hold secrets or answers.
  - Rate-limited requests write no audit.
- **What never reaches logs, audit fields or metric labels:** credentials,
  secrets, student data, exam answers, workspace content and unbounded user
  input. Log an unexpected failure once, at the outer boundary.
- **Credential handling:**
  - Hash passwords before taking database locks.
  - Credential deadlines reject at the deadline itself; never round the
    instant.
  - Never link an external identity by email or username.
  - Credentials never travel in a URL query string.
  - Mixed bearer and cookie credentials are rejected, not resolved by
    precedence.
- **Commit before effects.** Durable state commits before cache invalidation,
  cluster fan-out, realtime or mail. A failed effect is reported through
  `Publisher.Report`, and the use case still succeeds. Peers never rebroadcast
  what they receive.

## Runtime facts that surprise people

- **Startup** runs Platform, then the serving lease, then reconciliation, then
  Jobs, WebSocket and HTTP, then readiness. **Shutdown** is one ordered path in
  `server/shutdown.go` under one timeout; don't add a second drain.
- **Readiness** probes are bounded and cheap, and never name the failing
  dependency publicly.
- **Config** is one operator-owned JSON file, overlaid by `PROCTOR_*`
  variables. `SIGHUP` reloads only `Log` and `Authentication.External`.
- **The database pool** needs at least 2 connections, because migration holds
  two.
- **Build environment:** an untagged binary is production; `dev` needs
  `-tags dev`.
- **Jobs** are finite, retryable work worth tracing. Heartbeats and reapers are
  runtime tasks, not Jobs. Execution is at least once, so handlers are
  idempotent. A worker gets only the capabilities it declares, never `App`
  or `store.Catalog`.
- **Plugins** are first-party and compiled in. They get no Store, no SQL, and
  never act on an Attempt.
- **Execution environment:** the Attempt Workspace is the only authority for
  candidate files, and Apply Workspace Batch is the only write path. Don't
  poll; owners wake on notifications. Terminal bytes never cross cluster
  delivery.

## Tests

- **Test behavior through real code.** Application tests run the real
  service and the real authorizer from `app/access/accesstest`, over Store
  fakes that embed the Store interface. An unimplemented call then panics and
  tells you what to add.
- **Handler tests** assert what the request became at the Store.
- **No tests of conventions:** no tests of skills, docs, spelling or test
  style, and no tests of tests. A convention worth enforcing goes to
  golangci-lint or review.
- **Run what you touched:** `go test` for the touched packages, `go vet`
  (also with `-tags integration`), and `../.build/bin/golangci-lint run` on
  those packages. When the API might move, run `go run ./cmd/ptool openapi
  check`.
- **Database tests** use `-tags integration`, with Postgres on port 25432.

## Good examples to read first

- `server/app/user/profile.go`, then `server/httpapi/user_profile.go`
  (`search`), then `server/store/sqlstore/user_store.go` (`List`)
- `server/app/user/settings.go`
- `server/app/new.go`
- `server/shutdown.go`
