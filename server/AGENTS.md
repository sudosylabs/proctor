# Server agent guide

This file applies to the `server` module. Root [`AGENTS.md`](../AGENTS.md)
remains authoritative for repository-wide rules.

## Scope

The server owns Proctor-specific domain models, application use cases,
authorization, identity, transports, persistence, clustering, configuration,
and runtime composition. Reusable cache, mail, and VFS behavior belongs in the
independent modules under `packages/`.

Before changing the server, load the matching repository skill and any exact
component contract:

- HTTP routes, DTOs, errors, or OpenAPI:
  [`httpapi/CONTRACT.md`](httpapi/CONTRACT.md)
- cluster delivery or recovery: [`cluster/GUARANTEES.md`](cluster/GUARANTEES.md)

## Local rules

- Preserve the inward production graph documented by the
  [`server-boundaries` skill](../.agents/skills/server-boundaries/SKILL.md),
  including the selective `app/job`, `app/mail`, `app/search` and
  `app/realtime` child modules. Child modules never import their parent
  package or concrete transports.
- Keep concrete adapter selection in module-root package `server`.
- Keep `platform.Service` out of application services and use narrow
  consumer-owned ports; store contracts are the deliberate grouped exception.
- Keep ordinary `go test ./...` network-free. External services use the
  `integration` build tag and dedicated Make targets.
- Use the real `server.New` graph through `testlib` for wiring confidence; do
  not create a second composition path.
- Preserve the applicable file-level license header and upstream notices
  described by [`LICENSING.md`](../LICENSING.md), and record exact upstream
  provenance in [`NOTICE`](NOTICE) when adapting source.

## Conventions

Each row is the one answer for its question. Follow it in new and changed
code; the linked authority holds the detail, and the named check enforces it.

