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

func TestOpenAPICommandBuildsAndChecksArtifact(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	source := filepath.Join(directory, "openapi")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "fragments", "system"), 0o755))
	writeOpenAPITestFile(t, filepath.Join(source, "base.yaml"), `openapi: 3.1.0
info:
  title: Proctor API
  version: 1.0.0
tags:
  - name: System
    description: Runtime health.
`)
	writeOpenAPITestFile(t, filepath.Join(source, "fragments", "system", "health.yaml"), `paths:
  /health/live:
    get:
      operationId: getHealthLive
      summary: Check liveness
      tags: [System]
      security: []
      x-proctor-auth: public
      x-proctor-error-codes: []
      x-proctor-idempotency: none
      responses:
        "204":
          description: The process is live.
`)
	output := filepath.Join(directory, "openapi.json")
	var commandOutput bytes.Buffer
	arguments := []string{"openapi", "--source", source, "--output", output}
	commandArguments := func(action string) []string {
		return append(append([]string(nil), arguments...), action)
	}
	require.NoError(t, Execute(context.Background(), commandArguments("build"), &commandOutput, &commandOutput))
	require.Contains(t, commandOutput.String(), "wrote")
	commandOutput.Reset()
	require.NoError(t, Execute(context.Background(), commandArguments("check"), &commandOutput, &commandOutput))
	require.Contains(t, commandOutput.String(), "validated")

	// A compiled plugin's fragments join the contract, each operation marked
	// as the plugin's.
	plugins := filepath.Join(directory, "plugin")
	require.NoError(t, os.MkdirAll(filepath.Join(plugins, "execution", "openapi"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(plugins, "zipexport"), 0o755))
	writeOpenAPITestFile(t, filepath.Join(plugins, "execution", "openapi", "status.yaml"), `paths:
  /api/v1/plugins/execution/status:
    get:
      operationId: getExecutionStatus
      summary: Check the execution plugin
      tags: [System]
      security: []
      x-proctor-auth: public
      x-proctor-error-codes: []
      x-proctor-idempotency: none
      responses:
        "204":
          description: The plugin is ready.
`)
	withPlugins := append(append([]string(nil), arguments...), "--plugins", plugins, "build")
	require.NoError(t, Execute(context.Background(), withPlugins, &commandOutput, &commandOutput))
	compiled, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(compiled), `"x-proctor-plugin": "execution"`)
	writeOpenAPITestFile(t, filepath.Join(plugins, "execution", "openapi", "stray.yaml"), `paths:
  /api/v1/stray:
    get:
      operationId: getStray
      summary: Stray
      tags: [System]
      security: []
      x-proctor-auth: public
      x-proctor-error-codes: []
      x-proctor-idempotency: none
      responses:
        "204":
          description: Stray.
`)
	err = Execute(context.Background(), withPlugins, &commandOutput, &commandOutput)
	require.ErrorContains(t, err, "is not under /api/v1/plugins/execution/", "a plugin documents only its own paths")

	require.NoError(t, os.WriteFile(output, []byte("{}\n"), 0o600))
	commandOutput.Reset()
	err = Execute(context.Background(), commandArguments("check"), &commandOutput, &commandOutput)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is stale")
}

func writeOpenAPITestFile(t *testing.T, name, contents string) {
	t.Helper()
	require.NoError(t, os.WriteFile(name, []byte(contents), 0o600))
}
