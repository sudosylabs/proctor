# Academic structure, files, user settings and mail

## Academic structure

- **Structural rules:**
  - Academic Units form no cycles.
  - A Programme has one Unit, and a Programme Level has one Programme.
  - An Academic Period belongs to the Institution or to one Unit, and covers
    that Unit's subtree.
  - A Class has one Programme Level and one exact applicable Period.
  - A student has at most one active Class per Period.
  - Progression and transfer keep history.
- **There is no inferred "current period".** Progression names exact Periods,
  and Period dates may overlap.
- **IDs:** opaque random 26-character z-base-32 strings, one type per entity.
  The zero value is invalid, and an ID encodes no order or meaning.
- **Revisions:** they exist only on mutable, conflict-prone aggregates.
  Timestamps are never concurrency tokens.
- **Models:** they use constructors, named transitions and `Validate() error`.
  Closed vocabularies answer `IsValid() bool`. Domain validation does no I/O.

## Files and workspaces

- **Ownership:** file tables never use a polymorphic
  `owner_type`/`owner_id`. Owners use typed tables with real foreign keys, or
  authorization ends up trusting unverified IDs.
- **Revision states:** a revision is Pending, then Available, Quarantined or
  Rejected. Only Available is served or indexed.
- **Deletion:** an acknowledged backend delete is not proof of absence.
- **Workspace Paths:** logical metadata, in case-sensitive POSIX form.
  - At most 16 segments of 255 bytes each, and 1,024 bytes in total.
  - No `.proctor` root.
  - Empty directories are metadata, and a rename never moves an object.
- **No spooling of candidate bytes:** they never touch a local spool.
  Resource and export spools are 0600 files, unlinked at creation.
- **Environment writes:** an execution-environment write is the candidate's
  own work. It is proven by the owner's lease and incarnation, and audited as
  the candidate.
- **Retention:**
  - a zero retention period means "never expires", not "delete now";
  - saving a policy never approves deletion;
  - removal never promises that backups are erased.

## User Settings

- **The document is presentation only.** It never grants a capability or
  weakens policy. A workspace `.vscode/settings.json` has no authority, and
  the server never reads `locale` or `timezone` from the document.
- **Exact bytes:** the server keeps the exact bytes (the initial source is
  `{}\n`) and never normalizes or migrates them. The client owns the key
  registry.
- **Newer formats:** a newer persisted format is read-only
  (`writable: false`).
- **No echoes, no overwrites:** diagnostics never echo the source, conflicts
  never return it, and there is no last-write-wins.
- **A missing row is an integrity failure.** Reads never write, and every
  User-creation path inserts the row atomically.

## Transactional mail

- **Mandatory only:** mail is mandatory communication, so there are no
  opt-outs, digests or reminders. Live exam changes go over realtime.
- **Never inside a business transaction:**
  - mail is never sent inside a business transaction;
  - the occurrence, delivery and Job commit with the mutation and its audit;
  - enqueuing mail after the business commit is forbidden;
  - if enabled mail can't be prepared, the mutation rolls back.
- **One command, one notice.** One user-visible command sends one notice.
- **Templates:**
  - use cases ask for semantic outcomes, and the registry picks the key, Job
    class and lifetime;
  - templates get typed data;
  - no external images, fonts, tracking or attachments.
- **Delivery semantics:** "accepted" means SMTP accepted the DATA, never inbox
  delivery. Exactly-once delivery is not promised. The Message-ID is stable
  across retries.
- **Content limits:**
  - mail never contains answers, workspace content, scores, evidence, codes,
    IPs or tokens;
  - a result mail says only that a result is available;
  - an admin actor is "an administrator".
- **No action buttons:** ordinary mail has no action button, and protected
  API paths are never link destinations.
- **Enabling and disabling:** mail is off by default. Starting with mail
  disabled destroys outstanding work, and re-enabling never resurrects it.
- **Copy and localization:**
  - MJML generates the tracked HTML;
  - copy is flat, sorted JSON in `server/i18n`;
  - English is complete;
  - other locales can't add IDs or change placeholders.
