---
name: api-design
description: Design or change Proctor's HTTP or WebSocket API. Use for routes, handlers, request and response shapes, Problem Details and error codes, pagination, idempotency, realtime events, and the OpenAPI and AsyncAPI documents.
---

# Designing Proctor's API

You are an API designer. Clients are the webapp, the desktop app and
third-party scripts, and all of them depend on the API being predictable.
Consistency matters more than local cleverness. An endpoint that looks like
every other endpoint is a good endpoint.

## How to work

- **Start from the closest existing endpoint and copy its shape.**
- **If a design question has no precedent in Proctor,** look at how GitHub's
  or Stripe's API settles it. If it is still unclear, present the options to
  the owner before building.
- **Change the server, the OpenAPI document and the clients together.**
  Before the first release, there is no compatibility shim.

## Handlers

- **What a handler does:** decode and bound the input, build the invocation,
  call one use case, and map the result to a response. Authority and
  transactions stay in the use case.
- **Route declaration:**
  - routes are `route.Endpoint` literals in one catalogue
    (`httpapi/resources.go`);
  - every route states its credential and assurance class (`Auth`), even
    `route.AuthPublic`;
  - path parameters use closed kinds, read with `route.PathID[model.XID]`.
- **Decoding:**
  - the body limit applies first;
  - the content type is exactly `application/json`;
  - one value, with no unknown fields and nothing trailing;
  - `route.Optional[T]` covers omitted, zero and null in PATCH.
- **Models are not DTOs.** Response structs are written for the wire, so a
  new sensitive model field can't leak. The one exception is a model-owned
  canonical document.

## Wire dialect

- **Names:** paths are plural kebab-case. JSON, query and path names are
  snake_case.
- **Times:** RFC 3339 via `model.FormatRFC3339`. Omit an absent optional
  instant, never `null`. Lifecycle members are `<verb>ed_at`.
- **Server-owned fields:** create bodies never carry the server's `id`,
  timestamps or the parent IDs fixed by the path.
- **Collections:**
  - the envelope is `{"items", "next_cursor"}`;
  - paging uses only `limit` (default 50, max 200) and `cursor`;
  - no bare arrays, `Link` headers, `has_more` or offsets.
- **Cursors** are opaque and grant no authority. Every page reauthorizes.
- **Revision fences:** the fence is the body's `expected_revision`, and
  `If-Match` is only for binary content with a strong ETag.
- **`Idempotency-Key`:** declared per route. A replay returns the original
  result without repeating effects or revealing whether it ran.

## Errors

- **Codes:** errors are Problem Details with stable codes,
  `<domain>[.<sub>].<condition>`. Add a code only in
  `server/httpapi/route/error_catalog.yaml`, then run
  `make -C server errors-generate`. The condition word decides the status.
- **Error tables:** a route's table lists exactly what it can return. Change
  it and the OpenAPI `x-proctor-error-codes` together.
- **Localization:** only `title` and `detail` are localized. `code` is machine
  state.

## Realtime

- **Event list:** events form a closed list in `app/realtime/message.go`. Each
  has a constructor, an AsyncAPI message and a schema in
  `server/schemas/realtime/`.
- **Payloads:** an event carries safe IDs, state, revision and time; never
  paths, content or credentials. Missing an event means refetching. Clients
  ignore unknown event names.
- **Socket limits:** a token bucket of 64 burst and 16 per second per Session.
  A cookie upgrade needs an exact origin match.

## OpenAPI

- **Where to edit:** the fragment for the area under
  `server/openapi/fragments/`. Never hand-edit `server/openapi.json` or the
  generated reference.
- **Examples** are synthetic.
- **Growable catalogues** (error codes, Job types, audit actions) are closed
  enums in requests and open in responses (`anyOf: [{$ref}, {type: string}]`).
- **Run:** `make -C server openapi-build`, then `openapi-check`, then
  `openapi-agreement`.

## Good examples to read first

- `server/httpapi/user_settings.go`
- `server/httpapi/user_profile.go` (`search`)
- `server/httpapi/route/doc.go`
- `server/openapi/fragments/users/users-settings.yaml`
