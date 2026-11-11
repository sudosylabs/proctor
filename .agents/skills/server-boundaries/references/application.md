# Application-boundary reference

Use cases are direct methods with typed commands or queries and typed results:

~~~go
CreateAcademicUnit(ctx, invocation, command) (*model.AcademicUnit, error)
GetAcademicUnit(ctx, invocation, query) (*model.AcademicUnit, error)
~~~

A use case takes `(ctx, invocation, command)` with an `XCommand` for a
mutation or an `XQuery` for a read, passed by value, and returns a domain model
or an `XResult`. Ports to infrastructure or another module that are not use
cases name their argument for that port, such as a renderer `RenderRequest`.

`invocation.Invocation` is immutable and explicitly contains the principal and safe call metadata needed for authorization and audit. `context.Context` remains for cancellation, deadlines, and propagation, not hidden security dependencies.

The `app/invocation` leaf owns that one call type so the parent and every Exam
child module share it without a parent import, and transports import it
directly. Every use case and consumer-owned port takes
`invocation invocation.Invocation`, and no module declares a local call type,
alias, or conversion between call types.
Actorless work carries `invocation.System` in the same position, which names
either the durable Job execution (`invocation.Job`) or a non-durable runtime
task (`invocation.Task`) such as the Attempt expiry scan, startup
reconciliation, or an offline host-operator command, and cannot impersonate a
User. An anonymous transport use case (installation status, access discovery,
external provider list, Desktop compatibility) still takes an Invocation: a
zero Principal with its request metadata. `TestUseCasesTakeAnInvocationFirst`
checks every exported App method; the few that are not use cases (credential
resolution, realtime propagation, composition attachment, child-module ports)
are listed there with their reasons.

Each actor-sensitive use case authorizes immediately before expensive work or mutation. HTTP and WebSocket establish credential and assurance requirements but do not preflight resource permissions or issue decision receipts.

Domain models own local invariants and transitions. Application services own use-case policy, authorization, multi-aggregate coordination, transaction intent, audit, and external effects.

Each domain area is one package under `app/`: `app/access`, `app/academic`,
`app/identity`, `app/user`, `app/administration`, `app/onboarding`, and
`app/exam` with its children. An area package
exports a `Service` (or a few, when one would be too wide) whose exported
methods are the area's use cases, together with that area's effects and
adapters. A contributor looking for a feature opens the area directory; nothing
about one flow lives in the parent package.

Background jobs call application use cases with a system/service invocation. They do not manipulate stores directly.

The `app/idempotency` leaf owns the shared validation, canonical JSON, and
SHA-256 preparation algorithm for durable command idempotency, and reports its
failures as application errors: `Require` rejects a missing key with
`idempotency.key_required`, `Prepare` treats the key as optional, and both
report an invalid acting user as `idempotency.key_invalid` and an unencodable
semantic document as `request.invalid`. No module wraps it in a local
preparation helper. It does not own any business operation. Each command owner decides whether a key is required,
defines its stable operation name, normalizes its own semantic inputs, and
constructs the semantic document whose compatibility is preserved across
retries. The owning Exam service receives the bounded raw key and prepares
the persistence contract beside the command policy.

The `app/job` child module owns the generic durable execution engine: immutable
descriptor validation, claiming, leases, fenced transitions, checkpoints,
work budgets, retry, cancellation observation, and bounded lifecycle. Domain
Job handlers remain beside their owning use cases and cross that seam through
strict typed documents and safe outcomes. The engine depends only on the
domain Job records and `store.JobStore`; it never imports its parent package or
selects infrastructure.

The `app/realtime` child module owns transport-neutral realtime events and
connection-close reasons, local-first delivery, peer propagation codecs,
loop prevention, attachment synchronization, and propagation of session
revocation and authentication or authorization invalidation. It is inert after
construction: it owns no goroutine, queue, retry loop, or infrastructure
lifecycle. It declares `realtime.Publisher`, the one port through which an
application area delivers post-commit effects. An area service holds a
Publisher, builds its own events in private `publish<Subject><Change>` methods
(`effects.go` in the area package) and calls them from the use case after
commit; a failed publication is passed to `Publisher.Report` and the use case
still succeeds. Area tests pass `realtimetest.Publisher` and assert its
recorded calls whole. Two publications several Exam areas owe, the Sitting
board invalidation and the Exam activity invalidation of a Sitting's
candidates, are functions of this module that take the Publisher and, for the
second, a narrow candidate reader the Exam Sitting Store satisfies. The parent
application's realtime service implements the Publisher and retains Principal
validation, WebSocket subscription authorization, and translation between
delivery failures and public application errors.

