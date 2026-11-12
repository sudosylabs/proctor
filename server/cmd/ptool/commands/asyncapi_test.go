// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAsyncAPICommandBuildsAndChecksArtifact(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "asyncapi", "fragments"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "schemas", "realtime"), 0o755))
	writeOpenAPITestFile(t, filepath.Join(root, "asyncapi", "base.yaml"), `asyncapi: 3.0.0
info:
  title: Proctor Realtime API
  version: 1.0.0
`)
	writeOpenAPITestFile(t, filepath.Join(root, "asyncapi", "fragments", "websocket.yaml"), `channels:
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
      payload:
        $ref: '../../schemas/realtime/hello.json'
      examples:
        - payload:
            resumed: false
`)
	writeOpenAPITestFile(t, filepath.Join(root, "schemas", "realtime", "hello.json"),
		`{"type": "object", "required": ["resumed"], "properties": {"resumed": {"type": "boolean"}}}`)
	output := filepath.Join(root, "asyncapi.json")
	var commandOutput bytes.Buffer
	commandArguments := func(action string) []string {
		return []string{"asyncapi", "--root", root, "--output", output, action}
	}

	require.NoError(t, Execute(context.Background(), commandArguments("build"), &commandOutput, &commandOutput))
	require.Contains(t, commandOutput.String(), "wrote")
	commandOutput.Reset()
	require.NoError(t, Execute(context.Background(), commandArguments("check"), &commandOutput, &commandOutput))
	require.Contains(t, commandOutput.String(), "validated")

	require.NoError(t, os.WriteFile(output, []byte("{}\n"), 0o600))
	err := Execute(context.Background(), commandArguments("check"), &commandOutput, &commandOutput)
	require.ErrorContains(t, err, "is stale")
}
