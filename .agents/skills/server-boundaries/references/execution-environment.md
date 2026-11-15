# Execution environment rules

Read this before building core execution: the Attempt Environment, its owner
and processes, terminals, execution evidence streams, Apply Workspace Batch,
Workspace authors, or any execenv change they need. The design and its reasons
are in
[`docs/public/developers/server/execution-environment.mdx`](../../../../docs/public/developers/server/execution-environment.mdx).
This file holds only the rules that must not drift.

## Ownership

- Execution is an optional core feature, not a plugin. Do not route it
  through plugin records, leases, sections, routes or capabilities, and do
  not add seams to the plugin host for it. `server/plugin/execution` is
  deleted when the core environment lands.
- Design every seam for several actors. A new actor (an assistant, a grader)
  is a new author kind and a new process tag, never a new write path, a new
  terminal model or actor-specific code in the Workspace.

## Authority

- The Attempt Workspace is the only authority for candidate files. The guest
  is a synced copy; journal order decides every conflict.
- Admission and the Submission seal never wait on an environment. A settle
  (freeze, drain, harvest) runs before each seal within its budget; anything
  it misses is a recorded settle gap, and the seal proceeds.
- One Attempt Environment per Attempt, across Participations. It is driven by
  Attempt facts, never by a client.
- One owner per environment, under a fenced runtime lease. Every core write
  the owner makes carries the fence; the store refuses a stale one. Owner
  state lives in the environment row, never in memory alone.

## Files

- Apply Workspace Batch is the only write path: 1–128 mutations, all or
  nothing, for every author. Never add a per-file or per-author path beside
  it. A batch is one access check, one idempotency outcome, one audit pair
  (counts, bytes, cursor range, author; never paths) and one change
  notification.
- Every journal entry, batch audit and evidence record names an author: kind
  and reference. Every author acts on behalf of the candidate; the kind only
  adds checks (a live Connection, a live environment fence).
- A stale expected version is refused, never merged. A losing guest change is
  re-projected from the Workspace and kept as `execution.overwritten`
  evidence. Harvest batches group only dependent changes.
- The harvest plans against the core's Workspace state; never keep a private
  manifest copy or a second implementation of Workspace rules.
- Owners wake on post-commit notifications, execenv watches and long-polled
  observation. Do not add polling loops; a slow backstop is the only timer.
- Write budgets are per Attempt and author kind; quotas stay per Workspace and
  bind every author.
- `.proctorignore`:
  - It follows the git rule: patterns stop only new paths; already-synced
    paths keep syncing.
  - It always syncs itself and is submitted.
  - Its Attempt Workspace copy decides what is stored. The guest filter is
    only an optimization.
  - A new Workspace that uses execution gets a default copy; while the file is
    missing or does not parse, the default patterns apply.
  - execenv never hardcodes its name.

## Transport and evidence

- PTY bytes never travel over cluster peer delivery. Any node serves a
  terminal by attaching to the grant itself. Input is written by offset and
  accepted only from the process's author.
- Execution evidence goes into Attempt-owned, append-only evidence streams of
  typed kinds. Appends after the Submission are refused, except the settle's.
- The process log comes from the guest agent outside the workload. Terminal
  input is recorded only when the policy opts in, is captured at the serving
  node, and never includes output. Both are sealed evidence that only
  reviewers see.
- Shell history, the scratch home and ignored paths are never stored.
- PTY bytes, file bodies, command lines and tokens never enter logs or audit.
- Gaps and overwritten changes are review context, never Flags.

## Compatibility

Proctor and execenv keep no backward compatibility. When this design replaces
a contract, delete the old one in the same change.
