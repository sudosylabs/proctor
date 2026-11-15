# Form group contract

`FormGroup` divides a long hosted task into numbered parts so proximity and a
shared heading show which controls belong together. It is proven by setup
(Institution, administrator account, setup key) and two-step setup (scan the
code, enter the code).

| Prop | Type | Meaning |
| --- | --- | --- |
| `number` | `number` | One-based position, shown as a two-digit numeral (`01`) |
| `heading` | `ReactNode` | The group's small heading |
| `headingID` | `string` | Stable id that labels the group |
| `as` | `"fieldset" \| "section"` (optional, default `fieldset`) | `fieldset` for controls answering one question together; `section` for a step with its own content |
| `children` | `ReactNode` | The group's fields or step content |

A `fieldset` group uses a floated `legend`, so the legend never interrupts a
rule and takes ordinary block spacing; a `section` group uses an `h2`. Both are
named by the heading through `aria-labelledby`. The numeral is muted, set in
the mono family, and hidden from assistive technology because document order
already conveys sequence.

Groups after the first sit under a hairline rule. The component owns the
heading row, the rule, and the gap between its children; the owning form owns
the gap between groups and the action. It adds no panel, tint, card, or
progress indicator, and it does not own validation, focus, or submission.
