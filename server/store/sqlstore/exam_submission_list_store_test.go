// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/model"
)

func TestSubmissionListRowAcceptsAnEmptyDisplayName(t *testing.T) {
	t.Parallel()
	row := submissionListRow{
		SubmissionID: model.NewSubmissionID().String(), ExamID: model.NewExamID().String(),
		SittingID: model.NewExamSittingID().String(), AttemptID: model.NewExamAttemptID().String(),
		CandidateUserID: model.NewUserID().String(), Username: "candidate", SubmittedAt: model.NowUTC(),
		ReceiptCode: model.NewSubmissionReceiptCode().String(), IntegrityState: string(model.SubmissionIntegritySettled),
	}
	item, err := row.item()
	require.NoError(t, err)
	require.Empty(t, item.Candidate.DisplayName)
	require.Equal(t, row.Username, item.Candidate.Username)
}
