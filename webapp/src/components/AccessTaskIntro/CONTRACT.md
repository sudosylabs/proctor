# Access task intro contract

`AccessTaskIntro` introduces one focused hosted task with a quiet kind line,
the page's single `h1`, and a lede. It is proven by password recovery,
Invitation acceptance, provider connection, Desktop authorization, account
security, fresh proof, and setup; the owning feature still decides whether a
loading, success, or terminal state needs the intro.

| Prop | Type | Meaning |
| --- | --- | --- |
| `kind` | `ReactNode` | Sentence-case line naming the kind of task (“Reset password”) |
| `kindTone` | `"success" \| "warning" \| "danger"` (optional) | Colours the kind line teal, amber, or coral when it reports a true terminal outcome |
| `heading` | `ReactNode` | The page `h1`, set in the condensed display face |
| `headingID` | `string` | Stable heading id for `aria-labelledby` and focus |
| `body` | `ReactNode` | The lede below the heading |
| `focusHeading` | `boolean` (optional) | Opt-in heading focus after a user-triggered replacement |

The kind line is muted small-body text in the medium weight. It is never
uppercase, never purple, and never a pill or badge; without `kindTone` it is
neutral. A tone is chosen by the feature from its bounded state, and the
heading and lede still state the outcome in words, so colour is never the only
signal. The lede uses the large-body size in the subtle foreground and wraps
within the task column.

The component owns only typography, vertical rhythm, and the shared opt-in
task-heading focus mechanism. `focusHeading` is used only after a
user-triggered replacement removes its invoker; initial or background loading
never enables it. It does not own a route, workflow steps, API state,
evidence, navigation, or actions. Content may expand and wrap without
changing order.
