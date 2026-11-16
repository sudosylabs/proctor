# Exams, sittings, attempts and integrity

These are rules the code depends on but does not explain.

## Authoring and delivery

- **What an Exam is:** identity and lifecycle only. Title, instructions and
  policy live in its one Draft and its immutable Revisions. The Class,
  schedule and pause belong to the Sitting.
- **Live corrections:** a live correction creates a live-correction Revision
  and retargets only that Sitting. The policy and Starter digests must match,
  so only instructions, Resources and Browser Policy can change. A Sitting
  takes at most 32 corrections.
- **Management:** it needs both a current Exam Manager relationship and the
  scoped permission.
  - Admin override is explicit and audited. It is never membership, and it
    bypasses no structural rule.
  - A deferred constraint keeps the owner a Manager.
- **Sitting states:**
  - Scheduled, then Open (which toggles with Paused), then Closing, then
    Closed;
  - or Scheduled, then Canceled.
- **The deadline:** `ScheduledEndAt` is the only deadline, and a pause does
  not extend it. PostgreSQL time wins races. Superseded Jobs reread state and
  finish as no-ops.
- **Policy defaults** are copied into new Drafts, with no live inheritance. An
  unsupported policy shape denies admission rather than falling back to
  defaults.
- **Exam Markdown** is untrusted. Sanitize it through `app/exam/safemarkdown`,
  without moderating its meaning.

## Attempts and presence

- **First entry:** it lazily creates one Ready Attempt and copies the Starter
  Workspace by logical copy-on-write; no bytes are copied.
- **Class membership** is checked on every connection, not polled during one.
  A Kick is the immediate control.
- **Presence leases:**
  - presence is an explicit, authenticated renewal every 5 seconds of a
    20-second lease;
  - pings and timers never renew it;
  - only the SHA-256 of the continuity credential reaches the Store.
- **Connection loss:**
  - one failed request or ping is not Connection Loss; confirmed lease expiry
    is;
  - expiry is exclusive (`expires_at <= now`);
  - Connection Loss always applies `FlagAndSuspend`, which an Exam Manager
    cannot weaken;
  - candidate copy never mentions leases or generations.
- **Kicks:**
  - a Kick is a control, not suspicion, so it raises no Flag;
  - a Kick on a lapsed lease becomes Connection Loss, so a Kick can't erase
    evidence;
  - re-allowing keeps the evidence and starts a new generation.
- **The execution environment** outlives Participations. It freezes when each
  one ends and thaws at the next generation, and only the Submission ends it.

## Submission, integrity and review

- **A Submission** is terminal and single. Its manifest pins entries, paths,
  versions and checksums without copying bytes. Candidates get a receipt only.
  Managers never read a live workspace, only a sealed Submission.
- **The receipt code** is 80 random bits, written as 16 Crockford base32
  characters. A replay returns the stored code. It is neither an ID nor a
  selector.
- **Export containment:** candidates get no download, export, print, URL,
  local folder, drag-out or external open. The promise is export
  containment, not a claim that content never reaches the device.
- **The integrity chain:**
  - it runs Native Fact → Rule Status → Occurrence → Evidence → Flag (for
    enforcing rules only) → Review;
  - client clocks and severity are never authority;
  - there is one open Flag per Attempt, kind and generation, with at most 100
    evidence rows and counts beyond that.
- **Result release** is a separate, one-way `submission.release`. Until then
  the result is concealed as not found. Late data reopens only the current
  aggregate, never sealed work.
- **Plugin domain events** are written by database triggers inside the
  change's own transaction. No plugin code runs before commit.
- **A new state transition needs all of these:**
  - an owner;
  - an authorization resource;
  - an audit intent;
  - an idempotency rule;
  - a concurrency rule;
  - a replay result.
