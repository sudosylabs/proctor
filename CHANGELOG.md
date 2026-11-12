# Changelog

Notable changes to Proctor's four product modules are recorded here. The
format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
each module is versioned under the
[versioning policy](build/README.md#versioning-policy). Entries name their
module when a change is not server-only. No module has been released yet.

## [Unreleased]

### Removed

- Server: the webapp build manifest and the devseed journal no longer carry
  a `schema_version`; a manifest or journal that does is rejected.
- Server: execution hosts and interactive terminals, including their APIs,
  settings, metrics, and persistence (**breaking**).

### Changed

- Server: list filters. `GET /api/v1/users` takes repeatable `role_id` (at
  most 16; current Role Bindings inside the caller's `role_binding.view`
  scopes, and only for a caller with Institution-wide `role.view`, else
  `request.invalid`), `state=active|disabled` (disabled Users stay
  Institution-wide only, and `state` with `include_disabled=true` is
  `request.invalid`), and `never_signed_in`, `last_active_after` and
  `last_active_before`, which read `last_activity_at` and are refused like
  `sort=last_active`. `GET /api/v1/audits` takes `subject_user_id`,
  `status=attempt|success|fail` and a repeatable `action` (at most 32); an
  action outside the catalog is now `audit.query.invalid` rather than an empty
  page (**breaking**). `GET /api/v1/mail/deliveries` takes
  `recipient_user_id`. Every filter narrows the caller's visibility. The
  baseline adds `mail_deliveries_target_user_id_created_at_id_idx`. Existing
  development databases must be recreated.
- Server: list sorting and capped totals. `GET /api/v1/users` takes
  `sort=username|name|created|last_active` and `dir`; `last_active` is
  refused with `request.invalid` unless the caller holds Institution-wide
  `user.view`. `GET /api/v1/audits`, `GET /api/v1/jobs` and
  `GET /api/v1/onboarding-imports` take `order=asc|desc`. Class and Academic
  Unit member lists take `sort=enrolled|since|name` and `dir` and now default
  to the membership start rather than `(user_id, id)` (**breaking**).
  `GET /api/v1/mail/deliveries` takes `sort=updated|created` and `dir` and now
  defaults to the last update, newest first, rather than creation
  (**breaking**). The Class listings take `dir` and carry a bounded
  `student_count` for `class.members.view` callers. Member lists, Role
  Bindings, and the Programme, Programme Level, and Class listings report a
  first-page `total`. Every cursor records its order, and a cursor presented
  under another order is `request.invalid`; outstanding cursors of these lists
  are invalid. The baseline adds the generated `users.sort_name` column and
  indexes for each new order. Existing development databases must be
  recreated.
- Server: `GET /api/v1/audits`, `GET /api/v1/jobs` and
  `GET /api/v1/jobs/{job_id}/attempts` reject an out-of-range, empty or
  repeated `limit` or `cursor` with `request.invalid`. They had silently read
  the default page without their filters or cursor. Every list now shares one
  page rule. The baseline adds `jobs_created_at_id_idx` for the Job list order.
  Existing development databases must be recreated.
- Server: the baseline adds `access_policies.role_holder_mfa_required`,
  `session_idle_minutes` and `session_absolute_minutes` and
  `sessions.mfa_enrollment_required`, and admits those three policy fields
  among the recorded Access Policy transition fields. Existing development
  databases must be recreated.
- Server: the baseline adds indexes for the User directory affiliation filter,
  the onboarding import list, failed Jobs and Invitations by state, bounds
  `invitations.expires_at` between one and thirty days after creation, and
  adds `jobs.created_by_user_id`. Existing development databases must be
  recreated.
  the onboarding import list, the student progression list by source and by
  destination Class, failed Jobs and Invitations by state, adds the nullable
  `onboarding_imports.file_name` column, and bounds
  `invitations.expires_at` between one and thirty days after creation.
  Existing development databases must be recreated.
- Server: recent authentication now counts primary proof only. An MFA
  challenge no longer refreshes recency; it still makes a Session strong.
- Server: Desktop Authorization that reuses a recently reauthenticated Web
  Session now gives the new Desktop Session that latest primary proof instead
  of the Web Session's original sign-in time.
- Server: `CreatePersonalAccessTokenRequest.scopes` now lists the accepted
  action names as an `enum`, and its example uses the real scope `exam.view`
  instead of the invalid `examinations:read`.
- Server: audit events record the User they concern in `subject_user_id`: a
  User resource is always its own subject, and Role Binding, Class and Academic
  Unit membership, Invitation acceptance, self-service two-step verification,
  and Personal Access Token events name the affected User. Existing
  development databases must be recreated.
- Server: the account disable and enable audit records the operation
  `disable_account` or `enable_account` instead of `set_disabled`.
- Server and `packages/vfs`: move `golang.org/x/net` to v0.62.0 to clear
  CVE-2026-78661 (HIGH), with `golang.org/x/crypto` v0.58.0, `x/sys` v0.49.0,
  `x/text` v0.43.0, `x/sync` v0.24.0 and `x/tools` v0.51.0. Exception to the
  two-week dependency age rule: `x/net` v0.62.0 was 11 days old when adopted,
  taken early because the advisory blocks the runtime image scan.
- Server: transactional mail is redesigned as a plain, typeset letter with
  rewritten copy for every message. Each message greets the recipient by first
  name, names the institution, writes dates and times for people in the
  recipient's time zone instead of UTC timestamps, states exact link expiry,
  and leads security notices with what to do if the change was unexpected.
  The email-change warning shows a masked form of the new address and the
  retention notice shows its deletion date. Submission receipts no longer show
  the Sitting and Submission identifiers.
- Build: the isolated `gopls` tool moves to v0.23.0, the newest release that
  supports Go 1.26, picking up MCP go-sdk v1.6.0 and clearing its advisories.
- Build: local, cluster, and S3 test stacks run MinIO from
  `cgr.dev/chainguard/minio` (RELEASE.2026-10-23T20-06-51Z), pinned by digest,
  because `minio/minio` is no longer published on Docker Hub.
- Build: the workspace, the server, the build tools, the pinned build image
  and the editor toolchain move to Go 1.26.11, clearing the reachable
  `net/http` vulnerability GO-2026-6859. `packages/*` keep their Go 1.26.10
  minimum; the workspace builds them with 1.26.11.
- All modules: build with Go 1.26.10 (from 1.25.13), and move
  `golang.org/x/net` to v0.60.0 in the server and `packages/vfs`, clearing the
  known standard-library and `x/net` vulnerabilities (CVE-2026-78667,
  CVE-2026-78669, CVE-2026-97031, CVE-2026-111943). Building any module now
  requires Go 1.26.10 or newer. The server also moves `google.golang.org/grpc`
  to v1.83.2, `filippo.io/edwards25519` to v1.1.1, and
  `github.com/go-jose/go-jose/v4` to v4.1.6 (CVE-2026-116587) to clear their
  advisories.
- Server: every route's public error list now matches what its operation can
  return, checked against the code by a static analysis. Around 200 routes
  dropped codes they could never return (shared academic families on Exam
  and Attempt routes, `authorization.denied` and concealed Attempt codes
  behind concealment, unrelated delivery codes) and gained codes they could
  return but answered as `server.internal`. An authorization decision whose
  audit cannot be recorded now fails with `authorization.unavailable` instead
  of `audit.unavailable` (**breaking**).
- Server: every collection answers `{"items": [...], "next_cursor": "..."}`
  and pages with `limit` (default 50, at most 200) and `cursor`. Academic
  Units, children, Programmes, Programme Levels, Academic Periods, Classes,
  Users, Roles, Role Bindings, Affiliations, Sessions, personal access tokens,
  Desktop Registrations, authentication providers, Browser sources, and Draft
  Resources and Starter Workspace entries no longer answer bare arrays or
  unbounded lists; the User directory drops `after_username`/`after_id`;
  Academic Unit and Class Members drop the `Link` header and always page;
  audit listing renames `events` to `items`; Browser receipts replace
  `first_sequence`/`next_sequence` with `cursor`/`next_cursor`; and the
  Workspace change journal replaces `after_cursor`, `entries` and `has_more`
  with `workspace_cursor`, `cursor`, `items` and `next_cursor`. Programme
  Level Classes are ordered by name (**breaking**).
- Server: optional instants are omitted instead of `null` everywhere. The
  delivery closure (`closed_at`, `upload_expires_at` and its other optional
  members), unretained-delivery summaries (`first_unretained_at`,
  `last_unretained_at`), and Browser integrity groups
  (`overflow_first_received_at`, `overflow_last_received_at`) leave absent
  members out; requests reject `null` for Desktop Compatibility Policy
  `retry_at` (omit it for none) and Academic Period `start_at`/`end_at`. The
  HTTP suspension object's `flag_id` becomes `integrity_flag_id`, matching
  the realtime event (**breaking**).
- Server: the remaining `cancelled` spellings become `canceled`. The Desktop
  authorization transaction `state` value and `cancelled_at` column, the
  Sitting mail `change_kind` value, the `exam.sitting_cancelled` mail template
  key, and its localization IDs are renamed in the pre-release SQL baseline;
  development databases must be reset (**breaking**).
- Server: every revision fence is the body member `expected_revision`.
  `expected_draft_revision`, `expected_exam_revision`,
  `expected_attempt_revision`, `expected_declaration_revision`,
  `expected_sitting_revision`, and the single-fence uses of
  `expected_review_revision` and `expected_policy_revision` become
  `expected_revision`; the Integrity decision's `expected_decision_revision`
  becomes `expected_revision` beside `expected_review_revision`. Academic
  archive `DELETE` takes `expected_revision` in a JSON body instead of the
  query string (**breaking**).
- Server: pagination cursors are unversioned. Every cursor rejects a
  `version` member, the audit, Job, Job attempt, mail delivery, and Exam
  catalog cursors no longer accept versionless or version-0 tokens, and every
  cursor issued by an earlier build is invalid (**breaking**).
- Server: internal persisted documents carry no version field. Job
  commands, checkpoints, and results lose `command_version`,
  `checkpoint_version`, and `result_version`; the pre-release SQL baseline
  drops those columns and development databases must be reset. Frozen mail
  payloads and Sitting fan-out bundles lose their `version` member and the
  version-one bundle and relative-logo readers. Command outcomes drop
  `fingerprint_version` and `outcome_version`, and the request fingerprint
  no longer mixes in a version tag, so earlier retained outcomes cannot
  replay.
- Server: every retained command outcome is a dedicated persisted document
  with explicit member names, decoded strictly. Outcomes that stored model
  values under Go field names (Exam Sittings, Exams and Exam Managers, Review
  and Records results, Retention Previews and Controls, live corrections,
  Starter Workspace, Exam Resources, Academic Units and Periods, security
  preflights, delivery summaries, and the policy replacements) change format,
  as does the prepared security preflight document. Earlier retained outcomes
  no longer replay; reset development databases (**breaking**).
- Server: Integrity Discrepancy responses drop the constant `schema_version`
  member, and Examination Export archives drop `schema_version` from
  `records.json` and `manifest.json`. Browser Policies no longer carry an
  internal schema version, so a zero policy is the disabled policy
  (**breaking**).
- Server: command operation names and digest, HMAC, and nonce
  domain-separation tags lose their version suffix and use one dot-separated
  snake_case spelling (`exam.attempt.reallow.v1` becomes
  `exam.attempt.reallow`, `proctor/invitation-claim/v1` becomes
  `proctor.invitation_claim`). Retained command outcomes, issued CSRF tokens,
  in-flight OIDC logins, and pinned review digests from earlier builds no
  longer match (**breaking**).
- Server: wire timestamps are RFC 3339 strings. `create_at`, `update_at`, and
  `delete_at` become `created_at`, `updated_at`, and `archived_at` (omitted
  while active), Job `complete_at` and `start_at` become `completed_at` and
  `started_at`, timestamp query parameters take RFC 3339, and absent optional
  timestamps are omitted instead of `null` (**breaking**).
- Server: cancellation is spelled `canceled` on the wire. Retention Notice
  `cancelled_at` becomes `canceled_at`, and the Retention Notice and Record
  Retirement `state` value `cancelled` becomes `canceled`, matching Sitting
  `canceled_at` and every other `canceled` state. The pre-release SQL baseline
  renames the matching retention columns and state; development databases
  must be reset (**breaking**).
- Server: error codes follow one grammar, and the HTTP status derives from the
  code. Several codes were renamed (for example
  `authentication.invalid_credentials` became
  `authentication.credentials_invalid`, `internal` became `server.internal`,
  and `not_found` became `route.not_found`). Every `*.unavailable` code is 503,
  and Problem Details `type` URIs end in the code (**breaking**).
- Server: an application result the server cannot present returns 500
  `server.internal` instead of a resource family's `*.unavailable` code, and
  unparseable list query parameters return `request.invalid` (**breaking**).
- Server: create requests reject members that the route path already owns,
  such as `parent_id` on Academic Unit create or `class_id` on Class Member
  enrollment, with `request.invalid` (**breaking**).
- Server: production is the untagged build default. Development builds must
  pass `-tags dev`; `-tags production` no longer has an effect (**breaking**).
- Server: the pre-release SQL baseline changed (new indexes and manual Attempt
  suspension). Development databases created from an earlier baseline must be
  reset before use (**breaking**).
- Server: static TLS certificates and configuration reload on `SIGHUP`, and the
  systemd unit gains `ExecReload`.
- Server: Let's Encrypt mode requires
  `Server.TLS.LetsEncrypt.AcceptTermsOfService` to be `true` (**breaking**).
- Server: named advisory locks hash with one key scheme, so a node on an
  earlier build does not exclude a node on this one; upgrade every node of a
  cluster together (**breaking**).
- Server: Store timer metric labels drop the `_with_audit` suffix, and every
  security-sensitive Store mutation requires its audit record.
- Server: when the success audit of a login cannot be recorded, no Session is
  issued at all.
- Server: the administrative Session list (`GET /api/v1/users/{user_id}/sessions`)
  returns one page in an `items`/`next_cursor` envelope, newest first, and
  accepts `limit` and `cursor` (**breaking**).
- Server: the pre-release SQL baseline gained Session retention indexes;
  development databases must be reset (**breaking**).
- Build: `make container` packages Linux binaries with the same recipe as
  `make dist` and builds them with the release Dockerfile, so a local image
  matches a published one; `Dockerfile.runtime` is removed.
- Build: `make module-pins-check`, run in CI, fails when `server/go.mod` pins a
  `packages/*` module at a commit whose content differs from the workspace
  module, or when `go mod tidy` has dropped the `google.golang.org/genproto`
  requirement that keeps workspace builds free of ambiguous imports.
- Build: the root `make check` verifies transactional-mail template freshness,
  and `make -C server integration-postgres` runs every PostgreSQL integration
  test in its packages instead of a fixed list.
- Server: every shutdown path runs one ordered sequence (listener, HTTP, Jobs,
  WebSocket, transport, serving lease, metrics, tracing, Platform) under a
  single `Server.ShutdownTimeout` budget that starts with shutdown, instead of
  separate back-to-back timeouts. The Job, WebSocket, and metrics drains now
  take what remains of that budget too, so a slow step can no longer stretch
  shutdown past it or past the supervisor's stop timeout;
  `Metrics.ShutdownTimeout` only caps the metrics step within it.
- Server: the longest production functions and files are split by
  responsibility, without behavior changes. An architecture test now fails on
  any production function over 120 lines, with a short allowlist that may only
  shrink.
- Server: `migrate status` and the read-only inspection commands open
  PostgreSQL with `default_transaction_read_only`, so the database itself
  refuses any write.
- Docs: the documentation toolchain lockfile and overrides are refreshed,
  clearing every critical npm advisory. Two high advisories with no compatible
  fix are documented in `docs/site/README.md`.

### Added

- Server: audit summaries. Each item of `GET /api/v1/audits` and
  `GET /api/v1/users/me/audits` carries a `summary` sentence in the language
  `Accept-Language` selects, beside the existing codes. Names are read from
  the live records, once per page, and appear only when the caller could see
  that record now; account history still never names another person.
- Server: Users rename their own devices.
  `PATCH /api/v1/users/me/desktop-registrations/{desktop_registration_id}`
  takes `{"display_name": ...}` and keeps the name across later Desktop
  sign-ins with the same key; a revoked registration is
  `desktop_registration.revoked`. `PATCH /api/v1/users/me/sessions/{session_id}`
  takes `{"device_name": ...}` for a browser or CLI Session and refuses a
  Desktop Session with `session.desktop_named_by_registration`, naming the
  registration to rename instead. Both need an interactive Session but no
  recent authentication, trim and sanitize the name to 1-128 characters
  without control characters, and are audited (`desktop_registration.rename`
  and `session.manage` with `rename_own_session`), appearing in account
  history as `desktop.registration_renamed` and `session.renamed`.
  `GET /api/v1/users/me/sessions` items carry
  `desktop_registration_display_name` for Desktop Sessions. The baseline adds
  `desktop_registrations.renamed_at`; existing development databases must be
  recreated.

- Server: the realtime protocol has an AsyncAPI 3.0 contract,
  `server/asyncapi/` compiled into `server/asyncapi.json`, with one message
  for each of the 39 events and for `hello`, `resync_required`, `ping`,
  `subscribe` and `unsubscribe` and their replies, plus the close codes.
  Payload shapes are JSON Schema files in `server/schemas/realtime/`.
  `ptool asyncapi build` and `check` (`make -C server asyncapi-build`,
  `asyncapi-check`) validate it against the official AsyncAPI schema and check
  every example. The server's event list is now closed: realtime publication
  refuses an event name `app/realtime` does not declare, and conformance tests
  hold each constructor's data to its schema. `user_profile_picture_changed`
  now carries `changed_at` as an RFC 3339 string instead of Unix milliseconds
  (**breaking**). The API docs gain a generated Realtime Reference.
- Server: the Access Policy can require two-step verification of role
  holders, every User with a current Role Binding at any scope, with
  `role_holder_mfa_required` on `GET`, `PUT` and preflight; `GET` also
  reports the read-only `role_holder_mfa_enforced`. A role holder without a
  second factor signs in on the web to a Session marked
  `mfa_enrollment_required` that only reaches two-step setup, the challenge,
  reauthentication and sign-out; CLI and Desktop sign-in, Desktop code exchange
  and Desktop refresh fail with `authentication.mfa.enrollment_required`, a Web
  refresh restricts the Session, and turning two-step verification off fails
  with `mfa.required_by_policy`. Preflight adds the `mfa_unavailable` blocker
  and the `role_holders_without_mfa_count` and
  `role_holder_single_factor_session_count` counts, and
  `revoke_existing_sessions` signs out role holders' Sessions that have not
  proved a second factor when the rule is turned on. The rule pauses, with one
  logged warning, while the deployment disables MFA.
- Server: the Access Policy can shorten the idle and absolute Session lengths
  with the nullable `session_idle_minutes` and `session_absolute_minutes`;
  null keeps the deployment default, which stays the ceiling. `GET` also
  returns `session_idle_minutes_default`, `session_idle_minutes_maximum`,
  `session_absolute_minutes_default` and `session_absolute_minutes_maximum`.
  A length outside the deployment bounds is refused as `access_policy.invalid`
  naming the field. New Sessions, activity, refresh and the Desktop exchange
  use the effective lengths, and shortening a length pulls running Sessions'
  deadlines and credentials in without ever extending them.
- Server: the contract publishes three closed code catalogues as components:
  `MailFailureCode` for `public_failure_code` on Mail Deliveries and
  Invitation deliveries, and `AcademicAdministrationBatchErrorCode` and
  `InvitationBatchErrorCode` for batch item `error_code`. A Mail Delivery now
  refuses a failure code outside its catalogue.
- Server: `JobResponse` names who started a Job and what it is about:
  `created_by_user_id` and `created_by_display_name` for an Examination
  Export build, mail rekey or MFA rekey an operator started (omitted for
  recurring and system work), and `subject`, the Exam title, for an
  Examination Export build. `GET /api/v1/jobs` reads them once per page.
- Server: the first page of `GET /api/v1/users`, `GET /api/v1/invitations`,
  `GET /api/v1/jobs`, `GET /api/v1/audits`, and `GET /api/v1/mail/deliveries`
  reports `total` with `total_is_lower_bound`: the items the same filters and
  the caller's visibility match across every page, exact below 10,000 and
  flagged at that bound. Later pages carry neither member; there is still no
  offset paging.
- Server: `GET /api/v1/administration/counts` returns bounded navigation
  counts for an interactive Session: `pending_invitations`, `failed_jobs`
  (failed in the last seven days), `failed_mail_deliveries` (the `failed`
  state), and `open_onboarding_imports`, each stopped at 1,000 with a
  `<name>_is_lower_bound` flag. A count the caller may not list is omitted.
  The baseline gains `jobs_failed_updated_at_idx` and
  `invitations_state_created_at_idx` for these reads.
- Server: the contract publishes the closed server catalogues as enums:
  `JobType`, `JobStage` and `JobErrorCode` on Jobs and Job Attempts,
  `AuditAction` and `ResourceType` on audit events and their filters, and
  `ErrorCode` on every problem response. Tests keep each enum equal to the
  server's catalogue; clients own the labels.
- Server: each `GET /api/v1/academic-units/{academic_unit_id}/members` item
  names the member in `user` (`display_name`, `username`, and `email` only for
  Institution-wide `user.view`) and, for a caller holding `role_binding.view`
  on the unit, lists up to five of the member's current Role Bindings scoped
  exactly to it in `roles_here` with `roles_here_more`.
- Server: each `GET /api/v1/users` item carries a `summary` of who the User
  is: up to three current Affiliations, each with up to two of the User's
  current Academic Units the caller's `user.view` reaches, and, for a caller
  holding `role_binding.view`, up to three current Role Bindings inside that
  authority's scopes with Role and scope names. Each list is followed by a
  count of the rest; bindings outside the caller's scopes are not counted.
  The summary is read in two batched queries per page.
- Server: `GET /api/v1/users` filters the directory by current Affiliation
  with the repeatable `affiliation` parameter (`student`, `teacher`, `staff`,
  `external`) and by current Academic Unit membership, descendants included,
  with `academic_unit_id`. Both narrow the caller's visibility; a unit outside
  a scoped caller's reach answers an empty page.
- Server: `GET /api/v1/audits` takes `created_after` (inclusive) and
  `created_before` (exclusive) RFC 3339 bounds, validated like the self audit
  history's.
- Server: `POST /api/v1/users/{user_id}/disable` accepts an optional JSON body
  `{"reason": "..."}`, trimmed, at most 500 characters and without control
  characters. The reason is kept only in the audit record; `GET /api/v1/audits`
  shows it as `reason` on the disable event to Institution-wide readers, while
  Academic Unit readers and the self audit history never receive it. A request
  without a body behaves as before.
- Server: the four Invitation create bodies and the replacement body take an
  optional `expires_in_days` from 1 to 30. Issuing defaults to 7 days; a
  replacement keeps the replaced Invitation's lifetime in whole days; a value
  outside the range is `invitation.invalid`. Resend keeps the original
  expiry, and batch and onboarding import Invitations stay at 7 days. The
  baseline bounds `invitations.expires_at` between one and thirty days after
  creation.
- Server: `GET /api/v1/onboarding-imports` lists the unexpired onboarding
  imports the caller could read one by one, newest first, filtered by
  repeated `state` values and paged by `limit` and `cursor`. Items are
  summaries without rows; student progressions are not listed. A new
  `onboarding_imports (created_at DESC, id DESC)` index backs it.
- Server: `GET /api/v1/student-progressions` lists the unexpired student
  progressions newest first, filtered by repeated `state` values and by
  `source_class_id`, `destination_class_id`, `source_period_id`, and
  `destination_period_id`, and paged by `limit` and `cursor`; the first page
  reports `total` and `total_is_lower_bound`. A progression is listed only
  when the caller's `academic_progression.manage` authority covers both its
  source and destination Class, as a single read requires. Items and
  `GET /api/v1/student-progressions/{id}` now carry `source_class_name` and
  `destination_class_name`.
- Server: `POST /api/v1/onboarding-imports` takes an optional `file_name`
  query parameter, trimmed and accepted as a base name of 1 to 255
  characters without path separators or control characters, and import reads
  and list items return it as `file_name` when given.
- Server: the OpenAPI contract publishes the closed onboarding import and
  student progression code catalogues as `OnboardingImportFailureCode`
  (`failure_code`), `OnboardingImportPreviewCode` (row `preview_code`), and
  `OnboardingImportRowCode` (row `public_code`), each kept equal to the
  server's catalogue by a test. The server refuses to record a code outside
  its catalogue.
- Server: `GET /api/v1/users/me/audits` takes `order=asc` to read oldest
  first, and its first page reports `total`, the events the filters match,
  exact below 10,000 and flagged by `total_is_lower_bound` at that bound.
- Server: User profiles and the current User context carry
  `profile_picture_kind`, `custom` when the User uploaded their own picture and
  `default` when the generated picture is served, so clients can offer removal
  only when there is something to remove.
- Server: Desktop reauthentication. A DPoP Desktop Session starts
  `POST /api/v1/auth/desktop/reauthentications`, the person confirms it's them
  on the hosted `/reauthenticate/desktop` page with the Desktop Session's own
  password or provider, and the same Session and key redeem the one-use code
  through `POST /api/v1/auth/desktop/reauthentications/complete`, refreshing
  that Session's recent authentication without creating a Session. Adds the
  `authentication.desktop_reauthentication.invalid` and
  `authentication.desktop_reauthentication.denied` error codes and the
  `desktop_reauthentication` browser-authentication and external-login
  purposes. Existing development databases must be recreated.

- Server: `POST /api/v1/users/me/sessions/revoke-others` signs the caller out
  of every other Session while the requesting Session stays signed in, and
  returns `revoked_session_count`. Revoked Sessions record the new reason
  `user_other_sessions`, and items in the caller's own Session list carry
  `current`, true for the Session that made the request. Existing development
  databases must be recreated.
- Server: `GET /api/v1/users/me/mfa` reports `method` (`authenticator_app`,
  while two-step verification is enabled or pending), `enabled_at`, and
  `recovery_codes_generated_at`. `recovery_codes_total` is the size of the
  recovery-code set the User was actually given, used codes included, and is
  0 unless two-step verification is enabled.
- Server: each retention notice from `GET /api/v1/users/me/retention-notices`
  names the `exam_title` of the Exam Revision its Sitting delivered and the
  Sitting's `sitting_start_at` and `sitting_end_at`, for every recipient.
- Server: `GET /api/v1/search` finds Exams by Draft title, Classes by name,
  and Users by directory fields in one request, returning typed `exam`,
  `class` and `user` items (never an email address), at most `limit` (1–20,
  default 5) of each. Each type is scoped and audited exactly as its own list,
  a type the caller cannot list returns nothing, and the request is denied
  only when no requested type is listable. Invalid search input is
  `search.query.invalid`; the operation is under the new Search API tag.
- Server: `GET /api/v1/users/me/tokens/grantable-scopes` lists
  the Personal Access Token scopes the signed-in User may grant now, each with
  a localized title and description, its resource types, and where it may be
  granted: institution-wide or at up to 50 listed Academic Unit roots.
- Server: `GET /api/v1/users/me/audits` lists the signed-in User's own
  account history: sign-ins, sign-outs, password, two-step verification,
  Session, Personal Access Token, profile, email, account state, Role, and
  membership events, whoever performed them, plus the account events the User
  performed on others. Each item has a stable `event` name from a closed
  catalogue, an `actor.kind` of `self`, `administrator`, or `system` that never
  names another person, and the IP address and user agent only for the User's
  own actions. It filters by `created_after`, `created_before`, repeated
  `event`, `status`, and `relation`, and refuses Personal Access Tokens.
- Server: `Mail.SupportAddress` names a help address in every message
  footer, and `Mail.DisplayTimezone` sets the time zone used for message times
  when a recipient has not chosen one.
- Server: every sealed Submission has a short receipt code, such as
  `MBY3-KBMN-EB83-TFW1`, that a candidate can read out. The submit and
  manager-end receipts return it as `receipt_code`, and every exact replay
  returns the same code. Existing development databases must be recreated.
- Server: failed password checks against one account, counted across all
  sources and both its username and email address, delay further logins for
  that account progressively
  (`Authentication.LoginRateLimit.AccountFailuresBeforeDelay` and
  `MaximumAccountFailureDelay`). The delay never refuses a correct password,
  so other people's failures cannot lock an account out.
- Server: Exam Managers can manually suspend an active Attempt before the
  Sitting's scheduled end. A kick after the candidate's lease lapsed records
  that Connection Loss instead and fails with `exam.attempt.connection_lost`.
- Server: revoked and expired Sessions and superseded Session credentials are
  deleted after `Authentication.Sessions.RetiredRetention` (default 30 days)
  by an hourly Job.
- Server: startup refuses to serve when a stored MFA secret is sealed with a
  key missing from `Authentication.MFA`, instead of stranding those
  credentials.
- Server: MFA secret keys can be rotated online. `POST /api/v1/mfa/rekey`
  fences the key ring on the configured primary and starts a durable Job that
  re-seals every live TOTP secret under it; `GET /api/v1/mfa/keys` and
  `GET /api/v1/mfa/rekey/{job_id}` show the key usage, progress and the
  zero-reference proof that a retiring key may be removed. The operator
  procedure is in the maintenance guide.
- Server: `Server.TrustedProxies` names the reverse proxies whose forwarded
  client address and request ID are accepted.
- Server: new passwords are refused when they are a simple pattern, appear on
  a bundled common-password list, or contain the account's username, email
  local part or name, following NIST SP 800-63B. Each refusal has its own error
  code and webapp message.
- Server: `POST /api/v1/authentication-methods/password/change` changes the
  password on a strong recent Session and signs out the user's other Sessions.
- Server: reset, change and enrollment refuse a user's recent passwords
  (`Authentication.Password.HistoryCount`, default 5, 0 disables).
- Server: when an account's email changes, the previous address receives a
  single-use undo link (`Authentication.AccountRecovery.EmailChangeUndoTTL`,
  default 72h, at most 168h). Using it at `/account/undo-email-change`
  restores the address, signs out every Session and is audited.
- Server: `proctor config show` (`--json`) prints the effective configuration,
  file plus environment overrides, with every secret redacted, and names the
  environment variables that replaced a file value.
- Server: `proctor cluster status` (`--json`) lists serving-node leases,
  cluster discovery rows (expired ones included) and the schema version.
- Server: `proctor jobs list` (`--type`, `--state`, `--limit`, `--json`) and
  `proctor jobs show <id>` inspect durable Jobs and their attempts without
  printing payloads.
- Server: `proctor migrate down --steps N --confirm` reverts the newest
  migrations while no serving lease is live. On the pre-release baseline it
  leaves an empty schema.
- Server: Go runtime profiles can be served under `/debug/pprof/` on the
  metrics listener with `Metrics.ProfilingEnabled`. They need the metrics
  bearer token, even on loopback, never include the command line, and are
  never served on the public listener.
- Server: `Database.SlowOperationThreshold` logs a warning, with only the
  operation name and duration, for each store operation that reaches it.
- Server: opt-in OpenTelemetry tracing (`Tracing.*`) exports spans over
  OTLP/HTTP for HTTP routes (named by route template), store operations and
  Job executions. Trace context is continued only from trusted proxies. Log
  lines carry `request_id`, and `trace_id` and `span_id` when tracing is on.
- Server: `Tracing.Headers` (or `PROCTOR_TRACING_HEADERS`, in the
  `OTEL_EXPORTER_OTLP_HEADERS` format) sends authentication headers with each
  trace export, so spans can go straight to hosted backends such as Honeycomb
  or Grafana Cloud. Values are redacted from `proctor config show` and never
  logged, and headers require an `https` endpoint beyond loopback.
- Server: a second SIGTERM or SIGINT during the drain forces it to finish at
  once and `proctor serve` exits 1; a third exits immediately.
- Server: systemd receives `STOPPING=1` at shutdown, `RELOADING=1` and
  `READY=1` around `SIGHUP` reloads, and watchdog keep-alives that stop when
  the node stays unready. The shipped unit sets `WatchdogSec=60s`.
- Build: `make e2e` drives Chromium through first-run setup, login and
  sign-out, MFA enrollment and challenge, a mailed password reset and a mailed
  Invitation against one real `proctor serve` node with disposable PostgreSQL
  and Mailpit. CI runs it after the hermetic gate, and releases wait for it.
- Build: the development stack runs Jaeger 2.22.0 (all-in-one, in-memory)
  and the development collector forwards every span to it, so traces from
  `make run` and the cluster stack can be browsed at `http://127.0.0.1:16686`
  (`PROCTOR_JAEGER_UI_PORT`, `PROCTOR_JAEGER_IMAGE`).
- Build: `make container-scan` scans the runtime image with a pinned Trivy and
  fails on critical or fixable high vulnerabilities. CI scans on relevant pull
  requests and weekly. Releases build the image locally, scan each platform,
  and only then push the scanned archive, digest unchanged, and sign it, so a
  failed scan publishes no image.
- CI: CodeQL analyzes Go and TypeScript, pull requests that add high-severity
  dependency advisories fail, and the webapp's production npm dependencies and
  the documentation toolchain are audited. Both audits fail on high or critical
  advisories; the documentation audit accepts only the two advisories with no
  compatible fix documented in `docs/site/README.md`. The documentation
  toolchain takes joi 17.13.9 and follow-redirects 1.16.2 for two high
  advisories although both releases are under the two-week dependency age; they
  are patch releases used only to build the documentation, and the audit would
  otherwise block every release.
- CI: a DCO check fails a pull request when any of its non-merge commits
  lacks a `Signed-off-by` trailer matching the commit author
  (`build/scripts/check-dco`). Pull requests opened by Dependabot are exempt.
- CI: an opt-in GitHub Pages deployment for the documentation, gated on
  `DOCS_PUBLISH_ENABLED` and `DOCS_SITE_URL`. It runs as the last stage of a
  server release, only after the release is scanned, signed and published, and
  on manual dispatch; not on every push to `main`.
- Project: GOVERNANCE.md, MAINTAINERS.md and CODEOWNERS describe the
  single-maintainer decision, maintainer and release process.
- Docs: operator guidance for backup, restore, upgrade and rollback.
- VFS: `FileSystem.CheckRoot` checks that the backend root (the local root
  directory or the S3 bucket) is reachable without listing it. Every
  `FileSystem` implementation must add it (**breaking**).

### Fixed

- Server: mail deliveries required `target_user_id`, but Invitation mail goes
  to an invitee with no account, so a page holding one failed the contract.
  Exactly one of `target_user_id` and the new `target_invitation_id` is now
  present.
- Server: response members that carry a growing catalogue (error codes, Job
  types and stages, audit actions, resource types, mail, onboarding and batch
  codes, self-audit events, token scopes) accept any string beside the listed
  values, so a client built against an older contract keeps working when a
  value is added. Requests still accept only listed values.
- Server: a User's `last_activity_at` follows their Session activity instead
  of only their sign-ins. Recording Session use or a refresh also advances it,
  at the existing activity throttle and never backwards; `last_login_at`
  still changes only at sign-in.
- Server: the contract declared mail `template_key` as the single value
  `system.mail_test`, so clients validating responses rejected real mail
  deliveries and metrics. A shared `MailTemplateKey` enum now lists every
  template key, and a test keeps it equal to the server's catalogue.
- Server: password history refused reuse only by change time, so when the
  database clock trailed the server that stamped a previous change, two history
  entries could share a time and a recent password could be accepted again.
  Each change is now stamped strictly after the one before, and ties keep a
  fixed order.
- Server: audit, Exam, Exam Manager, Job, Job attempt, mail delivery and
  User Session listings no longer return a `next_cursor` on an exactly full
  final page.
- Cache: the memory backend never evicts a live atomic counter, so a flood
  of new usernames cannot reset another account's login rate limit; new
  counters beyond `Cache.Memory.MaxCounters` (default 100000) are refused
  and the guarded sign-in fails closed.
- Server: PostgreSQL sessions compute interval arithmetic in UTC, so
  invitation and onboarding lifetimes no longer break within a week of a
  daylight-saving change when the database runs in such a zone.
- Server: the metrics listener's TLS certificate is reloaded on `SIGHUP` and
  on file rotation, as the main listener's is.
- Server: concurrent first TLS handshakes for the automatic-certificate
  hostname no longer crash the process (golang/go#80119); the embedded ACME
  client is rebased on `golang.org/x/crypto` v0.55.0.
- Server: a Let's Encrypt renewal attempt that times out is retried instead of
  ending renewal for the life of the process, and each failed attempt is
  logged.
- Server: profile-picture and Exam Resource JPEGs with more than 32 scans are
  rejected before decoding, so a small progressive file can no longer hold a
  content permit for minutes.
- Server: every SQL statement reports a unique, foreign-key, or rejected-value
  violation as a Store conflict, reference, or invalid-input error, including
  writes such as browser and native delivery appends that did not translate
  them, which previously surfaced as unavailable.
- Server: Job history cleanup no longer creates a successor every few seconds
  after an incomplete batch, and skips Jobs that onboarding imports or mail
  deliveries still reference instead of failing on them.
- Server: a mail delivery left Queued or Sending after its Job failed can be
  retried by an operator, and the last Job attempt fails its delivery instead
  of requeuing it with no Job left.
- Server: a Job whose lease expired is requeued behind a backoff, and a Job
  canceled while its handler failed retryably completes as canceled instead of
  staying running until its lease expires.
- Server: one failed Job heartbeat no longer abandons the attempt while the
  lease has slack, and a handler that succeeds as its timeout elapses is not
  re-run.
- Server: a database constraint rejection during Sitting sealing or lifecycle
  work is retried instead of failing that Sitting's Job permanently.
- Server: adding an Exam Manager or granting a Role Binding locks the User
  before the Attempt admission fence, so it no longer deadlocks with an
  Invitation acceptance for the same User.
- Server: retiring a Submission finds the retry outcomes that cite it through
  indexes, so retirement no longer scans every audit event per Submission.
- Server: a retention policy preview counts audit expiry in one set-based pass
  instead of probing every side table per audit event, so previews finish
  within the query timeout at any audit volume.
- Server: cluster messages too large for one UDP packet are queued per peer
  instead of sent inline, so a slow or partitioned peer no longer blocks the
  publishing request for up to 10 seconds per peer. A full peer queue drops
  its oldest message and counts it as `dropped`.
- Server: a `SIGHUP` during startup or migration no longer terminates
  `proctor serve`; the reload is held until the node exists.
- Server: configuration reload is all-or-nothing. When logging or the
  external-provider registry cannot apply a change, the other is restored, the
  running configuration stays in force, and the reload reports failure instead
  of "configuration reloaded". Changed settings that need a restart keep their
  running values and are named in a warning instead of changing silently.
- Server: `proctor migrate status` only reads; it no longer creates the
  migration tables or takes the exclusive migration lock.
- Release image: packaged binaries and webapp are owned by root and read-only
  to the runtime user, and the image declares a `HEALTHCHECK` that runs
  `proctor-healthcheck` against readiness.
- Server: the serving lease keeps renewing during graceful shutdown until HTTP
  has stopped, and a lease failure during the drain force-stops HTTP, so a node
  can no longer keep serving after its fence may have expired.
- Server: re-allowing a focus-loss suspension no longer reports a committed
  re-allow as unavailable.
- Server: a client that disconnects mid-mutation no longer leaves its audit
  attempt open, and an hourly Job fails attempts still open after 24 hours
  with `audit.attempt_abandoned` so retention can expire them.
- Server: a failed audit completion is joined with, not substituted for, the
  original failure.
- Server: rows written with a node's clock are never later stamped earlier by
  PostgreSQL time (or the reverse), which removes intermittent check-constraint
  failures and unreadable Sessions when node and database clocks differ.
- Server: a refused cross-account Desktop approval is audited, and a Desktop
  Session withdrawn after its audited exchange gets its own withdrawal record.
- Server: audit list cursors keep microsecond precision, so rows created in
  the same millisecond are no longer skipped between pages.
- Server: Desktop approval audit binds to the approving account, local password
  login outcomes are durably audited, and result-release audit begins before
  release time is reserved.
- Server: manager Attempt operations authorize before reading Attempts.
- Server: password reset looks up the token before hashing the new password.
- Server: forwarded client identity is trusted only from configured proxies,
  HSTS is sent when the server terminates TLS, and transport refusals answer
  with Problem Details.
- Server: Workspace file content is served with `Content-Security-Policy:
  sandbox`, and HTML, XML, and SVG media types as plain text, so authored
  content cannot render as an application page.
- Server: the inbound WebSocket frame budget is shared by every connection of
  one Session on a node, so reconnecting or opening more connections does not
  grant a fresh burst.
- Server: every inbound WebSocket frame is rate-limited, and HTTP and WebSocket
  share origin normalization.
- Server: candidate answers are no longer spooled to the temporary directory,
  and transfer deadlines are sized from route declarations and, for uploads,
  from the declared request length.
- Server: manager re-allow, reschedule, and cancel use database time; voluntary
  seal lifecycle times stay monotonic; and late focus-loss discrepancies
  invalidate review waivers.
- Server: live renewals queue at the worker cap instead of being refused; the
  live lane validates requests before admission, limits each User to twice the
  renewal cadence, and keeps per-User limits when its identity table is full
  by evicting the least recently admitted User.
- Server: every SQL statement is bounded by the query timeout, rejected-value
  SQL errors translate to invalid input, and the Attempt candidate is locked
  before enrollment fences.
- Server: readiness probes mandatory dependencies, serving-lease renewal
  retries within its slack, oversized cluster envelopes use the reliable
  stream, offline recovery commands no longer migrate the schema, and requested
  shutdown no longer reports closed-listener errors.
- Server: administrator MFA reset notices and retention grace notices are
  delivered instead of failing every attempt.
- Server: profile names containing control characters (including CR, LF and
  tab) are rejected as invalid input on profile update, external identity
  provisioning, onboarding import, and invitation suggestions, instead of later
  blocking every mail-preparing mutation for that User; the pre-release SQL
  baseline enforces the same rule (**breaking** for development databases). A
  Sitting mail fan-out skips an unaddressable recipient instead of stopping.
- Server: an SMTP relay certificate, STARTTLS, or authentication failure
  retries and reports `mail.smtp_outage` instead of permanently failing every
  in-flight message.
- Server: Personal Access Token and retention notice mail honor the mail
  primary-key fence, so a rolling key rotation cannot strand an old-key payload.
- Server: disabled-mail and expiry suppression update a Retention Notice's
  delivery state instead of leaving it queued.
- Server: transactional mail keeps its Outlook conditional comments, so the
  layout renders in desktop Outlook.
- Server: Browser source refusals keep their meaning, so a spent source budget
  reports `exam.browser.source_budget_exhausted` instead of the generic Attempt
  conflict; `errors-check` rejects Store tables that map constraints the Store
  never produces.
- Cache: counter expirations are fixed windows.
- Server: `server/go.mod` pins the current cache, mail, and VFS modules, so
  `GOWORK=off` builds include the fixed-window counters and the other module
  fixes instead of August snapshots.
- VFS: a local listing stops once its page is full instead of walking and
  sorting the whole tree under the lock, and the server readiness probe checks
  the VFS root instead of listing, so a large local tree no longer times out
  readiness or stalls writes.
- VFS: S3 part size is bounded for unsized writes.
- VFS: delimited S3 listings return common prefixes in key order among
  objects, so a page and its cursor no longer skip prefixes that sort before
  the page's last object.
- Mail: relay TLS, STARTTLS, and authentication failures classify as
  temporary rather than permanent.
- Server: file purge shares each batch between the FileRevision, Starter
  Workspace, and Attempt Workspace phases and continues through successor
  Jobs while work remains, instead of 50 objects a day in which a FileRevision
  backlog starved workspace cleanup.
- VFS: the local backend fsyncs parent directories after publishing, moving,
  or removing a file and after creating directories, so a completed operation
  survives a crash.

[Unreleased]: https://github.com/sudosylabs/proctor/commits/main
