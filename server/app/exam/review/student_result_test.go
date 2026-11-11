// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func TestCandidateResultRequiresSessionOwnershipAndSanitizesRemarks(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.persistence.student = &model.StudentResult{ReviewID: f.reviewID, SubmissionID: f.submissionID,
		AttemptID: f.attemptID, CandidateUserID: f.userID,
		StudentRemarksMarkdown: "Safe **text** <script>alert(1)</script> ![remote](https://tracker.example/x)", ReleasedAt: f.at}
	result, err := f.service.GetStudentExamResult(context.Background(), f.inv, f.attemptID)
	require.NoError(t, err)
	require.Equal(t, "Safe **text**  remote", result.StudentRemarksMarkdown)
	wrongPrincipal := f.inv.Principal()
	wrongPrincipal.UserID = model.NewUserID()
	wrong := invocation.New(wrongPrincipal, f.inv.RequestMetadata())
	_, err = f.service.GetStudentExamResult(context.Background(), wrong, f.attemptID)
	require.Error(t, err, "GetStudentResult() accepted another candidate")
	patPrincipal := f.inv.Principal()
	patPrincipal.CredentialType = model.CredentialPersonalAccessToken
	patPrincipal.SessionID = ""
	pat := invocation.New(patPrincipal, f.inv.RequestMetadata())
	_, err = f.service.GetStudentExamResult(context.Background(), pat, f.attemptID)
	require.Error(t, err, "GetStudentResult() accepted non-Session principal")
}