| Question | Answer | Authority / check |
| --- | --- | --- |
| Application error type | One `*apperror.Error`, built in application code with `apperror.New(code)` or `apperror.Invalid(code, field)`; Store failures become codes only through a declared `apperror.StoreTable`. Transports mint only kernel codes. | [http-api](../.agents/skills/http-api/SKILL.md); `make -C server errors-check` |
| Route error tables | A route declares exactly what its operation can return: nothing unreachable, and every code whose error certainly flows to the return. Concealed codes (denial, `Concealed`) become `resource.not_found` and are not declared. | [`httpapi/CONTRACT.md`](httpapi/CONTRACT.md); httpapi `TestRouteErrorTablesMatchReachableCodes`, architecture `TestRouteErrorTableCheckRejectsPlantedViolations` |
| Error code spelling and status | `<domain>[.<subdomain>].<condition>`, lowercase dot-separated `snake_case`, listed once in `httpapi/error_catalog.yaml`; the condition word decides the HTTP status and `error_mapping.go` is generated. | [`httpapi/CONTRACT.md`](httpapi/CONTRACT.md); `errors-check` |
| Entity identifiers | A typed model ID (`model.UserID`, `[]model.SessionID`, …) from the point it is parsed, in every `store` and `app` (child packages included) struct field, exported function and method parameter, and port parameter named `*ID`/`*IDs`; path IDs are read by template name through the generic `httpapi` `pathID` helper, instantiated with the model ID type. A plain `string` checked with `model.IsValidOpaqueID` only for references whose entity type is chosen at runtime (Role scope, row target, retention record), runtime tokens (node, lease) and identifiers another party defines (identity providers, Desktop agreements and builds, mail keys), each listed with its reason. | [persistence](../.agents/skills/persistence/SKILL.md); architecture `TestEntityIdentifiersAreTyped` |
| Wire timestamps | RFC 3339 strings formatted by `model.FormatRFC3339`, in bodies and query parameters; absent optional instants are omitted, never `null`, in requests and responses alike. Lifecycle members are `<verb>ed_at` (`created_at`, `updated_at`, `archived_at`); scheduled bounds keep `start_at`, `end_at`, `expires_at`. | [`httpapi/CONTRACT.md`](httpapi/CONTRACT.md); OpenAPI agreement tests, `TestOpenAPIOptionalInstantsAreOmittedNotNull`, architecture wire-name test (pointer instants need `omitempty`) |
| Create request members | Never server-owned `id` or timestamps, nor parent identifiers the route path already fixes; strict decoding rejects them. | [`httpapi/CONTRACT.md`](httpapi/CONTRACT.md) |
| Use case invocation and idempotency | Every use case takes `(ctx, invocation.Invocation, ...)`, anonymous transport calls included (a zero Principal plus request metadata); actorless work takes `(ctx, invocation.System, ...)`, naming its Job execution (`invocation.Job`) or runtime task (`invocation.Task`: periodic scans, startup reconciliation, offline host-operator commands). Exported App methods that are not use cases are listed with a reason. Command idempotency is prepared only by `app/idempotency` `Require` or `Prepare`. | [server-boundaries](../.agents/skills/server-boundaries/SKILL.md); architecture `TestUseCasesTakeAnInvocationFirst` |
| Service construction | A service is a struct holding its collaborators: the `store` contracts it calls, the area's `Authorizer` and `Auditor` (each declared once per area package), one `realtime.Publisher` for post-commit effects, one `Now` and one `NewID`. Its constructor takes one `Dependencies` (or `<X>Dependencies`) struct declared beside it, never positional collaborators. | server-boundaries (application reference); `TestServicesTakeOneDependencies` |
| One name per type | A type or constant is named in the package that declares it. No application package (app or a child) aliases another package's type or restates its constant; transports name `attempt.ConnectCommand`, `job.Page`, `invocation.Invocation`, `store.ExamSummary` directly. Transports may name `store` result types but never a Store contract, function, or variable. | server-boundaries; `TestApplicationDeclaresNoForwardingNames`, `TestTransportsNameOnlyStoreResultTypes` |
| Application surface | Each use case is declared once, as an exported method on its area service; `app.App` exposes area services as named fields (`app.Academic`) and declares no use case of its own; it embeds nothing. The parent package only assembles: the services that span areas live in child packages and are named fields too (`app.Search` is `*search.Service`, `app.Counts` `*administration.Counts`, `app.Jobs` `*job.Operations`, `app.Mail` `*mail.Operations`). | server-boundaries; `TestApplicationSurfaceIsNamedServices` |
| Interfaces | Only where something substitutes behind it: a second implementation, a fake the consumer's tests pass in, a capability probe, or a port across a module boundary; declared once per area package. Never a restatement of a `store` contract: hold the store contract and fake it in tests by embedding it. Otherwise hold the concrete type. | server-boundaries |
| Adapters | Only where two packages disagree on a type and neither may import the other; one adapter per seam, never wrapping another. Application code and the transports log through `server/logging` directly (`*logging.Logger`) rather than through a logging port an adapter implements; they only write log records, and the logger's lifecycle (`Configure`, `LockConfiguration`/`UnlockConfiguration`, `Flush`, `Shutdown`) belongs to Platform and the module root. | server-boundaries; `TestAdaptersDoNotWrapAdapters` |
| Authorization ports | One shape, `Authorize(ctx, Invocation, Action, Resource) error`, plus named helpers `Authorize<Concept>(ctx, Invocation, ...)` that return `error`, or `(R, error)` with `R` a named struct saying what they report (`access.ManagerAuthorization{Override}`, `store.UserVisibilityScope`); a non-audited predicate is `Can<Concept>` returning `(bool, error)`. A helper forwarding to another keeps its name: one concept, one name (`AuthorizeSittingView`, not `AuthorizeView` beside it). | [authorization-audit](../.agents/skills/authorization-audit/SKILL.md); architecture `TestAuthorizationHelpersShareOneShape` |
| Commands and queries | A mutation takes an `XCommand` by value, even with one field. A read that needs only typed IDs (up to three) takes them as parameters: `GetClass(ctx, invocation, id)`. A read with paging, filters or other inputs takes an `XQuery`. No input type is empty, and none is an alias: use cases that share an input shape share one struct named for what it carries (`sitting.ManagerTransitionCommand`). Use cases return a model or `XResult`. Store named operations take one pointer input named for entity and operation (`ClassUpdate`); the fence on the input's own entity is `ExpectedRevision` and a fence on another resource is `Expected<Resource>Revision`; idempotency is a trailing `*store.CommandIdempotency`. | persistence; architecture `TestStoreRevisionFencesNameTheirResource`, `TestStoreMutationsTakeOneInput` |
| Audit | A security-sensitive mutation has one Store operation that requires its audit input, using the attempt protocol (`AuditEventID` + `AuditAt`, completed in the same transaction) or the draft protocol (`AuditEvent`, inserted once). Action names exist only as `model.Action` constants. | authorization-audit; action catalog tests |
| Internal versions and operation names | No version field, `.v1`/`V1`/`_v1` suffix, or legacy reader; a pre-release format changes outright. Only externally negotiated contracts (`/api/v1`, Desktop and cluster peer protocol ranges, Desktop native agreement keys) carry versions. Command operations are `<domain>[.<subject>].<verb>` snake_case; domain tags are `proctor.<topic>`. | persistence; architecture `TestNamesCarryNoVersionSuffix` |
| Dead code | Every production function is reachable from a server binary; a seam only its own package's tests use lives in a `_test.go` file, and a cross-package test seam needs a reasoned allowlist entry. | server-boundaries; architecture `TestProductionFunctionsAreReachable` |
| Store instants | Every instant at the Store boundary (struct fields and interface parameters such as `AuditAt`, `ChangedAt`, `EndAt`, `at`, `now`) is a UTC microsecond `time.Time`, or `model.OptionalTime` when absence is meaningful; never Unix milliseconds or another integer. Milliseconds stay only in application commands and model documents that define them, converted at that edge. | persistence; architecture `TestStoreInstantsAreTimes` |
| Time authority | PostgreSQL time decides validity, expiry, deadlines and leases; a mutation never records an instant earlier than the lifecycle instants it supersedes on the row (`GREATEST`). | persistence ("Time authority") |
| Where types live | `model` holds domain types; it carries json tags only for a canonical serialization it owns (digest-bound documents, delivery agreements and the results a transport returns verbatim, Job payloads). DTOs live in `httpapi`/`websocket`, persisted spellings in `sqlstore`, query-shaped results in `store` (which carries no json tags). | persistence; server-boundaries |
| Persisted outcomes | Every command outcome is a dedicated `sqlstore` document (it implements `outcomeDocument`) with an explicit json name on every field, nesting only other such documents or fully tagged `model` documents, and is encoded and decoded only through `encodeOutcome`/`decodeOutcome`; decoding is strict, so a stale format fails closed instead of replaying. No `model` or `store` type is persisted as an outcome directly. | persistence ("Idempotent command outcomes"); architecture `TestPersistedOutcomesAreTaggedDocuments` |
| Model failures | A package sentinel matched with `errors.Is`, or a `*ValidationError` from `invalidModelError`. Closed vocabularies answer `IsValid() bool`; structured values answer `Validate() error`. | `model` package doc |
| File layout | `store/store.go` holds only `Catalog` and `Store`; each contract lives in its topic file. `config/config.go` holds the root; each section has its own file. A file is named for the singular concept it holds. | architecture naming test |
| Import names | A repository package is imported under its own package name (`job`, `mail`, `realtime`, `exam`, `idempotency`), never an alias, unless the same file imports another package that declares the same name. A local that would shadow the package name is renamed instead (`record` for a `*model.Job`). | architecture `TestInternalImportsUseTheirPackageName` |
| Naming | One 1-3 letter receiver per type; Go identifiers spell `ID`; Store implementations are exported `SQL<Model>Store`; `app/job` is the Job engine and the operator Job service (`job.Operations`), `app/jobhandler` the handlers; `app/mail` is mail composition and the operator mail service (`mail.Operations`). A type's name does not repeat its package (`mail.DeliveryView`, `search.Query`, `job.CancelCommand`). | architecture naming test |
| SQL idiom | `?` placeholders (`$N` only for positional reuse); `errors.Is(err, sql.ErrNoRows)`; advisory locks only through `lockAdvisory` on an `advisoryLockKey`. | persistence ("SQL idiom") |
| Tests | Assert through testify `require` (`assert` only where continuing is useful or off the test goroutine), one assertion per property, expected value first, called directly; run case tables (struct cases with a `name`-like field, or maps keyed by case name) as named `t.Run` subtests; assert outcomes; a fake's recorded call log (`events`, `order`, `calls`) is asserted whole with `require.Equal` and a comment naming the ordering contract, never by membership or subsequence. | [`README.md`](README.md) "Write tests"; `architecture` test idiom test; `testifylint` |
| Exported documentation | Every exported identifier in `app`, `httpapi` and `store/sqlstore` has a doc comment that says something its name does not. Two kinds need none, because a comment could only restate them: a SQL Store method (the store contract documents it) and a `Command` or `Query` type (it is the input of the use case it is named for). | `.golangci.yml` revive `exported` (new and changed code) |
| Optimistic concurrency on the wire | A body member `expected_revision` (JSON or multipart metadata, `DELETE` included) for the changed resource; `expected_<resource>_revision` only beside it when one command fences several. No revision query parameter or header; `If-Match` only for a binary representation served with an `ETag`. | [http-api transport reference](../.agents/skills/http-api/references/transport.md); `TestOpenAPIRevisionFencesUseOneIdiom` |
| Cancel spelling | `canceled` and `canceling` (one l) in identifiers, SQL, persisted values, wire strings, templates, i18n keys and copy; the noun stays `cancellation`. | `TestCancellationIsSpelledWithOneL` (architecture) |
| Collections on the wire | `{"items": [...], "next_cursor": "..."}` (next_cursor omitted on the last page), paged only by `limit` (default 50, at most 200) and `cursor`; app use cases return `paging.Page[T]`. | [`httpapi/CONTRACT.md`](httpapi/CONTRACT.md) "Collections"; `TestOpenAPIListOperationsUseOneEnvelope` |
| Pagination cursors | Opaque, strictly decoded, and unversioned: one `opaqueCursorSpec` per listing whose members are exactly the cursor type's json members; a keyset change invalidates outstanding tokens. | [`httpapi/CONTRACT.md`](httpapi/CONTRACT.md); `httpapi` opaque cursor codec and `TestOpaqueCursorSpecRejectsVersionMember` |
| Wire DTO members | Snake_case json names on every exported member; instants are RFC 3339 `string` (or `time.Time`). | architecture wire-name test |
| Foreign keys | Every baseline foreign key is led by an index on its columns, or is listed with a reason. | `store/sqlstore` foreign-key index test |

## Verification

Use the narrowest target that covers the change. The hermetic full gate is:

```sh
make -C server check
```

Architecture and documentation changes use:

```sh
make -C server architecture
```

PostgreSQL, provider, Redis, and realtime integration targets are documented in
[`server/README.md`](README.md).
