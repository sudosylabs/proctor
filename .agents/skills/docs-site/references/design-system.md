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

Illustrations remain light evidence plates in both site themes so their print,
contrast, and review result does not change with the surrounding page.

## Core palette

Six colors define the identity. Lighter surfaces and dark-mode values are
derived tokens, not new meanings.

| Color | Light value | Meaning |
| --- | --- | --- |
| Proctor purple | `#5C00AA` | Proctor-controlled, authoritative, selected, or primary path |
| Ink | `#161616` | Content and neutral structure |
| Paper | `#FFFFFF` | Reading canvas and print-stable evidence plate |
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

The site uses locally bundled Geist Sans and Geist Mono. Illustrations keep
their IBM Plex lettering, which matches the outlined Proctor wordmark, so a
figure renders the same wherever it is viewed.

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

Illustrations use only three intrinsic text sizes: `18px` object names, `14px`
short supporting labels, and `12px` IBM Plex Mono utility annotations. Do not
place smaller text in a 1200×720 figure. If the content does not fit, delete
detail or split the illustration.

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
- evidence plates and large diagram objects: `16px`;
- pills: fully rounded only for a real status, token, or compact action.

A card is not a pill, and adjacent objects do not receive different radii for
visual variety. Shadows are limited to genuinely elevated surfaces such as a
search dialog. Ordinary cards and illustrations use borders and spacing.

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

## Illustration grammar

Every current-system diagram declares `visual_system` as
`proctor-assurance-v1` in the governed asset registry.

### Canvas and density

- Canvas: `1200×720` with a matching view box.
- Safe inset: `56px` on every side.
- Drafting grid: `8px`, using the plate-grid token at one-pixel width.
- Primary objects: normally four to seven.
- Focal elements: one, or at most two when a comparison is the point.
- One illustration communicates one claim. Split state, ownership, and
  transport behavior when combining them would require explanatory prose.

The page heading names the subject. The figure contains only object names,
short state labels, and necessary relationship labels. The visible caption
states the invariant. Do not repeat a poster title, subtitle, paragraph,
footnote, or prose-heavy legend inside the SVG.

### Objects and strokes

- Large objects use a `16px` radius and `2px` border.
- Small controls use a `6px` radius.
- Passive context uses ink or gray borders on paper.
- The authoritative or selected object uses purple fill or border, not both on
  every object in the path.
- Semantic surfaces use their corresponding soft token and retain a textual or
  geometric cue.
- All shapes align to the 8-pixel grid; use a 4-pixel optical offset only when
  stroke centering otherwise appears uneven.

### Connectors and arrows

- Primary connectors are `3px`; secondary relationships are `2px`; the
  drafting grid and annotation brackets are `1px`.
- Arrowheads are `12×12px`. Use one marker geometry across the complete asset
  set.
- Solid purple: authoritative ownership, commit, or primary transition.
- Dashed purple: explicit reference that is not ownership.
- Dashed teal: reconstructible or best-effort effect after a durable outcome.
- Amber: a human intervention or deliberately incomplete transition.
- Coral: rejection or fail-closed termination.
- Gray: passive context or a neutral alternative.

Every connector begins at a visible source boundary and ends with its arrow tip
on a meaningful target boundary. Orthogonal branches share an actual path
segment or junction dot. A branch may not stop near a spine, and an arrow may
not terminate in whitespace. Labels sit at least `8px` away from a connector
and never cover a bend or junction.

### Annotation rail

An evidence plate may end with one short bracket or rail stating the invariant
made visible by the drawing. The rail uses a one-pixel neutral stroke and no
more than two lines of 14px text. It must not become a second caption or a place
to rescue an unclear illustration.

## Vocabulary in visual design

Use canonical Proctor terms from the repository
[`glossary` skill](../../glossary/SKILL.md). An
illustration label is not a place to create a shorter synonym such as “session”
for Exam Sitting or “group” for Class. If a canonical term makes the drawing
too dense, simplify the drawing rather than rename the concept.

The glossary and tooltip slice will make selected first occurrences
explainable. Tooltips do not appear inside illustrations, headings, code, or
API paths.

## Interaction and motion

Movement must explain a state change or preserve orientation. Hover movement is
limited to a small translation on a real interactive target. Do not animate
illustration paths, add ambient particles, or scatter unrelated transitions.
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

## Retired illustration systems

The four original diagrams were simplified and redrawn under
`proctor-assurance-v1`. The former `legacy-cobalt-v0` palette is not an
alternative theme and is no longer accepted by the registry validator.

## Acceptance checklist

Before a visible slice is complete, inspect it in a browser:

- desktop `1440×1024` and mobile `390×844`;
- light and dark themes;
- keyboard order, focus visibility, and reduced motion;
- text contrast, line length, zoom, and narrow-width wrapping;
- no unintended horizontal overflow;
- figures at page width and full size;
- connector continuity, text containment, and print contrast;
- locally served Geist (site) and IBM Plex (illustrations) with no third-party font request; and
- no semantic color used merely for visual variety.

Screenshots are evidence for the review, not a substitute for exercising the
page.
