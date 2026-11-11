// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

func TestRecordsRejectsSelfWaiverWithDurableDenial(t *testing.T) {
	f, records, persistence, authorizer := newRecordsFixture(t)
	persistence.scope.Scope.SubmissionID = model.NewSubmissionID()
	persistence.scope.CandidateUserID = f.userID
	_, err := records.WaiveSubmissionReview(context.Background(), f.invocation, WaiveReviewCommand{Scope: persistence.scope.Scope,
		ReasonCode: "review_not_required", PrivateReason: "Private institutional rationale", IdempotencyKey: "waiver-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "self waiver")
	require.Equal(t, "resource.not_found", fault.Code(), "self waiver")
	require.True(t, authorizer.denied, "self waiver")
	require.Nil(t, persistence.waiver, "self waiver")
	require.Equal(t, []string{"records.resolve", "records.deny_self"}, *f.order)
	_, err = records.GetSubmissionReviewWaiver(context.Background(), f.invocation, persistence.scope.Scope)
	require.Error(t, err, "candidate read private waiver rationale")
}

func TestRecordsWaiverKeepsPrivateRationaleOutOfAudit(t *testing.T) {
	f, records, persistence, _ := newRecordsFixture(t)
	persistence.scope.Scope.SubmissionID = model.NewSubmissionID()
	private := "Private candidate evidence and rationale."
	value, err := records.WaiveSubmissionReview(context.Background(), f.invocation, WaiveReviewCommand{Scope: persistence.scope.Scope,
		ExpectedReviewRevision: 3, ExpectedDiscrepancyCount: 2, ReasonCode: "review_not_required", PrivateReason: private, IdempotencyKey: "waiver-key"})
	require.NoError(t, err)
	require.Equal(t, private, value.PrivateReason)
	require.Equal(t, private, persistence.waiver.PrivateReason)
	require.Equal(t, int64(2), persistence.waiver.ExpectedDiscrepancyCount)
	for _, field := range f.auditor.value {
		text, ok := field.(string)
		if ok {
			require.NotContains(t, text, private, "private rationale entered audit")
		}
	}
	require.Equal(t, "review_not_required", f.auditor.value["reason_code"])
	require.Nil(t, f.auditor.value["private_reason"])
}
