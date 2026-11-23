// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package filecontent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/packages/vfs"
	"github.com/sudosylabs/proctor/packages/vfs/memory"
	"github.com/sudosylabs/proctor/server/model"
)

func TestAuditExportStoresVerifiesOpensAndPurgesTheFile(t *testing.T) {
	ctx := context.Background()
	content, err := New(memory.New(), Policy{MaximumConcurrentOperations: 1}, nil)
	require.NoError(t, err)
	id, attempt := model.NewAuditExportID(), model.NewJobAttemptID()
	const file = "id,action\n1,role.manage\n"

	stored, err := content.BuildAuditExport(ctx, id, attempt, func(w io.Writer) error {
		_, writeErr := io.WriteString(w, file)
		return writeErr
	})
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(file))
	require.Equal(t, int64(len(file)), stored.SizeBytes)
	require.Equal(t, hex.EncodeToString(digest[:]), stored.SHA256)

	body, err := content.OpenAuditExport(ctx, id, attempt)
	require.NoError(t, err)
	data, err := io.ReadAll(body)
	require.NoError(t, body.Close())
	require.NoError(t, err)
	require.Equal(t, file, string(data))

	require.NoError(t, content.PurgeAuditExport(ctx, id, attempt))
	_, err = content.OpenAuditExport(ctx, id, attempt)
	require.Error(t, err, "a purged file still opens")
	require.NoError(t, content.PurgeAuditExport(ctx, id, attempt), "a second purge of an absent file failed")
}

func TestAuditExportStoresNothingWhenTheWriterFails(t *testing.T) {
	ctx := context.Background()
	fs := memory.New()
	content, err := New(fs, Policy{MaximumConcurrentOperations: 1}, nil)
	require.NoError(t, err)
	id, attempt := model.NewAuditExportID(), model.NewJobAttemptID()
	failure := errors.New("listing unavailable")
	_, err = content.BuildAuditExport(ctx, id, attempt, func(w io.Writer) error {
		_, _ = io.WriteString(w, strings.Repeat("x", 64))
		return failure
	})
	require.ErrorIs(t, err, failure)
	_, err = fs.Stat(ctx, auditExportKey(id, attempt))
	require.ErrorIs(t, err, vfs.ErrNotFound, "a failed build left a file")
}
