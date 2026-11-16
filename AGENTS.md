# Proctor agent guide

Proctor is an open-source, self-hosted examination and proctoring platform.
One installation serves one educational institution. Several nodes sharing
state are one installation, not tenants: don't add tenant columns or routing
without an explicit architecture decision.

People will judge this project by its code. Work like an experienced engineer
building a real product: read before you change, ask when intent is unclear,
and prefer the solution that fits what's here over the most general one.

## The repository

- **Four independently versioned Go modules:** `packages/cache`,
  `packages/mail`, `packages/vfs` and `server`. `go.work` joins them for
  development, but each must build and test alone. Developer tools are pinned
  in `build/tools`, outside `go.work`.
- **The reusable modules** under `packages/` never import the server or expose
  its types.
- **`webapp/`** is the server-hosted product UI, and `docs/` is the
  documentation and its Docusaurus site.
- **Canonical domain terms** live in [`docs/glossary.md`](docs/glossary.md).

## Rules that always hold

1. **Business logic** is independent of HTTP, WebSocket, SQL, Redis, SMTP, VFS
   and Memberlist. `server.New` is the only composition root.
2. **PostgreSQL** is the authority for durable state. Caches and cluster
   messages can be lost and rebuilt.
3. **Durable state commits** before cache invalidation, cluster fan-out,
   realtime or mail.
4. **Authorization** belongs to the use case. Authentication gives an
   immutable principal; the use case checks the action on the resource.
5. **Every route** declares its credential and assurance requirement.
6. **Security-sensitive changes** are audited, and fail closed where the
   audit can't be written.
7. **Credentials, secrets, student data, exam answers and unbounded user input**
   never reach ordinary logs or unsafe audit fields.
8. **One node** is the degenerate form of the clustered architecture.
   Horizontal scaling is never commercially gated.

## Licensing and provenance

- **Licences:** `packages/` is Apache-2.0; `server/` and `webapp/` are
  AGPL-3.0-only (see [`LICENSING.md`](LICENSING.md)). Every code file carries
  the copyright header and SPDX identifier.
- **Mattermost:** [Mattermost](https://github.com/mattermost/mattermost)'s
  open-source repository is an eligible reference.
  - A direct or substantial adaptation records the exact upstream revision,
    path, licence and nature of the change in `server/NOTICE` in the same
    change, and keeps the required notices.
  - Code written independently and only informed by upstream behavior carries
    the plain Proctor header.
  - Source Available or commercial Mattermost material needs explicit
    permission.
- **`.scratch/`** is ignored and ephemeral. Tracked files never link to or
  depend on it.

## Working habits

- **Shared tree:** check `git status` first, and keep work that isn't yours
  intact. Never commit `go.work.sum`.
- **Test what you touched.** Run the narrowest tests, vet and lint for the
  packages you changed. CI runs the full gates.
- **Report honestly:** what you verified, what you didn't, and what you
  decided.

## Skills

The skills in `.agents/skills/` are general guidance for when the task gives
no specific direction:

| Skill | Use for |
|---|---|
| `go-server` | Go server code |
| `persistence` | Storage |
| `api-design` | HTTP, WebSocket and OpenAPI |
| `domain` | What Proctor's concepts mean |
| `ui-design` | Anything a person sees |
| `docs` | Documentation |
| `git-workflow` | Commits and pull requests |

`server/AGENTS.md` adds what differs inside the server.
