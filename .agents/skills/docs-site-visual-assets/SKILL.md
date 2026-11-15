---
name: docs-site-visual-assets
description: Add or revise governed diagrams and screenshots for Proctor's Docusaurus documentation. Use for docs/public/assets.json, docs/public/diagrams, privacy review, deterministic capture, or visual acceptance; not for webapp brand or UI assets.
---

# Govern documentation visuals

## Workflow

1. Confirm that a visual materially clarifies a relationship that prose does
   not make equally legible. Completion: the intended claim and owning public
   page are explicit.
2. Read the shared
   [visual-system reference](../docs-site/references/design-system.md), in
   particular its diagram grammar. Completion: structure, accent, and wording
   follow its rules.
3. Create or update one registry record and its exact source under the
   governed paths below. Completion: ownership, provenance, privacy, review
   triggers, and accessibility text are complete.
4. Perform the visual acceptance review against the final source. Completion:
   all five checks and the final SHA-256 are recorded.
5. Run the asset checks listed below. Completion: validation and failure-mode
   tests pass without weakening the registry contract.

Visuals in the public documentation are evidence, not decoration. Add one only
when it makes an architecture, lifecycle, ownership, or UI procedure materially
easier to understand than prose alone. The surrounding prose must remain
sufficient in print and wherever the visual is unavailable.

## Authoring boundary

Every content visual has one entry in
[`docs/public/assets.json`](../../../docs/public/assets.json):

- a **diagram** owns one YAML layout at `docs/public/diagrams/<id>.yaml`,
  drawn on the page by the site's `Diagram` component in the site's own type
  and colours, so it follows the light and dark themes and stacks on phones;
- a **screenshot** owns one PNG beneath `docs/public/static/assets/`.

Authored Markdown and MDX do not import files, use Markdown image syntax,
create raw `img` elements, or write direct `/assets/` paths. Reference the
stable registry ID instead:

```mdx
<GovernedFigure asset="installation-authority-topology" />
```

`GovernedFigure` resolves the diagram or image, alt description, dimensions,
and caption from the registry. The validator rejects unregistered and orphaned
files, duplicate IDs, missing ownership or provenance, unapproved privacy
reviews, diagram sources the grammar cannot draw, screenshot dimension and
size drift, visual-review hash drift, unknown references, and missing review
triggers.

## Diagrams

Write a diagram as structure and words; the renderer owns every coordinate and
colour. The grammar and its validator live in
[`docs/site/diagrams/index.mjs`](../../../docs/site/diagrams/index.mjs), and
the rules for accent, dashed lines, wording, and density live in the
visual-system reference. A registry record for a diagram sets
`source: diagrams/<id>.yaml`, `visual_system: proctor-diagrams-v2`, and
`theme: site`. The caption states the point in one plain sentence; the alt text
describes the structure for a screen reader.

## Registry record

Use lowercase kebab-case IDs. A record owns:

- the diagram source, or the screenshot's path, public path, dimensions and
  maximum bytes;
- the documentation owner, provenance, and licensing state;
- a privacy decision with its evidence;
- useful alt text and a concise visible caption;
- the intended theme and review date;
- the visual-system version used by a diagram;
- repository files whose changes require the visual to be reviewed; and
- a visual acceptance record tied to the exact source SHA-256.

An original asset may record `license.status` as `pending` only while Proctor's
documentation-license production decision remains open. The note must explain
that state. Do not assign a license expression by assumption, and do not publish
the documentation site until that decision is resolved. Adapted work must
instead identify its exact source revision and satisfy the repository's
licensing and notice rules.

Run the asset checks from `docs/site`:

```sh
npm run test:diagrams
npm run validate:assets
npm run test:assets
```

The normal `npm run check` and root `make docs-check` workflows include them.

## Visual acceptance review

An asset is not approved merely because its source parses. Review it in a
browser inside the documentation page that uses it, in the light and dark
themes, at the standard 1440 × 1024 desktop and 390 × 844 mobile viewports.

1. Confirm every visible word remains inside its intended card, group, or
   label.
2. Trace every connector from source to target. Branches must join their spine,
   and arrowheads must end at a meaningful target rather than in whitespace.
3. Read the visual at the rendered desktop documentation width. Split or
   simplify a dense diagram instead of shrinking it.
4. Confirm the narrow page has no horizontal overflow and that the stacked
   diagram, or the screenshot's full-size link, remains usable.
5. Inspect print or print emulation so meaning survives without colour or a
   dark-theme background.

Record all five checklist identifiers and the review method in the registry,
then compute the SHA-256 of the final source. `npm run validate:assets` rejects
any later byte change until a maintainer repeats the review and updates the
approval record. The hash proves which exact source was inspected; it does not
replace the browser review.

## Screenshot fixtures

Screenshots use seeded synthetic data only. Never capture a real Institution,
User, Exam, answer, credential, email address, hostname, object key, or local
filesystem path. A screenshot's review triggers must include both the UI source
or component contract and the tracked seed fixture that produced its visible
state.

Use these deterministic capture defaults unless the procedure requires a
different viewport and the registry provenance explains why:

| Setting | Default |
| --- | --- |
| Desktop viewport | 1440 × 1024 CSS pixels |
| Mobile viewport | 390 × 844 CSS pixels |
| Browser scale | 100% |
| Theme | Light |
| Motion | Reduced |
| Locale | `en` |
| Time zone | `UTC` |
| Data | Tracked, deterministic synthetic fixture |

Capture only the application surface needed for orientation. Exclude browser
chrome, developer tools, notifications, account menus, and unrelated personal
applications. Prefer one clean frame over a sequence of nearly identical
screenshots. Crop annotations must remain outside meaningful UI content.

When a review trigger changes, regenerate the screenshot with the same fixture
and capture settings, inspect the visual diff, update `last_reviewed`, and
record any intentional capture change in `provenance`. If the UI is not stable
enough for a deterministic fixture, keep the procedure prose-only.
