# Server dependency reference

## Dependency direction

~~~text
internal/canonicaljson ← model ← {config, store}
store ← app/job
{model, store, logging, app/access, app/apperror, app/invocation} ← app/job ← {app, app/identity, app/jobs, httpapi, server}
{model, store} ← app/apperror ← {app, app/idempotency, app/exam, httpapi, websocket}
model ← app/invocation ← {app, app/idempotency, app/exam}
{model, store, app/apperror, app/invocation} ← app/access ← {app, app/academic, app/user, app/identity, app/administration, app/onboarding, app/exam, app/job, app/mail, app/search}
{store, app/apperror} ← app/paging ← {app, app/academic, app/user, app/identity, app/administration, app/onboarding, app/mail, app/search, httpapi}
{model, store} ← app/internal/batchitem ← {app, app/academic, app/identity, app/administration, app/onboarding}
{model, store, app/access, app/paging, app/apperror, app/invocation, app/idempotency, app/mail, app/realtime, app/internal/batchitem} ← app/academic ← {app, httpapi}
{model, store, app/access, app/paging, app/apperror, app/invocation, app/idempotency, app/realtime} ← app/user ← {app, httpapi, filecontent}
{model, store, secretseal, app/access, app/paging, app/apperror, app/invocation, app/idempotency, app/internal/batchitem, app/job, app/jobs, app/mail, app/realtime, app/user, app/exam/attempt} ← app/identity ← {app, httpapi, server}
{model, store, app/access, app/paging, app/apperror, app/invocation, app/idempotency, app/identity, app/internal/batchitem, app/jobs, app/mail, app/realtime} ← app/administration ← {app, httpapi, server}
{model, store, app/academic, app/access, app/administration, app/paging, app/apperror, app/invocation, app/idempotency, app/identity, app/internal/batchitem, app/jobs, app/mail, app/realtime, app/user} ← app/onboarding ← {app, httpapi, server}
{model, store, app/apperror, app/invocation} ← app/idempotency ← {app, app/exam}
{model, store, secretseal, app/invocation, app/job, app/mail, app/user, app/exam, app/exam/attempt, app/exam/sitting} ← app/jobs ← {app, app/identity, app/administration, app/onboarding}
{model, app/apperror} ← app/realtime ← {app, app/academic, app/user, app/identity, app/administration, app/onboarding, app/exam, websocket}
{model, store} ← app/exam ← app
{model, store, secretseal, app/exam, localization, app/access, app/apperror, app/invocation, app/paging, app/job} ← app/mail ← {app, app/academic, app/identity, app/administration, app/onboarding, app/jobs, app/exam/sitting, app/exam/attempt, app/exam/review, httpapi, server}
{model, store, app/access, app/apperror, app/invocation, app/paging, app/exam, app/user} ← app/search ← {app, httpapi}
localization ← {httpapi, websocket}
internal/weborigin ← {httpapi, websocket}
app/exam/safemarkdown ← {app/exam/attempt, app/exam/review}
app/exam/manageraccess ← {app/exam, app/exam/resource, app/exam/workspace, app/exam/correction, app/exam/sitting}
secretseal ← app
{model, localization, app/mail} ← cmd/mailpreview
{localization, app/mail, cmd/proctor/commands, httpapi, websocket} ← cmd/ptool
{internal/openapidoc, internal/errorcatalog} ← cmd/ptool
cmd/ptool/devseed ← cmd/ptool/commands
logging ← {platform, app, app/job, app/realtime, app/identity, httpapi, websocket}
{config, internal/tlscert, store/timerlayer, store/localcachelayer, platform, cluster, packages/vfs, packages/mail} ← metrics ← server
config ← tracing ← server
app ← httpapi
{model, logging, localization, internal/weborigin, app/apperror, app/invocation, app/paging} ← httpapi/route ← {httpapi, server}
webui ← server
model ← filecontent
packages/vfs ← filecontent
app ← filecontent
{app, httpapi, app/realtime, websocket, filecontent} ← server ← cmd/proctor/commands ← cmd/proctor
{config, model, store, store/sqlstore} ← operator ← cmd/proctor/commands
{model, internal/canonicaljson} ← desktoprelease ← server
internal/autocert ← server
internal/tlscert ← {server, metrics}
~~~

