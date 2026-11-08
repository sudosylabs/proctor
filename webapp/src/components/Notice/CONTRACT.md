# Notice contract

`Notice` is the domain-neutral inline evidence pattern shared by the hosted
pages. It presents short informational, successful, warning, or failure copy
as unboxed text: no tinted panel, enclosing border, accent rail, radius,
shadow, or decorative icon.

| Prop | Type | Meaning |
| --- | --- | --- |
| `tone` | `"accent" \| "information" \| "success" \| "warning" \| "danger"` (default `information`) | Selects the state treatment |
| `lead` | `ReactNode` (optional) | Short bold lead that names the point (“Didn't start this?”) before the supporting text |
| `children` | `ReactNode` | Supporting text; inline content or paragraphs |

`success`, `warning`, and `danger` add a small square state marker in the
state colour, aligned to the first line, and colour the optional lead. The
marker is hidden from assistive technology. `information` and `accent` are
neutral: no marker and an ink lead. `accent` remains in the type for
compatibility but renders like `information`, because purple is reserved for a
view's one primary action. Supporting text stays in the subtle foreground, so
a tone never supplies or replaces meaning. The owning feature provides
localized content and chooses the tone from its bounded state; arbitrary
server prose never enters the component.

`Notice` renders a neutral `div` and adds no ARIA role or live-region behavior
by default. The feature owns whether static content is a `note`, whether a
changed message is announced through its existing live region, and whether the
notice is part of a larger status component. Native `div` attributes, refs,
and `className` are forwarded for those semantics and local layout only.
The stable `data-proctor-notice` and `data-proctor-notice-tone` attributes are
diagnostic test hooks; product behavior must not branch on them.

The component owns its type, marker, lead treatment, forced-colors marker, and
wrapping behavior in both themes. Consumers may set placement or measure but
must not replace its color, typography, marker, or internal spacing. It does
not implement form-feedback announcements, toasts, overlays, dismissal,
actions, route-wide status layouts, or field-level validation. `FormFeedback`
composes its danger treatment when a submission failure must remain beside an
action.
