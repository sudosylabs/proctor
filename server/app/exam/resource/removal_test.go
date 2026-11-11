// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestRemovePassesOwnedIdempotencyToStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	resourceID := model.NewExamResourceID()
	_, err := f.service.RemoveExamResource(context.Background(), f.inv, RemoveCommand{ExamID: f.examID,
		ResourceID: resourceID, ExpectedDraftRevision: 1, IdempotencyKey: "remove-key"})
	require.NoError(t, err)
	want, err := prepareResourceIdempotency(f.inv, idempotencyOperationRemoveResource, "remove-key",
		f.examID, 1, resourceID.String(), "", "", "", 0, "", nil)
	require.NoError(t, err)
	require.Equal(t, want, f.persistence.idempotency)
}
