# Installation setup page contract

`/setup` is the one-time operator surface for establishing this Proctor
installation. It presents the deployment-owned bootstrap proof, the single
Institution identity, and the first administrator profile as one atomic task.
It never treats the first submitted User as an implicit administrator and it
never creates a Session.

## Entry and status

The page first requests `GET /api/v1/bootstrap` and accepts only a response
with a boolean `initialized` value.

- While the request is pending, the page names the bounded check and exposes
  no form. The shared Loading indicator and message occupy the main content
  reservation while the setup kind line, heading, and lede remain visible.
- When `initialized` is false, the setup form becomes available.
- When `initialized` is true, the page explains that Proctor is already set
  up and links to `/login`; it never offers to replace the Institution or
  first administrator.
- A transport failure, malformed success body, or server failure produces a
  recoverable state with an explicit retry action.

The route interprets no URL credential. Bootstrap removes an unexpected
fragment before render, and the page never reads setup input from query
parameters, storage, or document bootstrap data.

## Atomic form

The visible form is one flat column, ordered by what it creates:

1. the Institution: required `display_name` (“Institution name”), required
   canonical `name` (“Short name”), and an optional `description`;
2. the first administrator: optional `display_name` (“Your full name”),
   required `email`, required `username`, and required `password`; and
3. the deployment-owned `bootstrap_secret` (“Setup key”), last, with guidance
   naming the `PROCTOR_AUTHENTICATION_BOOTSTRAP_SECRET` setting and the
   value shown in the data face.

The short-name and username guidance states the characters the server
accepts. Password guidance sits under the password field and states the
minimum length from public discovery, which is available before setup; while
discovery is unavailable only the reuse note is shown.

Every control has a visible label, stable name, applicable input type and
autocomplete value, associated validation guidance, and a minimum 44-pixel
target. Required controls use native required semantics and the shared visible
required mark. Password and bootstrap-secret disclosure is a centered,
icon-only button with localized accessible-name and title text. Paste is never
blocked. Optional empty profile values are omitted; required ordinary text is
trimmed at submission while passwords and the bootstrap secret remain
byte-for-byte as entered.

Empty required fields are rejected before transport. Focus moves to the first
invalid control in visible order. One pending submission disables duplicate submission,
preserves visible input, marks the form busy, and announces progress. The
page sends exactly one same-origin request to `POST /api/v1/bootstrap` with the
generated API client.

## Completion and failure

A successful response clears live secrets and presents a completion state
distinct from the already-set-up state: a success-toned kind line, a heading
naming the new Institution, a lede giving the next step, a `FactList` of what
was created (the Institution display name; the administrator name, or username
when no name was given, and email), and a normal link to `/login`. Those
facts come only from the values the operator just submitted, never from the
response body. The heading takes focus because the submit action is gone. It
does not sign in, mint local authority, infer email verification, or navigate
automatically.

Problem behavior is selected only from the bounded `problem.code`:

The setup action module owns that selection and successful-response validation.
The form receives only semantic outcomes such as bootstrap denial, password
rejection, rate limiting, completion, or safe unavailability.

| Problem code | Page behavior |
| --- | --- |
| `installation.already_initialized` | Clear live secrets and show the already-set-up state, never the created state |
| `installation.bootstrap_denied` | Associate a safe denial with the bootstrap-secret field |
| `authentication.password.invalid` | Associate policy-compliant guidance with the password field |
| `authentication.rate_limited` | Show a form-level request-later failure |
| `installation.unavailable`, `request.invalid`, unknown, malformed, or transport failure | Show a generic recoverable form failure |

No server title, detail, identifier, response administrator profile,
bootstrap secret, password, or response resource is rendered, logged, serialized, persisted, or
placed in a URL. A failed transaction is never described as partially
complete. A general form failure remains in the `FormFeedback` region after
the action row, which takes no space until it holds a message, and is
announced politely without moving focus. The form fields
and action row remain stationary while ordinary failure copy appears.

## Presentation

The page uses the single task column of `AccessPageShell`. An
`AccessTaskIntro` (kind line, page heading, lede) precedes the flat ordered
form, which is not a wizard or a large floating card. The lede says setup runs
once and what it creates; there is no standing caution notice, and the
generic failure says that nothing was changed. The task remains
one-dimensional at every width and at 200% zoom.