Infrastructure adapters sit to the side and point inward at their contracts. The root `server` package imports the components needed to assemble the graph.

`platform` never imports `metrics`: the measuring wrappers decorate the
platform ports, so that edge points one way.

`operator` holds the offline host commands that read or migrate an
installation without starting it (`config validate` and `show`, `migrate`,
`jobs`, `cluster status`). It opens the configuration and the database itself
and builds no node, so it may not import the module root, the application, or
a transport. A host command that needs the application (administrator recovery,
administrator MFA reset) stays in the module root and goes through
`server.New`; moving it would give the CLI a second composition path.

The physical layout reflects that graph: `httpapi` sits beside `app` because it
is an outer adapter depending inward on application capabilities. Its child
`httpapi/route` is the routing kernel the resources in `httpapi` are written
against: it knows no API area, so it may not import the parent `app`, an area
package, `store`, or `httpapi` itself. The module root names the kernel's
ports and its `API` type directly when it composes the transport;
`websocket`, `webui` and `cmd/ptool` do not import it. Packages inside `app/`
are application-owned modules, not transports.

| Package | Allowed production dependencies | Forbidden examples |
| --- | --- | --- |
| `model` | Standard library, `internal/canonicaljson`, and narrowly justified domain libraries | `app`, HTTP, SQL, cluster, WebSocket, and any transport DTO or adapter-only persisted spelling |
| `internal/canonicaljson` | Standard-library bounded JSON validation and encoding | Domain models, schema ownership, application, persistence, transports, infrastructure |
| `config` | Standard library, `model` (for the grammars configuration shares with durable policy, such as provider identifiers), and narrowly scoped IDNA hostname validation | Application, persistence, transports |
| `store` | `model` | `sqlstore`, HTTP, application services |
| `store/sqlstore` | `model`, `store`, `config`, `migrations`, `internal/canonicaljson`, SQL libraries | Application services, transports, runtime composition |
| `app/job` | `model`, `store.JobStore`, `logging`, standard library; for the operator Job service (`job.Operations`), `app/access`, `app/apperror`, `app/invocation` and the Store contracts it reads names and titles from | parent `app`, area packages, concrete Jobs, `app/mail`, transports, concrete adapters |
| `app/apperror` | `store` error contracts, the `model` validation error, standard library | SQL, HTTP status, localization, transports, concrete adapters |
| `app/invocation` | `model`, standard library | authorization or audit policy, `store`, parent `app`, Exam modules, transports, concrete adapters |
| `app/idempotency` | `model`, `store`, `app/apperror`, `app/invocation`, standard library | business operation policy, parent `app`, Exam modules, transports, concrete adapters |
| `app/access` | `model`, `store`, `app/apperror`, `app/invocation`, standard library | parent `app`, area packages, `app/exam`, transports, platform, concrete adapters |
| `app/paging` | `store`, `app/apperror`, standard library | `model`, parent `app`, area packages, transports, concrete adapters |
| `app/internal/batchitem` | `model`, `store`, standard library | everything else; only `app`, `app/academic`, `app/identity`, `app/administration` and `app/onboarding` may import it, so no transport can build a batch context |
| `app/academic` | `model`, `store`, `app/access`, `app/paging`, `app/apperror`, `app/invocation`, `app/idempotency`, `app/mail`, `app/realtime`, `app/internal/batchitem`, standard library | parent `app`, `app/exam`, `app/job`, transports, platform, concrete adapters |
| `app/jobs` | `model`, bounded `store` contracts, `app/invocation`, `app/job`, leaf `app/mail` capabilities, the `app/user`, `app/exam`, `app/exam/attempt` and `app/exam/sitting` services its handlers hold, `secretseal`, standard library | parent `app`, `store.Catalog`, transports, platform, concrete adapters |
| `app/realtime` | `model`, `logging`, `app/apperror`, standard library, consumer-owned ports (the Session-revocation Attempt lookup is one; the parent adapts the Store to it) | `store` and `store/sqlstore`, parent `app`, area packages, HTTP, WebSocket libraries, cluster adapters |
| `app/exam` | `model`, bounded `store` contracts, standard library, consumer-owned ports, `app/apperror`, `app/invocation`, `app/idempotency`, `app/access` for the `access.Mutation` an audit attempt begins with, `app/realtime` for the Publisher and events of the seven use-case packages (`app/exam` and its `attempt`, `correction`, `resource`, `review`, `sitting` and `workspace` children), `app/mail` for the three children that prepare the mail they send (`sitting`, `attempt`, `review`), and explicitly shared leaf packages such as `app/exam/safemarkdown` | parent `app`, a sibling use-case package (`attempt` and `review` reach the Sitting service through a port), `app/mail` from `app/exam` itself or the other children, transports, platform, concrete adapters |
| `app/exam/safemarkdown` | Standard library | model, store, parent `app`, transports, concrete adapters |
| `app/exam/manageraccess` | `model`, bounded Store access projection, narrow membership port, standard library | parent `app` or `app/exam`, sibling use cases, transports, concrete adapters |
| `app/mail` | `model`, bounded `store` mail records, `secretseal`, `localization`, the Exam Manager preparation contract, standard-library templating, and consumer-owned sending ports; for the operator mail service (`mail.Operations`), `app/access`, `app/apperror`, `app/invocation`, and `app/paging` | parent `app`, area packages (the identity test-mail rate limit is a port), `app/job`, `app/jobs`, transports, platform, SQL, configuration, concrete adapters |
| `app/search` | `model`, `store`, `app/access`, `app/apperror`, `app/invocation`, `app/paging`, and the `app/exam` and `app/user` services it searches through, standard library | parent `app`, other area packages, `app/job`, `app/mail`, transports, platform, concrete adapters |
| `secretseal` | Standard library cryptography and encoding | model, persistence, configuration, transports, concrete adapters |
| `localization` | Standard library, caller-supplied catalog filesystems, and the CLDR-aware localization engine | application, domain, persistence, transports, concrete adapters |
| `logging` | Standard library and the hidden logging engine/target implementation | domain, persistence, global logger state, and any import of application or transport packages |
| `metrics` | `config`, `internal/tlscert`, narrow store recorder contracts, the ports its measuring wrappers decorate (the `platform` Cache and Mailer, the `cluster` transport, and the `packages/vfs` and `packages/mail` contracts), standard library, and Prometheus client libraries | application policy (a recorder that needs an application type is an adapter in the module root), platform service location, concrete infrastructure adapters, global Prometheus registry |
| `tracing` | `config`, standard library, and OpenTelemetry API, SDK, and OTLP/HTTP exporter libraries | application policy, transports, store contracts, platform service location, a process-global tracer provider |
| `app/user` | `model`, `store`, `app/access`, `app/paging`, `app/apperror`, `app/invocation`, `app/idempotency`, `app/realtime`, standard library | parent `app`, `app/exam`, `app/job`, `app/jobs`, `app/mail`, transports, platform, concrete adapters |
| `app/identity` | `model`, `store`, `secretseal`, `logging`, `app/access`, `app/paging`, `app/apperror`, `app/invocation`, `app/idempotency`, `app/internal/batchitem`, `app/job`, `app/jobs`, `app/mail`, `app/realtime`, `app/user`, the `app/exam/attempt` native-build contract, `golang.org/x/crypto/argon2`, standard library | parent `app`, `app/academic`, other `app/exam` modules, transports, platform, concrete adapters |
| `app/administration` | `model`, `store`, `app/access`, `app/paging`, `app/apperror`, `app/invocation`, `app/idempotency`, `app/identity`, `app/internal/batchitem`, `app/jobs`, `app/mail`, `app/realtime`, standard library | parent `app`, `app/academic`, `app/exam`, `app/job`, transports, platform, concrete adapters |
| `app/onboarding` | `model`, `store`, `app/academic`, `app/access`, `app/administration`, `app/paging`, `app/apperror`, `app/invocation`, `app/idempotency`, `app/identity`, `app/internal/batchitem`, `app/jobs`, `app/mail`, `app/realtime`, `app/user`, standard library | parent `app`, `app/exam`, `app/job`, transports, platform, concrete adapters |
| `app` | `model`, `store`, `logging`, `app/access`, `app/paging`, `app/academic`, `app/user`, `app/identity`, `app/administration`, `app/onboarding`, `app/apperror`, `app/invocation`, `app/idempotency`, `app/job`, `app/jobs`, `app/realtime`, `app/exam`, `app/mail`, `app/search`, consumer-owned ports | `platform`, `httpapi`, `sqlstore` |
| `filecontent` | `model`, `app/user` content contracts, `app/exam` export archive types, `packages/vfs`, narrowly allowlisted content codecs | persistence, transports, platform service location, Jobs, configuration, concrete VFS backends |
| `httpapi/route` | `model`, `logging`, `localization`, `internal/weborigin`, the application leaves `app/apperror`, `app/invocation` and `app/paging`, HTTP libraries; a DPoP nonce challenge and delivery recovery state are read through small interfaces on the error, and which codes advise `Retry-After` or carry an extension member comes from the generated error catalog | parent `app`, every area package and Exam child (`app/identity` and `app/exam/attempt` included), `store`, the `httpapi` resources, `websocket`, `platform` |
| `httpapi` | `httpapi/route`, `app`, `logging`, `app/academic`, `app/administration`, `app/onboarding`, `app/user`, `app/identity`, `app/paging`, `app/apperror`, `app/invocation`, `app/job`, `app/mail` and `app/search` for the command, query and result types their services declare, `app/realtime`, `app/exam` and its children, `store` result types, `model`, `localization`, `internal/weborigin`, HTTP libraries | Store contracts and functions, `sqlstore`, `platform` |
| `webui` | Standard-library HTTP and filesystem contracts | Application, domain, persistence, configuration, concrete filesystems, third-party libraries |
| `websocket` | the `httpapi` allowance without `httpapi/route`, with WebSocket libraries in place of HTTP libraries | Store contracts and functions, the HTTP routing kernel, SQL and platform service location |
| concrete adapters | Their inward contracts and implementation libraries | Application policy |
| `desktoprelease` | `model`, `internal/canonicaljson`, standard-library signature and artifact validation | persistence, transports, application eligibility, process/filesystem selection |
| `internal/weborigin` | Standard-library URL parsing | Domain models, application, persistence, transports, configuration |
| `internal/autocert` | Standard library, `x/crypto/acme`, and `x/net/idna` | Product policy, application, persistence, transports, unrelated third-party libraries |
| `internal/tlscert` | Standard library | Configuration, product policy, application, persistence, transports, third-party libraries |
| `internal/openapidoc` | Caller-supplied filesystems, OpenAPI/YAML parsing and validation, standard-library encoding | Process filesystem selection, runtime policy, application, persistence, transports |
| `internal/errorcatalog` | Caller-supplied catalog bytes and minted-code inventories, YAML parsing, standard-library rendering | Process filesystem and toolchain access, application, persistence, transports |
| `server` | Construction dependencies | Business rules |
| `cmd/proctor-healthcheck` | Standard-library HTTP readiness probing | Server packages, application state, configuration, persistence, and third-party libraries |
| `cmd/proctor` | `cmd/proctor/commands` and standard-library process lifecycle | Server, application, persistence, and concrete infrastructure |
| `operator` | `config`, `store/sqlstore` (the read-only store and the migrator), and the `model` and `store` vocabulary it reports; standard library | the module-root `server`, `app` and its children, transports, `platform`, `database/sql` and SQL drivers, third-party libraries |
| `cmd/proctor/commands` | Module-root `server`, `operator`, `localization`, Cobra, `golang.org/x/sys/unix` for the systemd notification clock, and standard-library presentation concerns | Application packages, persistence, platform, and independent infrastructure construction |
| `cmd/mailpreview` | `model`, `localization`, `app/mail`, standard library, and repository source assets | parent application, persistence, infrastructure adapters, mail delivery |
| `cmd/ptool` | `localization` plus the consumer-owned localization definition registries in `app/mail`, `cmd/proctor/commands`, `httpapi`, and `websocket`; `internal/openapidoc` and `internal/errorcatalog` behind the local-filesystem build/check adapters, which also type-check production packages to inventory minted error codes | runtime composition and concrete infrastructure construction |
| `cmd/ptool/devseed` | Standard-library HTTP, synthetic fixture construction, and private local journal I/O | Application packages, persistence, runtime composition, concrete server adapters |

