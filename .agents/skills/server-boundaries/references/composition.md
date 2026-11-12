# Composition and lifecycle reference

## Composition and lifecycle

`server.New` is the sole composition root. It loads configuration, constructs concrete adapters and store layers, gives infrastructure to `platform.Service` for lifecycle ownership, translates configuration into narrow application policies, constructs `app.App`, constructs HTTP and WebSocket transports, and wires post-commit effects. Construction is inert; blocking `Server.Run` alone enters runtime lifecycle stages and returns only after shutdown or runtime failure.

Construction may be split across cohesive files in the module-root `server`
package. That keeps backend selection reviewable without turning `server.New`
into one undifferentiated function or creating a second composition root.

The module root is about building and running one node. Beside the
construction and lifecycle files it holds only the adapters composition needs,
one per seam, each in a file named for the two sides it joins
(`authentication_cache.go`, `mail_sender.go`, `realtime_cluster.go`). An
adapter belongs there only when the two packages it joins may not import each
other. A wrapper that only measures a port is not an adapter: it lives in
`metrics` as `metrics.WrapCache`, `WrapMailer`, `WrapVFS` or `WrapCluster`,
and the root applies it while acquiring infrastructure, before Platform
accepts ownership. A metrics or tracing recorder that needs an application
type (`job.Recorder`, `mail.DeliveryRecorder`, `route.Tracing`) stays in the
root, because `metrics` and `tracing` may not import application packages.

Offline host commands that only open the configuration or the database live
in `server/operator` and build no node. The two that need the application,
administrator recovery and administrator MFA reset, construct an inert node
through `server.New` with a required-current schema and close it.

While the root acquires infrastructure, one private owner holds exactly the
`platform.OwnedResources` value that will transfer, plus the metrics and
tracing modules the root closes itself. Acceptance hands that value to
`platform.Accept` as one unit; what composition keeps afterwards is a
lifecycle-free projection whose cache, mailer and cluster types have no
`Close`, `Stop`, `Start` or `Ping`.

`platform.Service` owns shared infrastructure health, reconfiguration, startup, and shutdown. It is retained by `server.Server`, never passed into `app`, and never used as a service locator.

After the root has selected VFS and transferred its lifecycle to
`platform.Service`, it explicitly constructs the File Content module over that
VFS with immutable processing limits and a narrow work recorder, and projects
only the application's bounded content capabilities.
File Content neither selects a backend nor participates in lifecycle shutdown.

Constructors are inert. They validate required dependencies but do not normally start listeners or goroutines. Explicit `Start` methods begin work; `Close` or `Shutdown` is idempotent and bounded. Partial failure unwinds resources already acquired. The root `Server` drives every runtime owner through one Start/Close component shape; an owner that reports asynchronous failures adds only a failure channel.

Dependency injection is manual. Reflection containers, generated DI containers, global registries, and global mutable state are prohibited.

Application services do not receive `config.Config` or `config.Store`. Composition supplies small immutable policies, such as `SessionPolicy`, or narrow providers for explicitly dynamic behavior.

Startup enters Platform, commits the PostgreSQL serving-node lease, reconciles
protected Role and pending offline-recovery state, then starts durable Jobs,
WebSocket, listener ownership, and HTTP serving before publishing readiness.
Server records those stages privately: cancellation or failure before HTTP
accepts the listener closes the Server-owned listener. Normal shutdown makes a
running node unready and drains HTTP before Jobs, WebSocket, the HTTP transport,
the exact serving lease, and Platform are disposed. The 30-second lease renews
every 5 seconds; a transient renewal failure is retried each second while one
bounded attempt still fits at least 5 seconds before the last proven lease
expires. A rejected incarnation or exhausted slack instead makes the node
unready and force-stops HTTP before its last lease can expire. Renewal is
independent of the run context: it continues through the graceful HTTP drain
until the lease is closed after serving stops, and a lease failure during the
drain force-stops HTTP the same way. Lease writes
share the recovery fence and serialize only per node ID, so nodes never queue
behind one another's renewals. That failed lease remains until PostgreSQL expiry so offline
recovery cannot begin early; exact-incarnation withdrawal applies only to
normal shutdown. Every constructed owner is disposed exactly once, including on
close-before-start; only successfully entered active stages require runtime
drain behavior. Shutdown uses bounded deadlines and retains drain and cleanup
failures for concurrent or repeated callers. Liveness, readiness, and
authorized dependency diagnostics remain distinct signals.

After internal readiness is published, `Server.Run` invokes at most one
optional, promptly returning host-process readiness observer. The observer has
no infrastructure lifecycle and cannot make an otherwise ready node fail; its
errors are operational diagnostics. The CLI uses this seam to emit systemd's
`READY=1` datagram without making the module-root server depend on environment
variables or a supervisor-specific implementation.

## Plugins

Plugins are first-party Go packages compiled into the server; Proctor never
loads outside code. `server/plugin` holds their contracts and imports only the
standard library. `pluginhost` runs them and imports only `server/plugin`. The
root names every built-in in `builtinPlugins()`, the one place a plugin is
listed.

- Activate plugins right after configuration and logging, before anything
  that consumes what they register. A plugin that fails to activate is left
  out and never fails construction. Only a configuration mistake does, such
  as settings for a plugin that is not compiled in.
- Hand what plugins registered to its consumer from the root, as plain
  contracts: identity provider types to `externalauth`, the export format to
  `filecontent`, event subscriptions to `app/events`. The host wraps each one
  so that a panic is contained and a failed plugin answers
  `plugin.ErrUnavailable`. Consumers never see the host.
- Add a capability as one typed `Host` method with a contained wrapper, and
  only when a plugin needs it. Plugins get no broad API, store contract or
  `platform.Service`.
- Deactivate plugins once nothing can call them: after Jobs and transports
  stop, or when a failed construction is released.

## Module placement

- Keep Proctor domains in one `server` module — identity, authorization, academics, exams, WebSocket, clustering stay in `server`; extract `packages/*` only with a Proctor-independent contract and external consumers.
- Keep platform out of application services — `app` depends on ports and `model`/`store`, never on `platform.Service`.
