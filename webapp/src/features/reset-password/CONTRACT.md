# Reset-password page contract

`/account/reset-password` receives a `password_reset_token` from the sanitized
fragment bootstrap. The raw token remains in one in-memory ref, never appears
in rendered content, a query string, storage, logs, analytics, or error copy,
and is destroyed after a terminal success or unusable-link response.

The page requires an explicit form submission containing a new password and a
matching browser-only confirmation. It sends only the token and new password to
`POST /api/v1/auth/password-reset/complete`. Successful completion clears both
fields and explains that existing Sessions were revoked. Expired, consumed,
superseded, malformed, and concurrent-loser credentials share one bounded
unusable-link state.

The password rules are the new-password field's description (the discovered
minimum length, “At least {n} characters.”, plus the recent-reuse note), and
password-policy rejection remains adjacent to that field; the confirmation
field carries no rules. General failure appears in the action-adjacent
`FormFeedback` region, which takes space only while it holds a message, and
permits retry while the token may still be usable. The task introduction
states only that other devices will be signed out, without a progress
indicator or additional server state.

Completion and server-reported unusable-link replacements use the shared
`TaskState` hierarchy, a teal or coral kind line, and the persistent polite
announcement. Completion offers `Sign in`; the unusable-link state states the
1-hour single-use lifetime (the server default) and offers `Send a new link`
to `/account/forgot-password` plus a quiet return to sign in. A replacement caused
by submission focuses its heading because it removes the invoking form. A link
that is already missing during initial render presents the same bounded state
without taking focus.

The completion action accepts only the declared HTTP 204 response and owns the
mapping from concealed token failure, password rejection, rate limiting, and
unknown or transport failure into semantic results. Presentation never receives
or compares a server problem code.