Tests and `testlib` may cross production boundaries for verification; the
test-support packages `store/storetest`, `config/configtest`,
`app/realtime/realtimetest` and `httpapi/route/routetest` are imported only
by tests. Ordered,
declarative package rules enforce the production allowlist with no debt or
waiver mechanism.

The shared idempotency leaf has an exact inward dependency allowance of
`model`, `store`, `app/apperror`, and `app/invocation`. Within the Exam boundary its production consumers are
limited to `app/exam`, `app/exam/attempt`, `app/exam/correction`,
`app/exam/resource`, `app/exam/review`, `app/exam/sitting`, and
`app/exam/workspace`; those command owners retain operation names and semantic
fingerprints rather than moving policy into the leaf.

The application error leaf `app/apperror` owns the single transport-neutral
`Error` type, its `Code` vocabulary, the declarative `StoreTable` that is
the only translation of typed Store failures into application errors, and the
declarative `Concealment` through which a protected use case presents its
failures. It sits
below the parent application
so child modules report the same error type without importing their parent;
transports import it only to read codes and safe fields.

The invocation leaf `app/invocation` owns the one immutable call context and
the actorless Job `System` identity. It sits below the parent application so
the parent, the idempotency leaf, and every Exam child share one type; it
depends only on `model`. Transports import it directly to build an
`invocation.Invocation` with `invocation.New`.

