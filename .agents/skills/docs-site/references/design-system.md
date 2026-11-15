# Documentation visual-system reference

Proctor documentation should feel like a technical handbook prepared for an
operator, institution administrator, security reviewer, or developer who needs
to trust what they are reading. Its design is precise, calm, and inspectable.
It is neither a marketing skin, an application dashboard, nor a collection of
page-specific visual ideas.

This document owns the durable visual language. The human-edited values live in
[`docs/site/design-system/tokens.mjs`](../../../../docs/site/design-system/tokens.mjs). The
generated [`tokens.css`](../../../../docs/site/src/css/tokens.css) is the Docusaurus adapter,
and the design-system module supplies the approved palettes to the visual-asset
validator. Edit the source module, not either adapter.

## Direction

Proctor documentation should read like a carefully edited technical handbook:
neutral greys, one sans family, hairline structure and generous whitespace.
Every element is there because a reader needs it. Nothing is styled only to
make a page look designed.

Purple is reserved for links, focus, the reader's current location in the
outline, and a few syntax roles. It does not fill the header, panels, badges or
backgrounds. Status colors (teal, amber, coral) mark state only: a response
status dot, a warning icon, a required field.

Structure comes from typography, spacing and one-pixel rules. Do not use
eyebrow labels, uppercase micro-headings, tinted callout boxes, colored left
borders, cards around ordinary prose, or decorative numbering. A note is a
bordered box whose icon carries the tone.

The desktop structure is:

```text
┌──────────── sticky header: lockup, sections, search ────────────┐
│ section rail │ 45rem reading column │ "On this page" │
└──────────────────────────────────────────────────────────────────┘
```

The header stays fixed at the top while content scrolls beneath it. The
outline column hides below `1200px`; the section rail moves into the menu below
`997px`.

## Reading surfaces

A page head is the title, then one quiet line naming the audience and content
maturity, then the lead paragraph. The head ends with a hairline. Section
headings are sentence case with no rule beneath them. Tables use horizontal
rules only. Lists of destinations use the ruled link list: title, one line of
description, a chevron.

The section rail hangs every page list from one hairline; the current page
darkens its own segment of that line. The end of a page has one row with
"Edit this page" and "Report a problem with this page", then a ruled pager
whose previous and next halves are divided by a hairline.

Every code block uses one frame: a title or tab row, a copy action, an optional
line-number gutter and highlighted lines. Syntax colors are tokens, so one
Prism theme serves both color modes.

Endpoint pages split the width between the contract and a sticky request
sample. The contract reads as requirements, parameters, request body and
responses, each as ruled property rows with a monospace name, a muted type and
a "required" marker. Response statuses are tabs with a teal or coral dot. The
request form and authorization summary of the API theme are hidden; these
pages never send requests.

Error-code pages at `/problems/<code>` show the code as the title, the
catalog meaning as the lead, then causes, fixes, an example response and
related codes, with generated details (status, retry, endpoints) beside them.

Diagrams are drawn on the page in the site's own type and colour tokens, so
they follow the light and dark themes and print like the surrounding text.
Screenshots keep a light plate because they show the light product.

## Core palette

Six colors define the identity. Lighter surfaces and dark-mode values are
derived tokens, not new meanings.

| Color | Light value | Meaning |
| --- | --- | --- |
| Proctor purple | `#5C00AA` | Proctor-controlled, authoritative, selected, or primary path |
| Ink | `#161616` | Content and neutral structure |
| Paper | `#FFFFFF` | Reading canvas and screenshot plate |
| Teal | `#0B766E` | Reconstructible/transient effects or a successfully completed outcome |
| Amber | `#95520D` | Human intervention, caution, paused, or incomplete state |
| Coral | `#B4233C` | Denied, invalid, destructive, unavailable, or fail-closed outcome |

Color is semantic, not decorative:

- Purple is the only general focal accent. A card does not receive teal,
  amber, or coral merely to make a row look varied.
- Teal never marks an ordinary section or neutral diagram object. It means the
  result can be reconstructed, is transient, or is explicitly complete.
- Amber indicates that a person must notice or intervene, or that progress is
  deliberately incomplete. It does not mean “secondary.”
- Coral is reserved for real failure, rejection, destructive action, or an
  unavailable authority. It does not decorate security content.
- Gray and ink carry passive context, inactive structure, and neutral
  relationships.

