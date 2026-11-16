# Access page shell contract

`AccessPageShell` is the one structural frame for every declared hosted
route, from `/setup` and `/login` through password recovery, Invitation
acceptance, Desktop authorization, email tasks, provider connection, account
security, fresh proof, and terminal confirmation. It owns the skip link, the
masthead, the task column holding the single `main` landmark, and the
verification rail. There are no layout variants.

It does not own authentication state, API orchestration, form controls,
buttons, notices, headings, navigation decisions, or document metadata.
Feature pages provide all task and rail copy and retain their own state
semantics.

## Grid

A twelve-column grid bounded by `--proctor-measure-content` with `48px` side
gutters from `48rem` and `20px` below it. The masthead spans every column; the
task column spans columns one to six; the rail spans columns eight to twelve.
All three share the same inline-start edge. Below `48rem` the grid is one
column and the rail follows the task in document order.

## Masthead

A `header` (banner) above a hairline rule.

- **Identity** at the inline start: the 2.5D lockup for the effective product
  theme (purple mark with ink wordmark in a light scheme, ivory-front mark
  with white wordmark in a dark scheme), then a hairline divider and the
  Institution display name, muted and small. The name is read through
  `useHostedInstitution()` (see the [`auth` contract](../../auth/CONTRACT.md)),
  is omitted while discovery is pending, fails, or publishes no Institution,
  and wraps rather than truncating.
- **Signed-in identity** at the inline end, only when the page supplies
  `signedIn`: “Signed in as {name}” as escaped text, then the page-owned
  `signOut` control. Below `48rem` the sentence is visually hidden but stays
  in the accessibility tree, the name alone is shown, and the Institution name
  gives way so the row fits.

## Task column

`main#main-content` has `tabindex="-1"` so the skip link and feature-owned
validation focus have stable targets. Every route's heading lands on the same
block start. Content follows document order and remains one-dimensional at
narrow widths and 200% zoom.

## Verification rail

An `aside` labelled “About this page”. From `48rem` it carries a hairline rule
on its inline start that is only as tall as its content, and its first heading
shares the task heading's cap line. Below `48rem` it sits under a hairline rule
after the task.

The rail renders the page's `rail` items first, then items placed by nested
task content through `TaskRail`, and then the shell's help item,
present on every route: “Need help? Contact {Institution}.”, or “Need help?
Contact your institution.” until the name is known. When discovery publishes a
support address, the Institution name in that sentence becomes a `mailto:`
link without changing the sentence.

`RailItem` is the one rail entry: an optional small `h2` and its content, in
small-body text with muted paragraphs. `beforeTask` marks information a
person needs before acting (a required secret, a time limit). Such an item is
hidden below `48rem`, where the rail follows the task; the page restates it as
a note under its lede so nothing needed first ever appears after the action.
Every entry carries `data-proctor-rail-item` as a diagnostic hook.

A page that knows its rail items passes them through `rail`. Task content that
decides its own items from state it owns (a Desktop request's reassurance,
the setup-key reminder, a provider note) wraps them in `TaskRail`, which
portals them into a slot inside the rail. The slot adds no box, so portalled
items share the rail's rhythm; nothing renders until the shell has mounted
the slot, and the items leave the rail when the task content unmounts.

## Props

| Prop | Type | Meaning |
| --- | --- | --- |
| `children` | `ReactNode` | The task content, rendered inside `main` |
| `skipLabel` | `string` | Localized skip-link label |
| `rail` | `ReactNode` (optional) | Page-owned `RailItem` entries |
| `signedIn` | `{ name: string; signOut?: ReactNode }` (optional) | Confirmed signed-in identity for the masthead |

| `RailItem` prop | Type | Meaning |
| --- | --- | --- |
| `heading` | `ReactNode` (optional) | Small heading for the entry |
| `children` | `ReactNode` | The entry's content |
| `beforeTask` | `boolean` (optional) | Hidden below `48rem`; the page repeats it under its lede |

## Tests

`AccessPageShell.test.tsx` covers the skip link, the single focusable main
landmark, the masthead and rail landmarks, rail order, the before-task marker,
Institution omission and presence, the help line, and the signed-in identity.
The hosted-access browser specs cover lockup selection, masthead content, and
theme behavior.