Every type and constant has one name. Transports name a use case's command,
query, and result types in the package that declares them (`attempt.ConnectCommand`,
`exam.View`, `job.Page`, `store.ExamSummary`); no application package
re-exports another package's type or constant under a second name. A transport may name `store` result types, but never
a Store contract, function, or variable, so persistence stays out of reach
(`TestTransportsNameOnlyStoreResultTypes`). Review rejects a re-exported name.

Each package is imported under the name it declares (`job`, not
`jobengine`). An import alias exists only where the same file imports two
packages that declare one name, such as `packages/mail` beside `app/mail`,
and each such package has one alias, listed under `importas` in
`.golangci.yml` (`mailpkg`, `appmail`). A local variable that would shadow a
package name is renamed instead.

The shared Exam Manager eligibility leaf is consumed only by `app/exam`,
`app/exam/resource`, `app/exam/workspace`, `app/exam/correction`, and
`app/exam/sitting`. It selects between caller-owned ordinary and override
Actions using the current Manager relationship and exact Academic Unit
membership. The use-case owners retain projection validation, authoritative
authorization against their precise Resource, error presentation, audit,
and named transaction intent.

## Reusable capability boundaries

The reusable modules own portable infrastructure behavior; the server owns
product meaning and policy:

- `packages/vfs` owns backend-neutral file operations. The server owns semantic
  metadata, authorization, retention, and content policy. The server-owned
  `filecontent` module concentrates private semantic keys, bounded content
  processing, immutable rendition mechanics, exact reads, and physical purge
  over that reusable contract. Local VFS is for development or genuinely
  shared single-node storage; clustered production uses shared storage.
