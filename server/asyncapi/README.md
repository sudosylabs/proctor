# AsyncAPI authoring

This directory is the human authoring interface for the realtime protocol: the
frames sent over `/api/v1/websocket` after the HTTP upgrade. The upgrade itself
is the OpenAPI operation `connectWebSocket`. [`../asyncapi.json`](../asyncapi.json)
is the deterministic, reviewed AsyncAPI 3.0 artifact that the conformance tests
and the documentation site read; it is generated and must not be edited by
hand.

## Layout

[`base.yaml`](base.yaml) owns the document metadata and the server.
[`fragments/connection/`](fragments/connection/) holds the channel, its close
codes, the operations, and the protocol frames (`hello`, `resync_required`, the
commands and their replies). [`fragments/events/`](fragments/events/) holds one
file of event messages per product area.

Payload shapes are JSON Schema files in
[`../schemas/realtime/`](../schemas/realtime/), one per payload plus the shared
frames and scalars. Messages point at them with a relative `$ref`; the build
copies each one into `components.schemas`. A schema uses only the keywords
JSON Schema draft-07 (the AsyncAPI default schema format) and 2020-12
(OpenAPI 3.1) share, so OpenAPI can reference the same file; a shape two
payloads share is its own file rather than a `definitions` or `$defs` entry.
A file nothing references fails the build.

An event message's payload is an `allOf` of the event frame and one object that
narrows it: `event` is a `const` of the wire name, `action` a `const` of the
subscription action or absent for an event addressed to one User, `user_id`
required when the event is addressed to one User, and `data` the payload
schema, or forbidden with `not` when the event has none. Tag it `event` and its
product area. Every message has at least one synthetic example.

## Add or change an event

1. Declare the message in `app/realtime/message.go` and build the event in a
   constructor that sets `Name` from it. Two audiences that receive different
   payloads or authorization for one fact get two messages with one name.
2. Add or change its payload schema in `../schemas/realtime/` and its message
   in the area's file under `fragments/events/`; list it on the channel and on
   the `sendEvent` operation in `fragments/connection/websocket.yaml`.
3. Add the constructor to `conformanceConstructors` in
   `app/realtime/message_conformance_test.go`.
4. Regenerate and check:

   ```sh
   make -C server asyncapi-build
   go test ./app/realtime ./websocket
   ```

`ptool asyncapi check` validates the compiled document against the official
AsyncAPI 3.0.0 JSON Schema, resolves every `$ref`, checks that every message is
on the channel and used by an operation, validates every example against its
payload, and fails when `asyncapi.json` is stale. The `app/realtime` tests fail
when a declared message has no constructor entry or AsyncAPI message, or when a
constructor's data does not match its schema; the `websocket` tests do the same
for the protocol frames and the close codes.
