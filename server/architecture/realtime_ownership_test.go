// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealtimePeerContractsAreDeclaredOnlyByChild(t *testing.T) {
	t.Parallel()

	peerContracts := map[string]bool{
		"websocket.publish":              true,
		"authentication.session_revoked": true,
		"authorization.invalidated":      true,
	}
	serverRoot := filepath.Clean("..")
	err := filepath.WalkDir(serverRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != serverRoot && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err == nil && peerContracts[value] {
				relative, relErr := filepath.Rel(serverRoot, path)
				assert.NoError(t, relErr, "peer contract %q is owned outside app/realtime in %s", value, path)
				assert.True(t, strings.HasPrefix(filepath.ToSlash(relative), "app/realtime/"), "peer contract %q is owned outside app/realtime in %s", value, path)
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
}

func TestRealtimeAdaptationProvenanceFollowsOwnedSource(t *testing.T) {
	t.Parallel()

	packageDoc, err := os.ReadFile(filepath.Join("..", "app", "realtime", "doc.go"))
	require.NoError(t, err)
	notice, err := os.ReadFile(filepath.Join("..", "NOTICE"))
	require.NoError(t, err)
	for name, content := range map[string]string{"app/realtime/doc.go": string(packageDoc), "NOTICE": string(notice)} {
		assert.Contains(t, content, "10b780cb097b2ec94ab0f9df7ebcbd5b7850f13f", "%s does not preserve the exact Mattermost revision", name)
	}
	assert.Contains(t, string(notice), "server/app/realtime/", "NOTICE does not point to the canonical Realtime child package")
}