The `app/mail` child module owns all server-specific mail composition:
the complete template-definition registry, semantic preparation operations,
closed typed presentation requests, safe render inputs, disabled/ineligible
suppression, shared frozen-payload mechanics, stable Message-ID construction,
and locale-indexed Sitting fan-out bundles. It receives rendering and sending
capabilities at construction and returns validated `MailOccurrence`,
`MailDelivery`, and Job records; it does not send during a business mutation or
persist anything itself. Parent use cases retain authorization, audit,
idempotency, recipient lookup, and named transaction coordination through
narrow consumer-owned preparation ports. Production code in the parent uses
the child package's owned contracts directly rather than redeclaring aliases;
the child never imports the parent application, transports, platform, SQL, or
deployment configuration.

The examination capability is a cohesive `app/exam` boundary.
It owns authoring and publication policy, manager relationships, Sitting and
Attempt lifecycle, Participation fencing, Submission coordination, integrity
evaluation, authorization timing, audit intent, and post-commit effects.
Selective `app/exam/resource`, `app/exam/workspace`, `app/exam/attempt`,
`app/exam/correction`, and `app/exam/review` children are justified by their distinct stable
mechanics: published read-only supporting material, Draft Starter Workspace
authoring, Attempt admission/continuity, bounded Focus Loss evaluation, and
acknowledged live Workspace coordination over opaque VFS objects, plus bounded
live-correction staging and atomic application policy, and post-Submission
integrity decisions/finalization/release. The small `app/exam/safemarkdown`
module owns the one shared candidate-facing sanitizer used by protected Exam
presentation and released Review remarks; it contains no authorization or
transport policy. `app/exam/attempt`
consumes separate bounded Store contracts for the Attempt lifecycle and mutable
Workspace aggregate, narrow audit and content ports, and the shared
`realtime.Publisher`. None imports
the parent application package or selects SQL, VFS, WebSocket, Jobs, or other
infrastructure.

A service is a struct whose fields are its collaborators: the `store`
contracts it calls, the area's `Authorizer` and `Auditor` (satisfied in
production by `*access.Authorizer` and `*access.Auditor`), and whatever else it
needs. Its constructor takes one `Dependencies` (or `<X>Dependencies`) struct
declared beside it rather than positional collaborators
(`TestServicesTakeOneDependencies`).
A service generates identifiers with one `NewID func() string` (or calls
`model.NewID` directly) and reads time from one `Now func() time.Time`.

`app.App` holds one named field per service (`Academic`, `Identity`,
`ExamAuthoring`, `Sittings`, `Attempts`, ...), so every call site names the
owner of the use case it calls: `app.Academic.CreateClass(...)`,
`app.Attempts.SubmitExamAttempt(...)`.

Each Exam use case is the exported method on its owning service, named as the
public use case (`ScheduleExamSitting`), with the command, query, and result
shapes that the transports' ports require. Those shapes belong to the service, and
transports name them there (`sitting.ScheduleCommand`); the parent neither
aliases nor repeats them. The service method does all the boundary work: defensive input
copies, input normalization and defaults, deliberately narrower results, and
error concealment. Each Exam module declares one `publicFailures`
`apperror.Concealment`; a use case defers `publicFailures.Conceal` when its
target's existence is protected, or `Present` when it is not, so a denial or a
module's not-found code becomes `resource.not_found`. A use case whose outcome
the operations seam observes records it through the module's optional
`OperationalRecorder` after concealment.

Every application module, parent or child, reports expected public failures
with the one `*apperror.Error` type from the `app/apperror` leaf. Children
construct it directly with `apperror.New(code)` or `apperror.Invalid(code,
field)`; they declare no local fault type or per-package error helpers. A
use case's public failures pass through its module's declared
`apperror.Concealment`, and the parent adds nothing.
Typed Store failures are translated only through a declared
`apperror.StoreTable` keyed by Store error kind, entity, and constraint name;
a use case adds a small function before its table only for a typed Store
error that carries safe detail, such as a current-revision hint.

