# Proctor documentation site

This private Docusaurus package renders the task-oriented content in
[`../public/`](../public/) and the API reference rooted at
[`../api/`](../api/). Repository skills, component contracts, code, and tests
remain authoritative for internal behavior.

The current local build provides task-oriented navigation, public content
validation, contextual guide navigation, and a deterministic
generated API reference. A GitHub Pages deployment workflow exists but stays
inert until maintainers activate it as described in
[the documentation workspace guide](../README.md#publishing). The production
hostname, content licensing, versioning, and analytics remain explicit
decisions to settle before activation.

The search dialog uses a generated local index covering authored public and API
guides, canonical glossary terms, OpenAPI product areas, and every operation.
It performs no remote request and sends no query off the page.

## Navigation and reading layout

Each guide owns one sidebar in `sidebars.ts`. Keep pages in task order within
that guide; do not add another audience's page as a `doc` item. Cross-guide
links belong in prose or global navigation so pagination stays within a guide.
The guide title links to its overview and cannot be collapsed; deeper sections
may collapse and Docusaurus opens the active page's ancestors automatically.

`navigation.mjs` holds the shared guide destinations, short descriptions,
reference links, and audience labels used by the homepage, navbar, search, and
article metadata. Add a guide there and give it a sidebar owner in the same
change. `npm run test:navigation` checks that every public page has exactly one
owner, entry points resolve, and no audience falls back to an internal key.

The layout adapter uses Docusaurus' configured base URL to distinguish the
homepage from the reader. Scoped CSS owns reading widths; avoid selectors
against generated CSS-module names. The homepage is a compact guide directory,
not a second description of product architecture.

The header carries the flat Proctor Docs lockup: purple mark and ink lettering
on the light theme, all white on the dark one. Browser icons use the 2.5D mark
and follow the browser's color preference independently of the site's reading
theme. These are reviewed package-local assets; their tests
validate the bundled copies without reading repository brand masters.

The tracked [documentation visual-system reference](../../.agents/skills/docs-site/references/design-system.md)
owns the brand palette, semantic colors, Geist typography, spacing,
geometry, and illustration grammar. Human changes begin in
`design-system/tokens.mjs`; `src/css/tokens.css` is generated:

```sh
cd docs/site
npm run generate:design-system
npm run check:design-system
npm run test:design-system
npm run generate:glossary
npm run check:glossary
npm run test:glossary
npm run generate:search
npm run check:search
npm run test:search
npm run test:navigation
npm run generate:technical-reference
npm run check:technical-reference
npm run generate:problems
npm run test:problems
```

The check rejects stale generated CSS, literal colors outside the token module,
retired color names, unsupported font weights, insufficient standard-token
contrast, and illustration palettes that do not match their declared visual
system.

`src/components/DocIcon` owns the bounded documentation icon vocabulary. Its
typed names, three optical sizes, shared two-pixel geometry, and accessibility
contract keep domain and utility symbols consistent without an external icon
library. Add a name only when the concept recurs and update the icon contract
and its test in the same change.

The repository [`glossary` skill](../../.agents/skills/glossary/SKILL.md) is the
single domain-language authority. `generate:glossary` produces the public
glossary page and the typed runtime lookup used by explicit `<Term>` tooltips.
The check rejects stale generated views, unknown identifiers, repeated
annotations, and tooltip markup inside headings or code.

The search module reads human-authored MDX plus `server/openapi.json` and hides
route derivation, text extraction, duplicate detection, and TypeScript adapter
generation behind `generate:search`. Regenerate after public content or OpenAPI
changes; `check:search` rejects a stale index.

`scripts/technical-reference.mjs` generates the complete environment-variable
inventory from `server/config/environment_overlay.go` and checks the authored
command-line reference against the command constructors and flags in
`server/cmd/proctor/commands`. Regenerate after adding, removing, or renaming an
environment override. The command-line page remains human-authored because it
must explain intent, consequences, and recovery rather than merely repeat Cobra
help; `check:technical-reference` catches drift in both sources. Never edit the
generated environment-variable page by hand.

`validate:routes` checks every Proctor `/api/v1` path written in authored public
or API guidance against the reviewed OpenAPI path and method set. It understands
documentary placeholders and deliberately ignores API-shaped paths on explicit
external origins such as the local Prometheus API. Generated endpoint pages are
excluded because their paths already come directly from OpenAPI.

## Error-code pages

`problems/index.mjs` renders one page per code in
`server/httpapi/route/error_catalog.yaml` into the ignored `docs/problems/`
content root, served at `/problems/<code>`. The catalog owns the code, status
and meaning; `server/openapi.json` owns which endpoints declare it;
`server/i18n/en.json` supplies the example title and detail. Causes and fixes
come from the general advice for the code's kind of error unless
[`docs/api/problem-guidance.yaml`](../api/problem-guidance.yaml) has a written
entry for it. `npm run generate:problems` runs before `start` and `build` and
rejects guidance for a code the catalog does not contain.

## Commands

From the repository root:

```sh
make docs-start
make docs-check
```

`docs-start` serves a local preview. `docs-check` validates the documentation
design system, frontmatter, and governed visual-asset registry, proves
the generated OpenAPI artifact matches its human-authored YAML modules, audits
the reference data, tests the audit and renderer failure modes, regenerates and
verifies endpoint pages, type-checks the site, synchronizes the artifact, and
performs a strict production build.

To inspect the OpenAPI data without building the site:

```sh
cd docs/site
npm run audit:openapi
```

The command writes a versioned JSON report containing exact operation, schema,
tag, description, parameter, request-body, mutation-example, response-example,
code-sample, and Proctor-extension coverage. It fails on duplicate operation
IDs, unknown or multiple tags, short behavior or tag descriptions, missing
parameter/body purpose prose, mutations without synthetic request examples,
product areas without representative success or Problem Details examples, and
missing required extensions. The parameter-description exception list is
explicit and currently empty.

## Generated files

The human authoring workflow lives in
[`server/openapi/README.md`](../../server/openapi/README.md). Run `make -C
server openapi-build` after changing a route, description, example, or schema.

`scripts/sync-openapi.mjs` copies the generated and reviewed
`server/openapi.json` contract to
the ignored `static/openapi/openapi.json` publication path. The generated copy
is never edited or treated as an authority.

`scripts/generate-api-reference.mjs` renders that same contract into the
ignored `docs/api/reference/` directory: one page per operation, one page per
product area, and the sidebar. `api-reference/index.mjs` owns the reading
model. It turns schema constraints into plain rules ("26-character Proctor
ID", "1 to 128 characters") while keeping the exact pattern beside them,
groups each operation's `x-proctor-error-codes` by status with their catalog
meanings, and builds curl, JavaScript, Python and HTTP samples. Samples use
the contract's own request example; a response without one gets an example
built from its schema. `src/components/EndpointPage` and `AreaPage` render the
pages. Page IDs are the lodash-style kebab case of each `operationId`, so
endpoint URLs and search links stay stable. `api-reference/verify.mjs` fails
generation unless every operation, area and sidebar entry appears exactly once
and every declared error code is rendered.
The authored `docs/api/index.mdx` overview remains small;
route descriptions and schemas stay with their human-owned OpenAPI YAML
fragments.

The OpenAPI source modules now own the complete product-area taxonomy and
explicit authentication, error-code, idempotency, behavior, parameter, request
body, and example metadata for every operation. The full-content gate covers
public and authenticated JSON, strong recent assurance, optional and required
idempotency, keyset pagination, raw image upload, protected binary download,
candidate multipart upload, submission, and WebSocket upgrade. Representative
success and redaction-safe Problem Details examples exist across every
applicable product area.

`scripts/generate-asyncapi-reference.mjs` renders the realtime protocol from
the reviewed `server/asyncapi.json` into the ignored
`docs/api/reference/realtime-protocol/` directory: an overview of the event frame,
commands, and close codes, one page per message, and the sidebar that
`sidebars.api.ts` places under "Realtime Reference". It also copies the
artifact to the ignored `static/asyncapi/asyncapi.json` download path. The
search index reads the same artifact. Run `make -C server asyncapi-build`
after changing `server/asyncapi/` or `server/schemas/realtime/`;
`npm run test:asyncapi-reference` checks that every message has one page and
one sidebar entry.

The reference UI deliberately disables request sending. It documents the
contract and produces client-ready examples without turning a documentation
deployment into a credential-bearing API console.

The package-local `.npmrc` disables dependency lifecycle scripts. Re-enable
them only after reviewing every dependency that would execute.

## Dependency advisories

Every package in this site runs at build time; the published output is static
files. `npm audit --omit=dev` still covers the toolchain because a
compromised or crashing build dependency affects what is published.

The `package.json` overrides hold patched transitive releases that the
upstream declarations do not yet admit:

- `tinypool` 2.2.0 for Docusaurus' static-rendering worker pool, the version
  line the next Docusaurus major already declares;
- `postcss-selector-parser` 7.1.7 under the cssnano 6 minifier chain, which
  otherwise resolves an unpatched 6.x release; and
- `serialize-javascript`, `uuid` and `yaml` at patched releases.

Remove an override once its upstream declares a patched range. The production
build, which renders through the worker pool and minifies every stylesheet,
covers their runtime compatibility.

`npm audit --omit=dev` reports no high or critical advisory, and the
vulnerability workflow accepts none: `build/ci/npm-audit.mjs` fails on any
high or critical advisory. Accepting one means adding its GHSA ID to the docs
audit step in `.github/workflows/security.yml`, explaining here why no
compatible fix exists, and updating `build/ci/npm-audit.test.mjs`. Re-run the
audit after each Docusaurus upgrade.

## Image boundary

Public pages cannot add Markdown images, raw `img` elements, or image imports;
`npm run validate` enforces that boundary. It was introduced while
Docusaurus' authored-image parser depended on an unpatched `image-size`
release. That dependency now resolves to a patched version, but the boundary
also keeps every content image inside the governed review below, so it stays
until maintainers decide otherwise.

Reviewed SVG and PNG content assets instead use the constrained static route
governed by the repository
[`docs-site-visual-assets` skill](../../.agents/skills/docs-site-visual-assets/SKILL.md).
MDX supplies only a registry ID to `GovernedFigure`, so Docusaurus' authored
image parser never reads those files. The asset gate independently permits only
the two reviewed formats and checks inventory, ownership, provenance, license
state, privacy review, dimensions, size, safe SVG structure, references, and
review triggers. Each visual approval is bound to the asset's SHA-256 and a
desktop/mobile acceptance checklist, so an edited illustration cannot inherit
an earlier review silently. Do not remove the governed asset boundary.
