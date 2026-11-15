# Access task intro contract

`AccessTaskIntro` introduces one focused hosted task with an optional status
line, the page's single `h1`, and a lede. It is proven by password recovery,
Invitation acceptance, provider connection, Desktop authorization, account
security, fresh proof, and setup; the owning feature still decides whether a
loading, success, or terminal state needs the intro.

| Prop | Type | Meaning |
| --- | --- | --- |
| `status` | `{ label: ReactNode; tone: "success" \| "warning" \| "danger" }` (optional) | A real outcome, shown above the heading as an icon and a word in the state colour |
| `heading` | `ReactNode` | The page `h1`, set in the condensed display face |
| `headingID` | `string` | Stable heading id for `aria-labelledby` and focus |
| `body` | `ReactNode` | The lede below the heading |
| `beforeTaskNote` | `ReactNode` (optional) | Information needed before acting, shown under the lede only below `48rem` |
| `focusHeading` | `boolean` (optional) | Opt-in heading focus after a user-triggered replacement |

There is no neutral kind line. A status line appears only when the page
reports a real outcome and uses the shared `TaskStatusLine` from
[`TaskState`](../TaskState/CONTRACT.md), so colour, shape, and words travel
together. The lede uses the large-body size in the subtle foreground and wraps
within the task column.

`beforeTaskNote` pairs with a verification-rail item marked `beforeTask` (see
the [`AccessPageShell` contract](../AccessPageShell/CONTRACT.md)). Below
`48rem` the rail follows the task, so the note repeats that item under the
lede and the rail hides it; from `48rem` the note is hidden and the rail shows
the item. Each is announced once at any width.

The component owns only typography, vertical rhythm, and the shared opt-in
task-heading focus mechanism. `focusHeading` is used only after a
user-triggered replacement removes its invoker; initial or background loading
never enables it. It does not own a route, workflow steps, API state,
evidence, navigation, or actions. Content may expand and wrap without
changing order.
