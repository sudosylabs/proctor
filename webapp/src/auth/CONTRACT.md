# Hosted authentication boundary contract

`PublicAccessDiscovery.ts` is the single browser authority for fetching and
runtime-validating `GET /api/v1/discovery`. It accepts only HTTP 200, discovery
version 1, the complete capability and Desktop-compatibility shapes, bounded
provider descriptors, and an optional complete public Institution
presentation. It parses `canonical_origin` once and requires its origin to
equal the origin serving the hosted page.

The module exposes only three outcomes: validated same-origin discovery,
origin mismatch, or safe unavailability. Malformed bodies, invalid origins,
unexpected statuses, and transport failures are unavailable; arbitrary server
values never reach presentation.

The shared boundary does not decide feature admission or visible recovery.
Login decides whether any admitted method exists, registration decides whether
public registration is enabled, Invitation acceptance treats Institution
presentation as optional, and Desktop authorization combines the validated
Institution with its authenticated User check. Tests use an in-memory
transport behind the same loader interface as the production HTTP adapter.

`passwordRejection.ts` is the one mapping from the server's new-password
rejection codes (length, simple pattern, common password, account identity,
recent reuse)
to a closed reason. Every page that sets a password reports the reason on its
password field; length keeps the page's own wording and the other reasons
share one explanation each, so the pages never disagree about the policy.

`sharedPublicAccessDiscovery` wraps the same loader in one page-lifetime
request. Concurrent and later loads for the same origin reuse it; a settled
`unavailable` result is not retained, so a route retry requests again and
republishes the result to subscribers. Only the hosted-page Institution
provider and login use it, because they need the same initial document;
routes that must observe current policy (registration after a rejection,
Desktop authorization, Invitation acceptance) keep requesting directly.

`HostedInstitution.tsx` mounts once around every hosted page (in `App`). It
starts the shared discovery request without blocking rendering and exposes
`useHostedInstitution(): { displayName?: string; supportAddress?: string }`
for page chrome. Fields stay empty while loading, after failure or origin
mismatch, and when discovery omits them. `supportAddress` is discovery's
operator-configured `support_address`, exposed only when it is a plain mailbox
that needs no escaping in a `mailto:` link (an unusual value is dropped rather
than failing discovery); it is never derived from other values and does not
depend on the Institution name. The context default is empty, so chrome
rendered outside the provider omits the Institution rather than failing.

`passwordPolicy.ts` reads discovery's `password_minimum_length` from the same
shared result (`usePasswordMinimumLength()`) for pages that set a new password.
Those pages state “At least {n} characters.” in the password field's
description and drop only the length sentence while discovery is loading or
unavailable; the server remains the authority and still reports rejections
through `passwordRejection.ts`.

Fragment extraction, CSRF behavior, and navigation remain separately owned by
their narrow modules in this directory.
