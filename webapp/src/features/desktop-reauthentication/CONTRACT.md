# Desktop reauthentication page contract

`/reauthenticate/desktop` lets a person confirm, in the system browser, that
they still hold the account already signed in to Proctor Desktop, so the
existing Desktop Session counts as recently authenticated again. It never signs
in this browser and never creates or replaces a Session.

On first entry, bootstrap removes the fragment proof and the `request` query
parameter from browser history before interpreting them; only the bounded
opaque `state` remains in the URL. The page exchanges the in-memory handle,
fragment proof, and state exactly once for the server's route-scoped HttpOnly
binding cookie and never renders, logs, stores, or copies those values.
Everything after binding relies on that cookie.

The page offers exactly one proof: the method the Desktop Session itself used.
A password Session sees a single password field for its own account (there is
no login identifier and no second-factor step); a provider Session sees only
“Continue with <provider>”, which navigates this tab and returns to
`/reauthenticate/desktop?state=...`. A provider attempt that did not count
returns with the closed `#external_login=failed` fragment, which bootstrap
removes and the page shows as a warning beside the same choice. A signed-in
browser Web Session is never offered or used as proof.

The heading is “Confirm it’s you”; the lede names the Desktop Session's
account and says the proof is “to continue in Proctor Desktop”. A muted
closing line says the proof applies to Proctor Desktop only. The shell shows
no signed-in footer because this browser is not signed in.

As soon as the request is proved (after the password, or on return from the
provider) the journey approves it without another click and navigates only to
the server-returned loopback redirect, whose shape is validated exactly as
Desktop Authorization validates its own. The confirmed state stays visible if
Proctor Desktop does not open. Cancel posts the exact state and terminally
replaces the task.

Missing, expired, consumed, canceled, malformed, and other-account requests
share one unusable-link state. A failed check shows recoverable unavailability
with a retry that never repeats binding. A wrong password or rate limit keeps
the form, clears the field, and explains the failure in persistent
`FormFeedback`. Terminal transitions caused by an action focus their heading.

The feature-owned journey is the sole owner of transitions and pending work;
password proof, provider navigation, approval, and cancellation cannot overlap,
and completions after unmount change nothing.
