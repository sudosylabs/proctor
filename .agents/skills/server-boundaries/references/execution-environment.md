# Execution environment rules

Read this before building the terminal plugin, its seams, the batch workspace
write, or any execenv change it needs. The design and its reasons are in
[`docs/public/developers/server/execution-environment.mdx`](../../../../docs/public/developers/server/execution-environment.mdx).
This file holds only the rules that must not drift.

## Authority

- The Attempt Workspace is the only authority for candidate files. The grant
  is a projection, and it always converges to the Attempt Workspace; the first
  commit wins.
- The core never waits on the plugin at admission or at the Submission seal.
  The plugin flushes before a voluntary submit and drains before the deadline.
  Anything it misses is a recorded gap.
- The plugin writes only through the core use case bound to one Attempt and
  its current Participation generation. A harvested change is written on the
  candidate's behalf, with origin `environment`, and audited as the candidate;
  there is no plugin principal. The plugin never touches stores directly: it
  keeps its own state in the core record store and holds work under core
  leases.
- Plugin routes live under `/api/v1/plugins/{plugin_id}/` and run only after a
  core guard; plugin Exam settings are a plugin section frozen into the
  Revision, and admission never calls plugin code to read them.
- Every harvested write and every evidence record is checked against the
  Attempt's current binding to grant, epoch and incarnation. Grant IDs and
  epochs are never reused.

## Files

- There is one write path into the Attempt Workspace: Apply Workspace Batch,
  1–128 mutations, all or nothing.
  - The editor and the environment both use it.
  - Never add a per-file transaction path beside it.
  - A batch is one access check, one idempotency outcome and one audit pair
    (counts, bytes, cursor range, origin; never paths) and one realtime hint.
- Harvest is coalesced per path, skips changes whose SHA-256 matches the current
  content, and is bounded by per-Attempt and per-node budgets.
- `.proctorignore`:
  - It follows the git rule: patterns stop only new paths; already-synced paths
    keep syncing.
  - It always syncs itself.
  - Its Attempt Workspace copy decides what is stored. The guest filter is only
    an optimization.
  - Without the file, or while it does not parse, Proctor's default patterns
    apply. No file is ever seeded into a Starter or Attempt Workspace.
  - execenv never hardcodes its name.

## Transport and evidence

- PTY bytes never travel over cluster peer delivery. Any node may serve a
  terminal by resuming through execenv. One leased owner node runs each grant's
  lifecycle and its file sync.
- The process log comes from the guest agent outside the workload. Terminal
  input is recorded only when the Exam opts in, is captured server-side, and
  never includes output. Both are sealed evidence that only reviewers see.
- Shell history, the scratch home and ignored paths are never stored.
- PTY bytes, file bodies, command lines and tokens never enter logs or audit.

## Compatibility

Proctor and execenv keep no backward compatibility. When this design replaces
a contract, delete the old one in the same change.
