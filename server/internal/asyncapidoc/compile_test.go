// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package asyncapidoc

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

const validBase = `asyncapi: 3.0.0
info:
  title: Proctor Realtime API
  version: 1.0.0
defaultContentType: application/json
`

const validFragment = `channels:
  websocket:
    address: /api/v1/websocket
    messages:
      hello:
        $ref: '#/components/messages/hello'
operations:
  sendEvent:
    action: send
    channel:
      $ref: '#/channels/websocket'
    messages:
      - $ref: '#/channels/websocket/messages/hello'
components:
  messages:
    hello:
      name: hello
      payload:
        $ref: '../../schemas/realtime/hello.json'
      examples:
        - payload:
            connection_id: abc
            at: '2026-08-01T09:30:00Z'
`

const validSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["connection_id", "at"],
  "properties": {
    "connection_id": {"type": "string", "minLength": 1},
    "at": {"$ref": "instant.json"}
  }
}`

const instantSchema = `{"type": "string", "format": "date-time"}`

func validSource() fstest.MapFS {
	return fstest.MapFS{
		"asyncapi/base.yaml":                {Data: []byte(validBase)},
		"asyncapi/fragments/websocket.yaml": {Data: []byte(validFragment)},
		"schemas/realtime/hello.json":       {Data: []byte(validSchema)},
		"schemas/realtime/instant.json":     {Data: []byte(instantSchema)},
	}
}

func TestCompileBundlesSharedSchemasAndValidates(t *testing.T) {
	t.Parallel()

	compiled, err := Compile(validSource(), "asyncapi")
	require.NoError(t, err)

	var document map[string]any
	require.NoError(t, json.Unmarshal(compiled, &document))
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	require.Equal(t, []string{"hello", "instant"}, sortedKeys(schemas))
	hello := components["messages"].(map[string]any)["hello"].(map[string]any)
	require.Equal(t, map[string]any{"$ref": "#/components/schemas/hello"}, hello["payload"])
	at := schemas["hello"].(map[string]any)["properties"].(map[string]any)["at"]
	require.Equal(t, map[string]any{"$ref": "#/components/schemas/instant"}, at)

	compiledAgain, err := Compile(validSource(), "asyncapi")
	require.NoError(t, err)
	require.Equal(t, string(compiled), string(compiledAgain), "compilation is not deterministic")
}

func TestCompileRejectsInvalidSources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		edit    func(fstest.MapFS)
		message string
	}{
		{
			name: "base owns a fragment key",
			edit: func(source fstest.MapFS) {
				source["asyncapi/base.yaml"] = &fstest.MapFile{Data: []byte(validBase + "channels: {}\n")}
			},
			message: `top-level key "channels" belongs in a fragment`,
		},
		{
			name: "fragment declares a message twice",
			edit: func(source fstest.MapFS) {
				source["asyncapi/fragments/again.yaml"] = &fstest.MapFile{Data: []byte(`components:
  messages:
    hello:
      payload:
        type: object
`)}
			},
			message: "components.messages.hello is already declared",
		},
		{
			name: "schema file nothing references",
			edit: func(source fstest.MapFS) {
				source["schemas/realtime/orphan.json"] = &fstest.MapFile{Data: []byte(`{"type": "object"}`)}
			},
			message: "schemas/realtime/orphan.json is not referenced",
		},
		{
			name: "keyword the two dialects do not share",
			edit: func(source fstest.MapFS) {
				source["schemas/realtime/instant.json"] = &fstest.MapFile{Data: []byte(`{"type": "string", "$defs": {}}`)}
			},
			message: `keyword "$defs" is not in the subset`,
		},
		{
			name: "reference outside the schema directory",
			edit: func(source fstest.MapFS) {
				source["asyncapi/fragments/websocket.yaml"] = &fstest.MapFile{Data: []byte(
					strings.Replace(validFragment, "../../schemas/realtime/hello.json", "../hello.json", 1))}
			},
			message: "must name a .json file in schemas/realtime",
		},
		{
			name: "example that does not match its payload",
			edit: func(source fstest.MapFS) {
				source["asyncapi/fragments/websocket.yaml"] = &fstest.MapFile{Data: []byte(
					strings.Replace(validFragment, "'2026-08-01T09:30:00Z'", "yesterday", 1))}
			},
			message: "components.messages.hello example 0",
		},
		{
			name: "message without an example",
			edit: func(source fstest.MapFS) {
				fragment := validFragment[:strings.Index(validFragment, "      examples:")]
				source["asyncapi/fragments/websocket.yaml"] = &fstest.MapFile{Data: []byte(fragment)}
			},
			message: "components.messages.hello needs at least one example",
		},
		{
			name: "document the AsyncAPI schema rejects",
			edit: func(source fstest.MapFS) {
				source["asyncapi/fragments/websocket.yaml"] = &fstest.MapFile{Data: []byte(
					strings.Replace(validFragment, "action: send", "action: publish", 1))}
			},
			message: "is not valid AsyncAPI 3.0.0",
		},
		{
			name: "operation message from another channel",
			edit: func(source fstest.MapFS) {
				source["asyncapi/fragments/websocket.yaml"] = &fstest.MapFile{Data: []byte(
					strings.Replace(validFragment, "'#/channels/websocket/messages/hello'", "'#/channels/other/messages/hello'", 1))}
				source["asyncapi/fragments/other.yaml"] = &fstest.MapFile{Data: []byte(`channels:
  other:
    address: /other
    messages:
      hello:
        $ref: '#/components/messages/hello'
`)}
			},
			message: "is not a message of channel websocket",
		},
		{
			name: "message no channel lists",
			edit: func(source fstest.MapFS) {
				source["asyncapi/fragments/extra.yaml"] = &fstest.MapFile{Data: []byte(`components:
  messages:
    stray:
      payload:
        type: object
      examples:
        - payload: {}
`)}
			},
			message: "components.messages.stray is not listed by any channel",
		},
		{
			name: "reference that does not resolve",
			edit: func(source fstest.MapFS) {
				source["asyncapi/fragments/websocket.yaml"] = &fstest.MapFile{Data: []byte(
					strings.Replace(validFragment, "'#/components/messages/hello'", "'#/components/messages/missing'", 1))}
			},
			message: "does not resolve",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := validSource()
			tt.edit(source)
			_, err := Compile(source, "asyncapi")
			require.ErrorContains(t, err, tt.message)
		})
	}
}
