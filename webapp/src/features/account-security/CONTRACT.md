# Account security

`/account/security` owns the hosted MFA lifecycle for the current Session:
status, explicit authenticator setup or restart, activation, Session challenge,
recovery-code regeneration, and confirmed disable. Replacing an active
authenticator uses the existing explicit disable and setup operations; the
confirmation explains the interval without MFA. It never silently disables MFA.
User-facing copy calls MFA “two-step verification”.

The bounded MFA status projection is authoritative for service availability,
recovery restriction, and authentication context. A restricted Session may
reenroll or sign out; it is not sent through ordinary account APIs. Disabling
the service does not remove a recovery restriction. Fresh primary proof and
strong assurance are checked by the server for each operation.

## Status layout

Status is a hairline `FactList` followed by one section per decision, each
with a small heading, one sentence, and its own action:

- facts: “Password: Changed {date}” first when the account has a password
  (`password_changed_at`, a calendar date in the viewer's locale and time zone
  inside a `time` element; omitted for accounts without one), two-step
  verification (On, Off, or Setup not finished, with a small state marker),
  recovery codes as “N of M left” when enabled (`recovery_codes_remaining` of
  the issued `recovery_codes_total`), and how this Session was established
  (`authentication_method`, `authentication_strength`, and the provider
  display name from public discovery when the Session is external).
- not enabled: “Turn on two-step verification” with the one primary action.
- enabled: “Recovery codes” (make new codes, secondary) and “Turn off two-step
  verification” (secondary). Neither is the purple primary action.

“Verify this session” is never the default primary action. It appears as a
quiet action beside the Session fact only when two-step verification is on,
the service is available, and this Session is not yet multi-factor. A
sensitive operation that the server refuses for missing strong or fresh proof
offers the bounded `/account/reauthenticate?task=security` journey beside the
error.

The turn-off confirmation uses the `danger` Button for the confirming action
and a secondary “Keep it on” escape. Regeneration confirms before replacing
codes.

A recovery-restricted Session sees the restriction as a warning notice. With
the service available it offers setup; with the service disabled it says
setup cannot be finished now and offers only what it can do: check the status
again or sign out. Because a restricted Session has no confirmed name for the
shell footer, its sign-out stays in the task column.

An ordinary Session's display name is read from `GET /api/v1/users/me` only
after the status confirms the Session is not restricted, and is shown only in
the shell footer beside a quiet sign-out control.

## Secrets

Setup keys, provisioning URIs, authenticator inputs, and recovery codes live only in component
memory. They never enter navigation, browser storage, logs, or third-party
requests. Setup renders a scannable QR code locally from the bounded server
provisioning URI after validating its TOTP type, matching secret, and supported
six-digit parameters. The setup key is always shown beside the QR code, grouped
in fours in the data face, as the manual and nonvisual alternative. The setup
deadline is stated relative to now (“Finish within 10 minutes”) from the
server's `expires_at`, never as a timestamp, and describes the code field.
The QR code and key share the existing setup lifetime and are discarded together.
Enrollment accepts a complete six-digit authenticator code. Session challenges
also offer an explicit recovery-code field; changing entry method clears the
previous value and focuses the replacement field. Recovery codes
appear only in a successful activation or regeneration response, set in a
two-column data-face grid. An explicit download or copy, or manual saving, and
a required acknowledgement precede leaving that step. Copying writes to the
clipboard only on that explicit action. The acknowledgement is a User action,
not evidence that a file was saved. Refreshing or closing the page can lose
this one-time view.

## Recovery and focus

An uncertain activation or regeneration response leaves the mutation flow and
offers authoritative status refresh. Active MFA with lost codes requires
regeneration under a strong Session; status never reconstructs codes.
Restarting setup explicitly supersedes the pending secret. Returning from fresh
proof restores current status and never replays a sensitive action.

Feature transport validates response shapes and maps Problem codes into bounded
outcomes. Components render localized copy and share the existing task shell,
inputs, buttons, feedback, and announcement patterns. State replacements after
User actions focus the new heading; initial reads do not take focus.