- `packages/cache` owns memory/Redis semantics, codecs, TTLs, conditional
  writes, and counters. The server owns namespaces, cache eligibility,
  invalidation, and security staleness. Cache is never durable state, an
  implicit message bus, or an unconstrained lock.
- `packages/mail` owns transport-neutral messages, MIME composition,
  validation, SMTP, and sender conformance. The server owns templates,
  localization, recipients, rate limits, retries, and durable delivery policy.
  The `app/mail` child module concentrates server-owned composition,
  the complete definition registry, semantic preparation operations,
  suppression, shared payload freezing/encryption, stable routing metadata,
  closed typed rendering, and family-specific meaning. The area that sends a
  mail prepares it with the child package's contracts and persists the result
  through a named aggregate transaction. Mail
  is not sent synchronously inside a durable business transaction.
- `secretseal` owns versioned AES-256-GCM envelopes, bounded key rings,
  authenticated purpose/owner binding, and safe cryptographic failures for
  recoverable server application secrets. It has no persistence or
  configuration dependency and is used directly as an in-process module, not
  hidden behind a replaceable cryptography port.


Identity, authorization, examinations, WebSockets, clustering, and MFA
remain server concerns until they have coherent Proctor-independent
contracts, plausible external consumers, and their own compatibility
policies.

## Rationale

- Conceptual transport/application/domain/persistence boundaries preserve
  cohesive packages without imposing a mechanical layer tree.
- Consumer-owned interfaces make each dependency narrow; the bounded
  `store.Store` family is grouped deliberately for shared conformance testing.
- Import tests make inward dependency direction enforceable instead of relying
  on prose or review memory.
