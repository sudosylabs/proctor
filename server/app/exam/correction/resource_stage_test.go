// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestStageResourceContentReservesBeforeDeterministicWriteAndReturnsAuthoritativeMetadata(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	sha := strings.Repeat("a", 64)
	got, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{
		ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID,
		Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText,
		Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: sha, IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.False(t, got.StageID.IsZero())
	require.False(t, got.ResourceID.IsZero())
	require.Equal(t, model.ExamResourceMediaText, got.MediaType)
	require.Equal(t, int64(5), got.Size)
	require.Equal(t, sha, got.SHA256)
	require.False(t, got.ExpiresAt.IsZero())
	// Contract (guardrail 8): authorization precedes the audit, the audit begins
	// before the durable reservation, and bytes are stored only inside the
	// reservation before it becomes ready.
	require.Equal(t, []string{"access", "membership", "authorize", "audit.begin", "reserve", "content.store", "ready"}, f.order)
	require.NotEmpty(t, f.persistence.reservation.AuditEventID)
	require.False(t, f.persistence.reservation.AuditAt.IsZero())
	require.Equal(t, model.ActionExamSittingManage, f.authorizer.action, "authorization")
	require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: f.sittingID.String()}, f.authorizer.resource, "authorization")
	require.False(t, f.persistence.reservation.ManagerOverride, "authorization")
	for _, forbidden := range []string{"media_type", "size", "sha256", "stage_id", "private_reason", "display_name"} {
		_, ok := f.auditor.value[forbidden]
		require.False(t, ok, "unsafe audit field %q in %#v", forbidden, f.auditor.value)
	}
	wantIdempotency, prepareErr := prepareStageIdempotency(f.invocation, StageResourceContentCommand{
		ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID,
		Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText,
		Size: 5, ExpectedSHA256: sha, IdempotencyKey: "test-key",
	})
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.persistence.idempotency)
}

func TestStageResourceContentReadyReplaySkipsContentWrite(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	f.persistence.returnReady = true
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID, Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, 0, f.content.calls, "content")
	require.Equal(t, 0, f.persistence.readyCalls, "content")
}

type correctionCapacityError struct{}

func (correctionCapacityError) Error() string { return "content capacity is unavailable" }

func (correctionCapacityError) WorkCapacityExceeded() {}

func TestStageResourceContentCapacityRefusalLeavesOnlyPendingReservation(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	f.content.err = correctionCapacityError{}
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{
		ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID,
		Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText,
		Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "service.busy", fault.Code())
	require.ErrorIs(t, err, f.content.err)
	require.NotNil(t, f.persistence.reservation, "refused content became ready or published: store=%#v effects", f.persistence)
	require.Equal(t, 0, f.persistence.readyCalls, "refused content became ready or published: store=%#v effects", f.persistence)
	require.Nil(t, f.persistence.application, "refused content became ready or published: store=%#v effects", f.persistence)
	require.Empty(t, f.publisher.Calls(), "refused content became ready or published: store=%#v effects", f.persistence)
	// Contract (guardrail 8): the audit begins before the durable reservation that
	// the refused bytes were stored under.
	require.Equal(t, []string{"access", "membership", "authorize", "audit.begin", "reserve", "content.store"}, f.order)
}

func TestStageResourceContentUsesReservedTimeForConcurrentReplay(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	f.persistence.stageCreatedAt = f.at.Add(-time.Minute)
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{
		ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID,
		Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText,
		Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.True(t, f.content.createdAt.Equal(f.persistence.stageCreatedAt), "rendition created_at=%s want reserved", f.content.createdAt)
}

func TestStageResourceContentUsesExplicitOverrideWithoutCurrentUnitMembership(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	f.memberships.empty = true
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID, Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, model.ActionExamSittingManageOverride, f.authorizer.action, "action=%s reservation=%#v", f.authorizer.action, f.persistence.reservation)
	require.True(t, f.persistence.reservation.ManagerOverride, "action=%s reservation=%#v", f.authorizer.action, f.persistence.reservation)
}

func TestStageResourceContentStopsWhenCurrentManagerMembershipCannotBeRead(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	failure := errors.New("membership unavailable")
	f.memberships.err = failure
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID, Target: store.ExamCorrectionResourceAddition, MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.sitting.correction.unavailable", fault.Code())
	require.ErrorIs(t, err, failure)
	// Without current membership nothing is authorized or reserved.
	require.Empty(t, f.authorizer.action, "authorized without current membership")
	require.Nil(t, f.persistence.reservation, "reserved without current membership")
}

func TestStageReplacementPinsFileEntryFromExactBaseRevision(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	resourceID, entryID := model.NewExamResourceID(), model.NewFileEntryID()
	f.revisions.snapshot = &model.ExamRevision{ID: f.baseRevisionID, ExamID: f.examID, Resources: []model.ExamRevisionResource{{ResourceID: resourceID, FileEntryID: entryID}}}
	_, err := f.service.StageExamSittingCorrectionResourceContent(context.Background(), f.invocation, StageResourceContentCommand{ExamID: f.examID, SittingID: f.sittingID, BaseRevisionID: f.baseRevisionID, Target: store.ExamCorrectionResourceReplacement, ResourceID: resourceID, MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Nil(t, f.persistence.reservation.Entry)
	require.Equal(t, entryID, f.persistence.reservation.FileEntryID)
	require.Equal(t, resourceID, f.persistence.reservation.ResourceID)
}