Every meaning must also survive without color. Pair color with a label, state
word, line style, shape, or position. Normal text maintains at least `4.5:1`
contrast against its actual background; the design-system check verifies the
standard foreground/surface pairs in both themes.

### Theme surfaces

The site's neutral values are tuned for long reading. They live in
`tokens.mjs`; this table records their intent.

| Role | Light | Dark |
| --- | --- | --- |
| Canvas | `#FFFFFF` | `#0D0D0F` |
| Surface (inline code, hover) | `#F6F6F7` | `#17171A` |
| Border | `#E8E8EB` | `#25252A` |
| Strong border | `#D6D6DB` | `#35353C` |
| Primary text | `#17171A` | `#F3F3F5` |
| Secondary text | `#404047` | `#C8C8CE` |
| Muted text | `#686871` | `#8F8F98` |
| Accent | `#5C00AA` | `#BB9DFF` |

## Typography

The site, including its diagrams, uses locally bundled Geist Sans and Geist
Mono.

| Role | Family | Allowed weights |
| --- | --- | --- |
| Headings, interface, prose | Geist Sans | `400`, `500`, `600` |
| Code, protocol values, error codes, paths | Geist Mono | `400`, `500` |

Do not request weights outside this set. Do not set a literal font family in a
page stylesheet. Use the generated `--ifm-font-family-base` and
`--ifm-font-family-monospace` variables.

| Role | Size | Line height | Weight |
| --- | --- | --- | --- |
| Utility and captions | `0.78–0.84rem` | `1.5` | `400` or `500` |
| Small body | `0.875–0.9rem` | `1.6` | `400` |
| Body | `1rem` | `1.72` | `400` |
| Lead | `1.125rem` | `1.6` | `400` |
| Subsection heading | `1.0625rem` | `1.4` | `600` |
| Section heading | `1.375rem` | `1.3` | `600` |
| Page heading | `2.125rem` | `1.15` | `600` |

Headings tighten their tracking as they grow. Body copy stays within a `45rem`
column; a wide viewport is not permission to lengthen a line.

Diagrams use `15px` card titles, `13px` supporting text and `12px` arrow
labels at the real column width, never text scaled down from a larger canvas.
If the content does not fit, delete detail or split the diagram.

## Spacing and geometry

The base grid is 8 pixels. The `4px` step exists only for optical corrections
inside a control or icon. Structural layout uses the tracked scale:

| Token | Pixels | Typical use |
| --- | ---: | --- |
| `space-1` | 4 | Optical adjustment |
| `space-2` | 8 | Tight internal gap |
| `space-3` | 12 | Label-to-value gap |
| `space-4` | 16 | Control padding |
| `space-5` | 24 | Card padding or related-object gap |
| `space-6` | 32 | Component separation |
| `space-7` | 48 | Section separation |
| `space-8` | 64 | Major composition gap |
| `space-9` | 96 | Page-level interval |

Use three radii consistently:

- controls and small tags: `6px`;
- cards: `12px`;
- screenshot plates: `16px`;
- pills: fully rounded only for a real status, token, or compact action.

A card is not a pill, and adjacent objects do not receive different radii for
visual variety. Shadows are limited to genuinely elevated surfaces such as a
search dialog. Ordinary cards and diagrams use borders and spacing.

## Icons

The Proctor icon vocabulary represents recurring domain or infrastructure
nouns. Icons inherit the mark's square construction, visible openings, and
45-degree transitions where the subject permits them.

- Optical sizes are `24px`, `32px`, and `48px`.
- The standard icon stroke is `2px`, with round caps and round joins unless a
  fenced edge requires a square end.
- Keep at least `2px` clear space at the 24px size.
- Use the owned `DocIcon` component; do not mix emoji, a general-purpose icon
  library, and one-off SVG symbols.
- Do not add an icon for a concept used only once; use a label instead.
- Product or vendor marks remain their own artwork and never inherit the
  Proctor icon geometry.

The bounded vocabulary is deliberate:

| Name | Meaning |
| --- | --- |
| `deployment` | Runtime nodes and operational deployment work |
| `account` | Personal access, authentication methods, and account security |
| `exam` | Exam Manager authoring, delivery, and review work |
| `candidate` | Candidate preparation, participation, and result access |
| `institution` | The one educational Institution governed by an Installation |
| `assurance` | Reviewed security or authorization guarantees |
| `integration` | Developer work across a typed or protocol boundary |
| `api` | The public HTTP contract and endpoint reference |
| `glossary` | Canonical Proctor vocabulary |
| `architecture` | System ownership and dependency topology |
| `search`, `close`, `arrowRight`, `external`, `check` | Repeated interface actions and state confirmation |

