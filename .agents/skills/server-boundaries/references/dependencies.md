# Server dependency reference

## Dependency direction

Dependencies point inward: transports and infrastructure adapters depend on
the application, the application on its area packages and shared leaves, and
everything on the `model` and `store` contracts.

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

## Dependency rules

`dependencyRules` in
[`server/architecture/import_policy_test.go`](../../../../server/architecture/import_policy_test.go)
is the one statement of which package may import which. The table below is
generated from it: `TestDependencyReferenceMatchesRules` fails when they
differ and prints the table to paste between the markers.

Rules are listed in precedence order and the first rule whose packages match
owns a production package; a package no rule matches fails closed. Paths are
relative to the server module, `server` is its root, and `packages/...` are the
reusable modules. An import a row does not allow is forbidden, and
`TestDependencyAllowancesAreUsed` removes an allowance once nothing the rule
owns imports it. The rule comments in `dependencyRules` say why an allowance
exists.

<!-- Generated from dependencyRules by TestDependencyReferenceMatchesRules. -->

*Infrastructure* is `net/http`, `database/sql` and below, `net/smtp`, `os`, `io/fs`, `path/filepath`.

| Rule | Packages | Server and repository imports | Third-party imports | Standard library denied |
| --- | --- | --- | --- | --- |
| reusable modules | `packages/cache` and below, `packages/mail` and below, `packages/vfs` and below | any except `server` and below | any | none |
| composition root | `server` | any | any | none |
| model build tools | `model/internal/idgen` and below | none | none | none |
| route error-code analysis for tests | `internal/errorflow` | none | `golang.org/x/tools/go` and below | none |
| static TLS key pair files | `internal/tlscert` | none | none | none |
| ACME adapter | `internal/autocert` | none | `golang.org/x/crypto/acme`, `golang.org/x/net/idna` | none |
| OpenAPI compiler | `internal/openapidoc` | none | `github.com/getkin/kin-openapi/openapi3`, `gopkg.in/yaml.v3` | infrastructure except `io/fs` |
| error catalog compiler | `internal/errorcatalog` | none | `gopkg.in/yaml.v3` | infrastructure except `net/http` |
| browser origin serialization | `internal/weborigin` | none | none | infrastructure |
| canonical agreement JSON | `internal/canonicaljson` | none | none | infrastructure |
| domain model | `model` | `internal/canonicaljson` | none | infrastructure |
| store contracts | `store` | any except below `server`, but `model` | none | infrastructure |
| SQL store adapter | `store/sqlstore` and below | `model`, `internal/canonicaljson`, `store`, `config`, `migrations` | any | none |
| local cache store layer | `store/localcachelayer` and below | `model`, `store`, `packages/cache` | none | infrastructure |
| generic store layers | `store/timerlayer` and below, `store/retrylayer` and below | `model`, `store` | none | infrastructure |
| concrete jobs | `app/jobs` and below | `model`, `store`, `secretseal`, `app/invocation`, `app/job`, `app/mail`, `app/user`, `app/exam`, `app/exam/attempt`, `app/exam/sitting` | none | infrastructure |
| job engine | `app/job` and below | `logging`, `model`, `store`, `app/access`, `app/apperror`, `app/invocation` | none | infrastructure |
| realtime application | `app/realtime` and below | `logging`, `model`, `app/apperror` | none | infrastructure |
| mail application | `app/mail` and below | `model`, `store`, `secretseal`, `app/exam`, `localization`, `app/access`, `app/apperror`, `app/invocation`, `app/paging` | `golang.org/x/net/html` | infrastructure except `io/fs` |
| application error vocabulary | `app/apperror` | `model`, `store` | none | infrastructure |
| invocation application | `app/invocation` | `model` | none | infrastructure |
| idempotency application | `app/idempotency` | `app/apperror`, `app/invocation`, `model`, `store` | none | infrastructure |
| exam manager access | `app/exam/manageraccess` | `model`, `store` | none | infrastructure |
| exam Sitting application | `app/exam/sitting` | `app/access`, `app/apperror`, `app/idempotency`, `app/invocation`, `app/exam/manageraccess`, `app/mail`, `app/realtime`, `model`, `store` | none | infrastructure |
| exam manager access consumers | `app/exam`, `app/exam/correction`, `app/exam/resource`, `app/exam/workspace` | `app/access`, `app/apperror`, `app/idempotency`, `app/invocation`, `app/exam/manageraccess`, `app/realtime`, `model`, `store` | none | infrastructure |
| exam mail and safe Markdown consumers | `app/exam/attempt`, `app/exam/review` | `app/access`, `app/apperror`, `app/idempotency`, `app/invocation`, `app/mail`, `app/realtime`, `model`, `store`, `app/exam/safemarkdown` | none | infrastructure |
| safe Markdown | `app/exam/safemarkdown` | none | none | infrastructure |
| file-content adapter | `filecontent` and below | `app/exam`, `app/user`, `model`, `packages/vfs` | `github.com/HugoSmits86/nativewebp`, `github.com/disintegration/imaging`, below `github.com/pdfcpu/pdfcpu`, `golang.org/x/image/webp` | infrastructure except `os` |
| Desktop release verification | `desktoprelease` | `model`, `internal/canonicaljson` | none | infrastructure |
| secret sealing | `secretseal` | none | none | infrastructure |
| localization | `localization` | none | `github.com/nicksnyder/go-i18n/v2/i18n` and below, `golang.org/x/text/language` | infrastructure except `io/fs` |
| access application | `app/access` | `app/apperror`, `app/invocation`, `model`, `store` | none | infrastructure |
| paging application | `app/paging` | `app/apperror`, `store` | none | infrastructure |
| academic application | `app/academic` | `app/access`, `app/apperror`, `app/idempotency`, `app/internal/batchitem`, `app/invocation`, `app/mail`, `app/paging`, `app/realtime`, `model`, `store` | none | infrastructure |
| user application | `app/user` | `app/access`, `app/apperror`, `app/idempotency`, `app/invocation`, `app/paging`, `app/realtime`, `model`, `store` | none | infrastructure |
| identity application | `app/identity` | `logging`, `app/access`, `app/apperror`, `app/exam/attempt`, `app/idempotency`, `app/internal/batchitem`, `app/invocation`, `app/job`, `app/jobs`, `app/mail`, `app/paging`, `app/realtime`, `app/user`, `model`, `secretseal`, `store` | `golang.org/x/crypto/argon2` | infrastructure |
| administration application | `app/administration` | `app/access`, `app/apperror`, `app/idempotency`, `app/identity`, `app/internal/batchitem`, `app/invocation`, `app/jobs`, `app/mail`, `app/paging`, `app/realtime`, `model`, `store` | none | infrastructure |
| onboarding application | `app/onboarding` | `app/academic`, `app/access`, `app/administration`, `app/apperror`, `app/idempotency`, `app/identity`, `app/internal/batchitem`, `app/invocation`, `app/jobs`, `app/mail`, `app/paging`, `app/realtime`, `app/user`, `model`, `store` | none | infrastructure |
| search application | `app/search` | `app/access`, `app/apperror`, `app/exam`, `app/invocation`, `app/paging`, `app/user`, `model`, `store` | none | infrastructure |
| batch item context | `app/internal/batchitem` | `model`, `store` | none | infrastructure |
| application | `app` | `logging`, `app/academic`, `app/access`, `app/administration`, `app/identity`, `app/onboarding`, `app/user`, `app/apperror`, `app/invocation`, `model`, `store`, `app/job`, `app/jobs`, `app/realtime`, `app/mail`, `app/search`, `secretseal`, `app/exam` and below | none | infrastructure |
| hosted webapp transport | `webui` | none | none | none |
| HTTP routing kernel | `httpapi/route` | `logging`, `app/apperror`, `app/invocation`, `app/paging`, `model`, `localization`, `internal/weborigin` | below `github.com/gorilla` | infrastructure except `net/http` |
| HTTP resources | `httpapi` and below | `httpapi/route`, `logging`, `app`, `app/academic`, `app/administration`, `app/apperror`, `app/identity`, `app/invocation`, `app/job`, `app/mail`, `app/onboarding`, `app/paging`, `app/search`, `app/user`, `app/exam` and below, `model`, `store`, `localization` | none | infrastructure except `net/http` |
| WebSocket transport | `websocket` and below | `logging`, `app/apperror`, `app/exam/attempt`, `app/invocation`, `app/realtime`, `model`, `store`, `localization`, `internal/weborigin` | below `github.com/gorilla` | infrastructure except `net/http` |
| cluster contracts | `cluster` | none | none | none |
| cluster adapters | `cluster/local` and below, `cluster/memberlist` and below | `cluster` | any | none |
| metrics | `metrics` | `config`, `internal/tlscert`, `store/timerlayer`, `store/localcachelayer`, `platform`, `cluster`, `packages/vfs`, `packages/mail` | `github.com/prometheus/client_golang` and below | none |
| tracing | `tracing` | `config` | `go.opentelemetry.io/otel` and below | none |
| external-auth adapters | `platform/externalauth` and below | `config`, `model`, `platform/externalauth` | any | none |
| platform lifecycle | `platform` | any except below `packages/cache`, below `packages/mail`, below `packages/vfs`, below `cluster`, below `platform/externalauth`, `store/sqlstore`, `metrics`, `app` and below, `httpapi` and below, `websocket` and below | none | none |
| healthcheck executable | `cmd/proctor-healthcheck` | none | none | none |
| offline operator commands | `operator` | `config`, `model`, `store`, `store/sqlstore` | none | infrastructure |
| operator executable | `cmd/proctor` | `cmd/proctor/commands` | none | none |
| operator commands | `cmd/proctor/commands` and below | `server`, `localization`, `operator` | `github.com/spf13/cobra`, `golang.org/x/sys/unix` | `database/sql` and below, `net/smtp` |
| ptool executable | `cmd/ptool` | `cmd/ptool/commands` | none | none |
| ptool commands | `cmd/ptool/commands` and below | `cmd/ptool/devseed`, `app/mail`, `cmd/proctor/commands`, `httpapi`, `internal/errorcatalog`, `internal/openapidoc`, `localization`, `websocket` | `github.com/spf13/cobra` | none |
| development seed HTTP client | `cmd/ptool/devseed` | none | none | none |
| unknown ptool children | below `cmd/ptool` | none | none | all |
| mail preview executable | `cmd/mailpreview` | `app/mail`, `localization`, `model` | none | none |
| configuration | `config` | `model` | any | none |
| leaf infrastructure | `logging`, `migrations` | none | any | none |

<!-- End of generated dependency rules. -->

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
