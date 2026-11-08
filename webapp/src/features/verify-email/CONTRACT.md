# Verify-email route contract

This file is the exact contract for the server-hosted
`/account/verify-email` feature. It presents and consumes the existing
purpose-specific email-verification User Token without adding a new account,
Session, profile, or persistence contract; its only additional transport is
the read-only address lookup below.
[`../../../DESIGN_SYSTEM.md`](../../../DESIGN_SYSTEM.md) owns shared
presentation and interaction rules. The server identity and HTTP contracts
remain authoritative for token issuance, expiry, consumption, rate limiting,
and Problem Details.

## Purpose and boundary

The page lets the holder deliberately verify the one email address already
bound to a single-use verification token. It never selects a User or email,
changes profile data, creates a Session, authenticates a browser, grants an
Affiliation, membership, or Role Binding, or infers that registration granted
Institution access.

The token is not consumed automatically. Mail scanners, link previews, and a
browser preloading the page must not spend a one-time credential; the person
must activate the explicit `Confirm email address` button. Completion sends exactly one
same-origin `POST /api/v1/auth/email-verification/complete` request containing
only `{ "token": "<captured token>" }`.

## URL and credential handling

The mail link supplies the token as the sole `token` value in the URL fragment.
The nonvisual bootstrap removes the complete fragment from history before
interpreting it and passes a purpose-specific `email_verification_token` only
in memory. A missing, empty, malformed, or multiply-valued fragment yields the
same unusable-link presentation and makes no API request.

The token never enters a query string, document title, DOM text or attribute,
storage, cookie, log, error, analytics event, screenshot, or server-provided
prose. The feature releases its local token reference after verified or invalid
completion. An unavailable result retains it only in memory for explicit
retry.

## Document and semantic structure

The page requests the stable title `Confirm email · Proctor`. It uses the
shared hosted shell column and the shared `TaskState` hierarchy: the quiet
`Confirm email` kind line, one state-specific `h1`, a lede, and the state's
actions, plus one persistent polite live region (`TaskStateAnnouncement`).
There is no separate centred layout, outcome marker, or evidence note. The
kind line is teal when verified, coral when the link is unusable, amber when
the result is unavailable, and muted otherwise; the heading and lede always
state the outcome in words. The ready lede states the single-use, 24-hour
lifetime (the server default).

On entry with a captured token the route makes one read-only `POST
/api/v1/auth/email-verification/inspect` with the same `{ "token": … }` body.
It never consumes or extends the token. When it names the address, the ready
lede becomes “Confirm {address} as your account’s email address. …” with the
address untranslated in `strong`. Any lookup failure, including an unusable
token or a rate limit, leaves the generic ready lede and the explicit action
unchanged: the page still never decides on load that a link is unusable, and
the lookup neither takes focus nor delays the button.

There is no autofocus on initial load. After a user-triggered completion or
retry resolves into a new state, focus moves to the resulting heading so the
context change is immediate to keyboard and assistive-technology users. A
retry that ends in the same unavailable state leaves focus on the retry
button. Pending work keeps the current state on screen, disables duplicate
activation with the button's pending label, and announces it. Reload and Back return to the
clean path and therefore cannot replay a removed credential.

All authored copy uses `webapp.verify_email.*` messages in `server/i18n`; the
generated browser catalog is the runtime view. Raw Problem title, detail, type,
instance, and unknown code values are never rendered.

## State machine

| State | Entry | Available action |
| --- | --- | --- |
| Ready | One purpose-specific token was captured | Explicit `Confirm email address` button and quiet same-origin `Back to sign in` link |
| Pending | The explicit completion request is pending | The current state with its button disabled and pending; no duplicate request |
| Verified | The API returns exactly `204` | Primary same-origin `Sign in` link |
| Link unavailable | No token exists, or the API returns `request.invalid` or `authentication.account_token.invalid` | Primary same-origin `Sign in` link |
| Verification unavailable | Network failure, malformed response, rate limit, or any other bounded API failure | Explicit `Try again` button and quiet `Back to sign in` link |

The browser does not attempt to determine token expiry when the page opens.
The credential is opaque to the page, and only completion's check of its
active, unconsumed state against authoritative PostgreSQL time decides the
outcome when the person activates `Confirm email address`; the address lookup
is presentation only. An expired, superseded, consumed, malformed, or
concurrently consumed token therefore enters the same link-unavailable state;
the copy may name those possibilities but never claims which one occurred.

The link-unavailable state does not distinguish malformed, expired,
superseded, already-used, or concurrent-loser tokens. The unavailable state
does not claim whether a request committed; retry may therefore resolve as
verified, unavailable, or the same concealed invalid-link state.

## Security and privacy invariants

- Navigation to the route is never proof that an email is verified.
- Only an exact `204` response proves completion to the page.
- The public completion and lookup requests contain no Session credential or
  email input.
- No state automatically redirects, resends mail, signs in, or requests a new
  token.
- Requesting another verification message remains the authenticated API's
  responsibility after sign-in.
- Theme, locale, focus, motion, and responsive state carry no credential or
  identity data.
- The page preserves the server-owned no-store, Content Security Policy,
  referrer, frame, MIME, and origin contract.

## Acceptance

The feature is complete only when tests cover fragment removal before render,
missing and malformed fragment behavior, no automatic consumption, the exact
request body, duplicate prevention, `204`, concealed invalid-token outcomes,
network or `503` recovery, explicit retry, clean-path reload, title and DOM
secret absence, focus and live announcements, keyboard order, forced colors,
reduced motion, `200%` zoom, light and dark themes, and compact, tablet, and
desktop design-system viewports.
