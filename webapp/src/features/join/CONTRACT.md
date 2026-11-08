# Invitation acceptance page contract

`/join` captures an `invitation_claim` from the URL fragment before render and
exchanges it once through `POST /api/v1/auth/browser/invitations`. The claim is
never rendered, copied into a query string, provider state, storage, logs, or
feedback. The shared initial-resource lifecycle composes its two independent
requests and reuses one in-flight result under React Strict Mode.

The returned purpose and requirement decide the closed terminal operation. An
`account` transaction collects username, password, and optional first and last
names, then calls the account-creating acceptance endpoint; its password
description states the discovered minimum length and the reuse note. A `session`
transaction collects no profile or credential fields and calls only the
current-Session acceptance endpoint. Neither path invents Invitation target,
recipient, Role, or membership details that the browser projection omits.

The start projection names the address the Invitation was sent to and, for a
student Class Invitation only, the Class display name. The account form shows
that address first as a native read-only email field described “From your
invitation. You can’t change it here.”; it has no `name` and is never part of
the acceptance body, which takes no address. Session acceptance lists it as a
“Sent to” fact instead.

Acceptance is one atomic Invitation-package transition. Success clears local
credentials and presents a sign-in return path. Invalid, expired, consumed,
superseded, or otherwise unusable claims share bounded recipient-safe copy.
Retryable infrastructure failure preserves the purpose-bound browser handle.
The optional Institution name comes only from public discovery and never gates
acceptance.
It is read only from the shared, fully validated, same-origin discovery result;
malformed or origin-mismatched discovery is treated as absent presentation.

The ready account and Session variants share one intro: the Invitation kind
line, a heading that names the Institution when discovery supplies it, and a
lede chosen from the closed purpose and requirement. A `FactList` then states
what the Invitation is for (the purpose as a plain role word) and when it
expires, formatted with `Intl` in the viewer's locale and time zone inside a
`time` element; raw timestamps are never shown. A student Class Invitation's
Class is one more fact between the two. Academic Unit and Role names are
deliberately absent from the projection, as from their mail, so none is shown
or invented. The account form or the single Session action follows. The page presents no workflow steps or
transaction internals.

Loading, unavailable, invalid, and accepted route states use the shared
`TaskState` hierarchy and persistent polite announcement, with the kind line
toned only for a real outcome (danger for an unusable link, warning for an
unavailable check, success after acceptance). The unusable-link state names
the possible causes together without revealing which applies, and offers sign
in plus a support contact when the Institution publishes a support address.
Acceptance states what the person can now do, chosen from the same purpose
and requirement. Acceptance moves
focus to the replacement heading because the action controls disappear;
initial loading and background context completion do not take focus.

Invitation action modules validate the declared acceptance response and
classify account and Session failures into separate bounded semantic outcomes.
Presentation never receives a Problem Details code, response identifier, or
arbitrary server value.

When Session acceptance reports that sign-in is required, the page preserves
the purpose-bound handle by opening sign-in in a new tab. The persistent
failure text visibly warns about that behavior, programmatically describes the
link, and the new browsing context is isolated from the Invitation page.