The `app/exam/manageraccess` leaf owns the shared current Exam Manager and exact
Academic Unit membership rule used to select ordinary or explicit override
Actions. It receives the caller's access projection and time, performs the
current membership lookup through its narrow port, and returns the selected
Action or lookup failure. It does not grant permission: each use case retains
input and projection validation, its precise Resource and authoritative
authorization call, error presentation, audit timing, and named Store command.
The same exact-unit membership check supports Exam creation and Manager-target
eligibility without implying either role permission or a Manager relationship.

The Attempt child also owns voluntary and actorless automatic Submission use
cases plus protected manager inspection queries. Voluntary Submission crosses
one named Store aggregate seam that revalidates current membership and
continuity, seals lifecycle and integrity state with the immutable manifest,
and commits audit and idempotent outcome together. Bounded Sitting-close work
pages Attempts in its parent Job use case, then invokes the child once per
Attempt; the child records system-audit intent and calls a separate natural-
identity sealing operation without importing or scheduling Jobs. The
application publishes only safe manager and candidate facts and requests exact
Attempt-Connection unbinding after commit; a replay returns the retained safe
receipt and repeats neither effect. Manager queries authorize the canonical
Submission resource before concealing any nested ownership mismatch, and
stream retained file bytes only through the narrow content port.

The package is introduced with the first working vertical slice, not as an
empty architectural placeholder. Its `doc.go` must define the Exam, Draft,
Revision, Sitting, Attempt, Participation, Resource, Starter Workspace,
Workspace, Submission, Integrity, and Review vocabulary; state what the
package owns and excludes; and state its allowed inward dependencies. The
parent `app.App` exposes its services as named fields and `app.New` remains
the sole application constructor. Detailed examination boundaries are in
[the `exam-lifecycle` reference](../../exam-lifecycle/references/examinations.md).

Realtime receives its required authentication invalidator and diagnostics at
construction. Its sink and peer fan-out are attached once before readiness
because the composition graph constructs the application before its WebSocket
and cluster adapters. The child depends only on the domain model and narrow
consumer-owned ports; it never imports its parent package or transport
packages. The parent does not redeclare the child's event, close-reason, sink,
fan-out, codec, or propagation contracts; its thin publication service exists
only to translate typed delivery failures into application errors. WebSocket
admission, principal revalidation and subscription authorization, is one
focused service that composes the authentication, Exam, Sitting, and Access
Control decisions.

Operator Job use cases depend on the engine rather than a separate Job Store
and descriptor catalog. They retain Principal authorization, resource
resolution, durable audit ordering, and application-error translation; the
engine owns safe projections and descriptor-governed persistence mechanics.

Atomic store operations are explicit named aggregate operations such as bootstrap, enrollment transfer, or password-reset consumption. The application decides policy; the adapter owns locking, constraints, concurrency checks, and commit/rollback. Raw SQL transactions and generic `WithTransaction(func(Store))` callbacks do not cross into `app`.

The name and contract expose the complete atomic and race guarantee to callers
and reusable conformance tests. A generic transaction callback would leak
adapter mechanics while leaving the business guarantee undiscoverable.

Browser Authentication application services consume purpose-specific Store
facts rather than persisted transaction aggregates. Creation, Invitation
resolution, code issue, cancellation, and exchange expose only the identities,
callbacks, deadlines, Session, and credential expiries needed for the next
application decision. Persistence retains ownership of locking, PostgreSQL
time, proof destruction, and atomic terminal transitions.

## Interfaces and public surface

Interfaces normally live beside their consumer and expose only what it needs. Broad aggregates may exist at composition but are not daily dependencies. Prefer concrete types during construction and narrow interfaces at consumption.

An interface exists only where something substitutes behind it: a second
production implementation, a fake the consumer's tests pass in, a
type-assertion or `errors.As` capability probe, or a port across a module
boundary that the consumer cannot name concretely, such as a File Content
capability. Otherwise the consumer holds the concrete type. An area package
declares each such collaborator once (typically `Authorizer` and `Auditor`),
not once per service; realtime effects go through the shared
`realtime.Publisher`, which no area redeclares. A service never redeclares a Store contract: it holds `store.ClassStore`
(or the root `store.Store`) directly. A test fakes a Store contract by
embedding it and overriding only the methods the test exercises:

~~~go
type classStoreFake struct {
    store.ClassStore
    created *store.ClassCreation
}
~~~

Calling a method the fake does not override panics, which tells the test author
exactly what to add.

