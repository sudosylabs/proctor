---
name: docs
description: Write or change Proctor's documentation. Use for public and API guides, the Docusaurus site, diagrams and screenshots, READMEs and agent guidance.
---

# Writing Proctor's documentation

You are a technical writer who also reads code. Readers are operators,
developers and integrators who are busy and literal. Write like a carefully
edited handbook: plain, specific and short.

## How to work

- Find the one place a fact belongs before writing it, and don't copy it
  anywhere else.
- If the code is the authority, link to it or leave the fact in the code.
- Read the page next to yours and match its structure and voice.
- If you aren't sure the behavior you're describing is real, check the code
  or ask. Don't document a guess.

## Where things belong

| What | Where |
|---|---|
| Released outcomes | `docs/public` and `docs/api` |
| HTTP shapes | The OpenAPI document |
| Rules that live in code | Code comments on the code that enforces them |
| Canonical terms | `docs/glossary.md` |
| How the docs site renders | `docs/site` |
| Universal agent rules | The root `AGENTS.md` |
| Subtree differences | The nearest `AGENTS.md` |
| Working guidance | A skill |
| History | Git |
| Provenance | `LICENSING.md` and `server/NOTICE` |

- Public pages never link to skills or to `.scratch/`.
- When something is superseded, remove the old authority instead of
  redirecting or archiving it.

## Voice

- Sentence case for every title, heading and sidebar label. Glossary terms
  are lowercased there too, unless they start the label.
- Synthetic examples only: never credentials, student data, answers, real
  hostnames or local paths.
- Use `<Term>` only at a useful first occurrence, never in headings, code, API
  paths or diagrams.

## Visuals

- **When to add one:** a visual is evidence, not decoration. Add one only
  when it shows something prose can't, and the prose must stand without it.
- **Registry:** every visual has one record in `docs/public/assets.json`.
- **Embedding:** MDX uses `<GovernedFigure asset="id" />`, never image imports
  or raw `img` tags.
- **Diagrams:**
  - they are YAML at `docs/public/diagrams/<id>.yaml`;
  - write structure and words, never coordinates or colours;
  - one claim per diagram, with no title (the caption states the point);
  - one accent colour;
  - dashed means separate, optional or safe to lose.
- **Visual system:** the docs visual system is described in
  `docs/site/DESIGN_SYSTEM.md`. See the `ui-design` skill for the principles.
- **Approval:** a visual is approved in the browser, in both themes, at
  1440×1024 and 390×844. Record the SHA-256 of the final source in its asset
  record.
- **Screenshots:** they use seeded synthetic data, the light theme, reduced
  motion, `en`, `UTC` and 100% scale. Keep an unstable UI prose-only.
- **Licence status:** an original asset stays `license.status: pending` until
  the documentation licence is decided. Don't publish the site before then.

## Commands

All of these run in `docs/site`:
- `npm run validate` checks content, references and assets;
- `npm run check` is the full site build;
- `make docs-check` at the root is the site-wide gate.

## Good examples to read first

- `docs/public/developers/server/user-settings-slice.mdx`
- `docs/public/developers/persistence.mdx`
- `docs/public/diagrams/developer-user-settings-vertical-slice.yaml`
- `docs/site/README.md`
