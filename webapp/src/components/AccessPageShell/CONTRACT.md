# Access page shell contract

`AccessPageShell` is the one structural frame for every declared hosted
route, from `/setup` and `/login` through password recovery, Invitation
acceptance, Desktop authorization, email tasks, provider connection, account
security, fresh proof, and terminal confirmation. It owns the skip link, the
masthead, one centred task column holding the single `main` landmark, and the
footer. There are no layout variants: a page that seems to need a second
column, a wider measure, or a vertically centred status card is expressed
inside the task column instead.

It does not own authentication state, API orchestration, form controls,
buttons, notices, headings, navigation decisions, or document metadata.
Feature pages provide all task copy and retain their own state semantics.

## Masthead

The masthead is a `header` (banner) with the flat Proctor lockup at the
inline start and the Institution display name at the inline end. The lockup
consumes the shared effective product theme: a light-scheme theme uses the
purple mark with ink wordmark and a dark-scheme theme uses the purple mark with
white wordmark. It does not query the system preference or import theme IDs,
and has no enclosing background or adjacent rule.

The Institution name is muted, small-body text read through
`useHostedInstitution()` (see the [`auth` contract](../../auth/CONTRACT.md)).
It is omitted while discovery is pending, when discovery fails, and when the
installation publishes no Institution presentation; its absence never blocks
page rendering. It is hidden below `30rem`, where the footer help line still
names the Institution, and wraps rather than truncating when long.

## Task column

`main#main-content` has `tabindex="-1"` so the skip link and feature-owned
validation focus have stable targets. It is the task column: bounded by
`--proctor-measure-form` (`36rem`), centred, and given a consistent block
start so every route's kind line and heading land in the same place. Content
follows document order and remains one-dimensional at narrow widths and 200%
zoom.

## Footer

A `footer` (contentinfo) below the column carries a hairline rule over the
column's width and muted label-size text:

- `signedIn?: { name: string; signOut?: ReactNode }` — supplied only by a page
  that has confirmed a signed-in identity. The shell renders “Signed in as
  {name}” as escaped text and places the page-owned `signOut` control after a
  decorative separator. The page owns the control, its operation, and its
  quiet (text-variant or plain link) presentation.
- The help line, always present: “Need help? Contact {Institution}.”, or
  “Need help? Contact your institution.” until the name is known.
  When discovery publishes a support address
  (`HostedInstitution.supportAddress`), the Institution name in the same
  localized sentence becomes a `mailto:` link without a new prop or layout
  change; with no known name the sentence stays unlinked.

## Props

| Prop | Type | Meaning |
| --- | --- | --- |
| `children` | `ReactNode` | The task content, rendered inside `main` |
| `skipLabel` | `string` | Localized skip-link label |
| `signedIn` | `{ name: string; signOut?: ReactNode }` (optional) | Confirmed signed-in identity for the footer |

The `variant`, `aside`, `asideLabel`, and `mainSize` props of the earlier
split/single/status shell no longer exist.

## Tests

`AccessPageShell.test.tsx` covers the skip link, the single focusable main
landmark, Institution omission and presence, the help line, and the signed-in
footer. The hosted-access browser specs cover lockup selection, masthead
content, and theme behavior.
