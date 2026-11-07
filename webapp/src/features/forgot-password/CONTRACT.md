# Forgot-password page contract

`/account/forgot-password` is the public local-account recovery request page.
It accepts one email address and calls `POST /api/v1/auth/password-reset/request`.
The accepted outcome is deliberately identical for known and unknown Users and
the page never claims that an account, password, token, or mail delivery exists.

The form validates required email syntax locally, focuses the first invalid
field, keeps input stable during submission, and reserves action-adjacent
`FormFeedback` space. Rate limiting and service failure use bounded localized
copy. Successful submission presents the generic check-your-email state.

The accepted replacement uses the shared `TaskState` hierarchy with a teal
kind line and the persistent polite announcement. Its lede echoes the address
the person typed, held only in component memory, inside one fixed conditional
sentence (“If an account uses …”) so a typo is obvious while the response stays
identical for known and unknown addresses. The address never enters the URL,
the live announcement, storage, or a request other than the one submitted.
“Send it again” returns to the form with that address still filled in and
focuses the email field. Because submission removes the invoking form, focus
moves to its replacement heading; initial presentation and ordinary form
failures do not move focus.

The recovery-request action accepts only the declared HTTP 202 response and
maps rate limiting, unknown Problem Details, malformed responses, and transport
failure into bounded semantic results. Presentation never compares a server
problem code.

The page uses the shared shell column. Its kind line, heading, lede (which
states the 1-hour link lifetime, the server default), form, and actions follow
document order without implying server-backed progress. The accepted state's
delivery hint uses the shared unboxed `Notice`. The page captures no
credential, stores no account value outside component memory, and offers only
the fixed `/login` return path.