An adapter exists only where two packages genuinely disagree on a type and
neither may import the other, such as the module root's translation of
`platform.Cache` into the authentication cache port. An adapter translates one
value at one seam and never wraps another adapter. Authorization uses the
single `Authorize(ctx, Invocation, Action, Resource)` shape of
`*access.Authorizer`.

Persistence is the exception: the bounded root `store.Store` and complete per-model or aggregate contracts live together in `store` for shared conformance testing.

Do not prefix interfaces with `I`. Add compile-time assertions where an adapter intentionally implements an important cross-package contract. Every substantive package has a package comment explaining what it owns, excludes, and may depend on.

Every production function is reachable from a server binary. Code only tests
call is dead: delete it, or, when it is a seam its own package's tests need,
declare it in a `_test.go` file of that package. A seam that other packages'
tests construct through (the `server.NewForTesting` hook, configuration and
log fakes, fixture constructors) stays in production code only with a reason
in the architecture test's `reachableOnlyFromTests` list, which
`TestProductionFunctionsAreReachable` checks by rapid type analysis from every
`main` package.

Migrations are by vertical slice. A package or capability is introduced only when working code has a stable responsibility to inhabit it. An area moves into its package as one complete commit series (code, tests, transports and documentation together), never half in the parent and half in the child.

## One application surface

Every use case is declared once, as an exported method on the area service that
owns its policy, under its public use-case name (`CreateClass`, not `Create`).
`app.App` exposes the area services as named fields and declares no use case of
its own. Transports call `app.Academic.CreateClass` and so on.
`httpapi.New(application, settings)` builds each resource from the area
services it serves, usually one. An `httpapi` consumer interface lists the
methods a resource calls and is there for the resource's tests to fake; a
resource no test fakes takes the concrete service. The WebSocket hub receives WebSocket
admission (`app.RealtimeAdmission`) and the Attempt service (`app.Attempts`)
as its typed dependencies.

Work a wrapper would add belongs in the owning service method: operational
outcome recording, error concealment, input normalization, defensive copies,
and result narrowing. A service whose construction is optional, such as Mail
or Job operations when the graph has no Job Store, answers its own
unavailability with a nil-receiver guard. A use case that needs several areas
lives on the service that owns its policy and receives the others as
collaborators; it never reads another service's fields.

`app.App` is not a persistence locator. Area services hold the Store contracts
they use. App itself has no Store field or accessor; architecture tests reject
production `App.Store()` traversal.

`app.New` in `app/new.go` remains the sole application constructor. It reads
top to bottom: validate the dependencies, build the shared mechanics (password
hashing, attempt accounting, cache invalidation, realtime publication, audit
and mail composition) and `access`, then each area in dependency order, then
the optional Job runtime and the services that span areas (search and the
administration counts), and return one `App` literal that names every field.
Its local variables carry each built service to the next step; an area too
large to build inline has a helper in `new_<area>.go` named for what it
builds (`newExamServices`, `newJobRuntime`). Identity is built before
onboarding and the Job runtime, so it receives an Invitation acceptor and a
Job wake (which onboarding also receives) that `New` connects once those
exist.

Another project's package taxonomy is not a template. An area or child package
is introduced only after the responsibility is stable, can obey inward
dependencies without a cycle, and passes the deletion test.

Test fakes remain beside the tests that use them; Store conformance and the
real composition graph provide shared persistence and wiring evidence.

## Migration status

The move to area packages (plan of 2026-11-10) is complete. Every area lives
in its package, and `App` names every service in its own field and embeds
nothing; `TestApplicationSurfaceIsNamedServices` keeps it that way. The parent
keeps only the services that span areas or own no area (`SearchService`,
`AdministrationCounts`, `RealtimeService`, `RealtimeAdmission`,
`JobOperations`, `MailService`), each named on `App` like an area service.

Every area service is flat: `academic.Service`, `user.Service`,
`administration.Service`, `onboarding.Service` and `identity.Service` are each
one struct whose fields are the Store contracts it calls and one of each
collaborator, built by one `New`. Identity is the one area with two audit
collaborators: `auditor`, the `access.MutationAuditor` for ordinary audited
mutations, and `securityAudit`, the subset of `*access.Auditor` for the
sign-in, Desktop and MFA audit protocols that the mutation runner does not
cover. No area service embeds another implementation. A new concern adds its
methods and fields to its area's `Service`; it does not add an embedded
implementation.