Icons next to an existing text label are decorative and hidden from assistive
technology. A standalone icon supplies a short accessible title. Adding a name
requires a recurring use, an entry in this table, a geometry review at all
three sizes, and an update to the icon contract test.

## Diagram grammar

A diagram is a YAML layout in `docs/public/diagrams/<id>.yaml`, registered in
the asset registry with `visual_system: proctor-diagrams-v2`. The grammar and
its validator live in
[`diagrams/index.mjs`](../../../../docs/site/diagrams/index.mjs); the
`Diagram` component draws it. Authors choose structure and words, never
coordinates or colours.

- **Rows** place up to four cards, groups or branches side by side, joined by
  arrows that may carry a short label and a step number. Below 600px of
  available width every row stacks top to bottom and the arrows turn down.
- **Connectors** join items of one row to items of the row below, to fan out
  or to continue a sequence.
- **Cards** carry a title of one to three words and one short sentence, or a
  few short lines.
- **Groups** frame related cards under a title and a short subtitle.
- **Branches** fan one spine out to final outcomes.
- **Notes** close a diagram with its main point when the drawing needs one.

### Visual rules

- One accent per diagram, on the element or path that matters most. Everything
  else is ink, grey text and hairline borders on the page background.
- Dashed borders and lines mean separate, optional, or safe to lose.
- No fills, tints, shadows, grids, frames, eyebrow labels, uppercase tags, or
  numbered badges. Order comes from layout and arrow labels.
- The diagram has no title; the page heading names the subject and the
  caption states the point once.
- Labels are plain language. Keep canonical Proctor terms, but drop slogans and
  internal jargon; the page prose carries exact technical detail.
- One diagram makes one claim. Split it rather than squeeze it.

## Vocabulary in visual design

Use canonical Proctor terms from the repository
[`glossary` skill](../../glossary/SKILL.md). A
diagram label is not a place to create a shorter synonym such as “session”
for Exam Sitting or “group” for Class. If a canonical term makes the drawing
too dense, simplify the drawing rather than rename the concept.

The glossary and tooltip slice will make selected first occurrences
explainable. Tooltips do not appear inside diagrams, headings, code, or
API paths.

## Interaction and motion

Movement must explain a state change or preserve orientation. Hover movement is
limited to a small translation on a real interactive target. Do not animate
diagram paths, add ambient particles, or scatter unrelated transitions.
All motion honors `prefers-reduced-motion` and leaves the complete meaning
available without animation.

Focus indicators use a three-pixel primary outline with three-pixel offset.
Hover is never the only way to reveal information. Tooltips, dialogs, menus,
and search results must work with keyboard, pointer, and touch input.

## Human authoring workflow

To change a durable token:

1. Edit [`tokens.mjs`](../../../../docs/site/design-system/tokens.mjs).
2. Update this document when meaning, allowed use, or geometry changes.
3. From `docs/site`, run `npm run generate:design-system`.
4. Run `npm run check:design-system` and `npm run test:design-system`.
5. Review the affected pages in both themes at desktop and mobile widths.

The generator is deliberately one-way. It hides CSS serialization, font
imports, Infima compatibility variables, contrast checks, and palette lookup
behind a small interface. A maintainer does not synchronize separate color
lists by hand.

Authored stylesheets may use semantic custom properties and `color-mix()`, but
they may not introduce literal colors, the retired `cobalt`/`aqua` token names,
or unbundled font weights. The design-system audit rejects those forms.

## Retired diagram systems

Diagrams were once fixed 1200×720 SVG plates (`proctor-assurance-v1`, and
before that `legacy-cobalt-v0`). Both were replaced by `proctor-diagrams-v2`
and are no longer accepted by the registry validator.

## Acceptance checklist

Before a visible slice is complete, inspect it in a browser:

- desktop `1440×1024` and mobile `390×844`;
- light and dark themes;
- keyboard order, focus visibility, and reduced motion;
- text contrast, line length, zoom, and narrow-width wrapping;
- no unintended horizontal overflow;
- diagrams at desktop width and stacked at phone width; screenshots at page width and full size;
- connector continuity, text containment, and print contrast;
- locally served Geist with no third-party font request; and
- no semantic color used merely for visual variety.

Screenshots are evidence for the review, not a substitute for exercising the
page.
