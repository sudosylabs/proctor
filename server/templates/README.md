# Transactional-mail template workflow

This data-only directory is the exact maintainer contract for Proctor
transactional-mail presentation. It contains no Go implementation: only build
tooling, one authored MJML source, one authored plain-text source, and one
tracked generated HTML file for every closed mail key. Human copy is not
authored here; the flat server-owned catalogs under
[`../i18n`](../i18n) supply localized fields to
both alternatives.

The HTML presentation is set like a letter: a white page, one text column, a
strict type scale, and hairline rules instead of boxes or badges. Ink is
`#16141B`, secondary text `#46424F`, muted labels `#77727F`, rules `#E9E6EE`,
and violet `#5C00AA` appears only on the action button and links. Security
notices mark their kind line and aside lead in rust `#9A3412`. The header pairs
the package-local lockup `proctor-lockup-25d-v1.png` (600×118 transparent PNG,
112×22 display, `alt="Proctor"`) with the institution name. Every template
includes the same two partials, [`partials/head.mjml`](partials/head.mjml) and
[`partials/message.mjml`](partials/message.mjml), so all messages share one
reading order: kind, heading, greeting and body, an optional emphasised detail,
labelled facts, an optional aside, an optional action, and the colophon. Phones
stack each fact's label above its value. Human prose still comes only from
`../i18n`. Maintainers may restyle the partials without changing delivery
logic, but must retain the semantic reading order, complete text equivalent,
contextual escaping, accessibility, and privacy constraints in the repository
[`transactional-mail` skill](../../.agents/skills/transactional-mail/SKILL.md).

## Inline branding

The header uses the 2.5D purple mark and ink wordmark on the white paper.
`proctor-lockup.svg` is the package-local editable artwork. The released PNG
is embedded in the server and referenced by a Content-ID containing its
SHA-256 digest. The mail-owned image catalog validates its digest and dimensions
at startup; each production template must reference one released logo.
The root mail adapter supplies the PNG as an inline MIME part, so delivery
requires no remote image request.

Released PNG filenames, digests, and bytes are immutable. A later artwork
release adds a new version and Content-ID while retaining earlier versions:
queued deliveries and frozen fan-out bundles must resolve their original
artwork across upgrades and retries. Any other image source, including a relative
path, fails closed.

The generator includes released PNGs in template source digests. After
changing the shared header or adding an image version, regenerate the HTML.
The preview command copies referenced PNGs alongside its output and resolves
Content-IDs to those local copies for browser viewing.

## Outlook conditional comments

The generated HTML keeps MJML's Outlook conditional comments
(`<!--[if mso]>…<![endif]-->` and the revealed `<!--[if !mso]><!-->` markers).
`html/template` strips ordinary comments, so the renderer lifts each complete
conditional comment out as trusted static markup before parsing. A template
action inside a conditional comment is rejected at startup.

## Typed properties

Every MJML and text source starts with a non-rendering license header followed
by a non-rendering comment listing the private `server/app/mail` renderer
properties. The generator emits the same license header in every tracked HTML
result before its generated-file marker. All templates receive the same model:

- `.Copy.Subject`, `.Copy.Preheader`, `.Copy.Kind`, `.Copy.Heading`,
  `.Copy.Greeting`, `.Copy.Paragraphs`, `.Copy.Why`, `.Copy.Support`, and
  `.Copy.SentBy` — always present.
- `.Copy.Institution` — the institution display name, empty when unknown.
- `.Copy.AsideTitle` and `.Copy.AsideText` — both or neither.
- `.Copy.ActionLabel`, `.Copy.ActionNote`, `.Copy.ActionFallback`,
  `.Copy.ActionFallbackLink`, and `.ActionURL` — the action, its expiry note,
  and a short fallback link under the button; `.ActionURL` is empty when no
  action is offered. The text alternative prints the full URL.
- `.Hero` — an optional emphasised detail with `.Label`, `.Primary`,
  `.Secondary`, `.Code`, and `.Struck`.
- `.Facts` — ordered labelled details, each with `.Label`, `.Value`, and
  `.Struck`.
- `.AsideAlert` — true for security notices; their aside precedes the action.

The renderer, not the template, decides which facts a message shows. Times
are written for people in the recipient's own time zone, or the installation
`Mail.DisplayTimezone` when the recipient has none (UTC is the unset default).
Exam Sitting messages are frozen once per locale for the whole audience, so
they carry no recipient name and always use the display time zone. The
greeting uses the first word of the frozen display name.

Each presentation family keeps its privacy bounds:

- Personal Access Token notices show the description, an institution or
  Academic Unit scope label, expiry, and action time. They never receive the
  one-time credential, stored token hash, the complete action list, or the
  action count.
- Exam Sitting messages show the published Exam title, Class display name,
  date, time window, duration, and, for a reschedule, the previous Class. They
  never receive Exam instructions, resources, roster contents, private
  cancellation rationale, or the actor's identity.
- Exam Manager notices show the Exam title, resulting relationship, and action
  time. They never receive the actor, authorization grants, other Managers, or
  private audit detail.
- Class membership notices show the affected Class display names and effective
  dates. They never receive the actor, roster contents, authorization grants,
  or private audit detail.
- Submission receipts show the Exam title, the Submission receipt code, and
  the sealing time. They never receive answers, workspace paths or selectors,
  manifest contents, integrity signals, Session or continuity credentials,
  candidate profile fields, or private review state.
- The released-result message shows only the Exam title and release time. It
  never receives a score, academic outcome, candidate remarks, manager notes,
  decisions, evidence, rationale, Submission or Workspace data, or an action
  link.
- The email-change warning shows the previous address and a masked form of the
  new address.
- The retention notice shows only the planned deletion date.
- Academic Unit membership and Role Binding notices name neither the unit nor
  the role, so a message cannot identify privileged accounts.

The renderer accepts no arbitrary map and registers no custom template
functions. Copy is markup-free and HTML is parsed with Go `html/template`, so
localized and dynamic values are contextually escaped. Action URLs are
server-constructed absolute HTTPS URLs; templates never construct routes.

## Commands

Install the exact lockfile toolchain once after checkout:

```sh
make -C server/templates install
```

Regenerate tracked HTML after changing MJML or a partial, then verify that the
result is fresh and deterministic:

```sh
make -C server/templates generate
make -C server/templates check
make -C server/templates test
```

Render the complete English catalog, without mail delivery or production
data, into a caller-selected directory:

```sh
make -C server mail-preview OUTPUT=/tmp/proctor-mail-preview
```

Open `index.html` in that directory to inspect every HTML alternative; each
entry also links to its plain-text equivalent. Preview output is untracked and
must not be written below this source directory.

## Adding or translating a message

1. Add the key to the closed `model.MailTemplateKey` catalog and add the required
   lexically sorted flat `mail.<key>.<field>` entries to `../i18n/en.json`.
2. Add matching `<key>.mjml` and `<key>.txt` sources that include the shared
   partials and the exact property comment, list the template's optional copy
   fields and any copy placeholders in `server/app/mail/localization.go`, and
   build its facts in `server/app/mail/presentation.go`.
3. Regenerate `<key>.html` and run the template and Go tests.
4. Add or extend another top-level locale file using only English IDs and
   matching interpolation placeholders. A locale may be partial; every missing
   message falls back to the installation locale, then English.

The MJML compiler is a build-time dependency only. Runtime binaries embed the
generated HTML, authored text, and released PNGs. Renderer construction parses every
production template; the later delivery composition must construct the
renderer before server readiness. Template changes require regeneration,
rebuild, and restart.
