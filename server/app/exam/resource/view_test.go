// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestOpenUsesExactAuthoritativeRenditionWithoutStorageDiscovery(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	record := testRecord(f.examID, model.NewExamResourceID(), model.NewFileEntryID(), model.NewFileRevisionID(), 1)
	f.persistence.items = []store.ExamResourceRecord{record}
	opened, err := f.service.OpenExamResource(context.Background(), f.inv, f.examID, record.Resource.ID)
	require.NoError(t, err)
	defer opened.Body.Close()
	body, _ := io.ReadAll(opened.Body)
	require.Equal(t, "protected", string(body), "body=%q content=%#v", body, f.content)
	require.Equal(t, record.Resource.SelectedFileRevisionID, f.content.openedRevision, "body=%q content=%#v", body, f.content)
	require.Equal(t, record.Rendition.ID, f.content.openedRendition, "body=%q content=%#v", body, f.content)
}
