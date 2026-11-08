# Form feedback contract

`FormFeedback` is the domain-neutral submission-result region shared by the
hosted forms. It presents a persistent form-level failure beside the action
that produced it. Field-specific validation, and server failures the page can
attribute to one field (wrong credentials under the password, a rejected setup
key under that key), remain owned by `InputField` or the feature's native
control and are never moved into this component.

The component is always mounted with `role="status"`, `aria-live="polite"`,
and `aria-atomic="true"`. A changed message is therefore announced without
moving keyboard focus. The owning feature focuses the first invalid field when
a failure identifies one; a general submission failure leaves focus alone.
There is one announcement path: a feature does not repeat the same message in
another live region or focus the feedback merely to make it speak.

The empty region renders no children and has zero block size, so it reserves
no blank space between an action and whatever follows it. The owner places
the region directly after its action row in normal block flow, without a flex
or grid gap between the two; the component owns the space above a visible
message. The fields and submit action precede the region and remain
stationary when a message appears; only content after the region moves down
to make room. Copy grows in normal document flow and is never clipped,
truncated, overlaid, or constrained to a fixed height. The component adds no
transition, so a message appears without animated movement.

Visible failures compose the shared `Notice` danger treatment. The component
adds no panel, icon, shadow, radius, dismissal, timer, portal, or toast
behavior. A failure remains visible until the owning feature makes it obsolete
through an edit, retry, state transition, or successful completion. The stable
`data-proctor-form-feedback` attribute is a diagnostic test hook and does not
drive product behavior.

An owning feature may assign the region an `id` when its persistent failure
also describes a related control through `aria-describedby`; this does not
change the region's announcement or layout responsibility.
