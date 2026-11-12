// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package batchitem

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestAuditValueNamesTheImportRowOnlyInsideAnImport(t *testing.T) {
	t.Parallel()
	id := model.NewOnboardingImportID()
	value := map[string]any{"user_id": "u"}

	require.Equal(t, value, New(Fields{}).AuditValue(value), "an ordinary call keeps its value")
	require.Nil(t, New(Fields{}).AuditValue(nil), "an ordinary call adds nothing")
	require.Equal(t, value, ImportAuditValue(value, id, 0), "a row number below 1 names nothing")

	named := New(Fields{OnboardingImportID: id, OnboardingImportRowNumber: 7}).AuditValue(value)
	require.Equal(t, map[string]any{"user_id": "u", "onboarding_import_id": id, "onboarding_import_row": 7}, named)
	require.Equal(t, map[string]any{"user_id": "u"}, value, "the caller's value is not changed")
	require.Equal(t, map[string]any{"onboarding_import_id": id, "onboarding_import_row": 7}, ImportAuditValue(nil, id, 7))
}
