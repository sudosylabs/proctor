// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCreateStagesVerifiedBytesBeforeAuditAndAtomicVisibility(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.memberships.items = []*model.AcademicUnitMember{{AcademicUnitID: f.unitID, UserID: f.userID}}
	got, err := f.service.CreateExamResource(context.Background(), f.inv, CreateCommand{ExamID: f.examID, ExpectedDraftRevision: 1, DisplayName: "  Reference  ", DescriptionMarkdown: "Read **carefully**.", MediaType: model.ExamResourceMediaMarkdown, Body: strings.NewReader("# Notes"), Size: 7, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, "Reference", got.Resource.DisplayName)
	require.False(t, got.Resource.FileEntryID.IsZero())
	require.Equal(t, got.Rendition.RevisionID, got.Resource.SelectedFileRevisionID)
	require.Equal(t, int64(2), got.DraftRevision)
	// Contract: authorization precedes the reservation, verified bytes are stored
	// inside the reservation before the audit begins (guardrail 8) and the
	// atomic finalization commits before its effect (guardrail 14).
	require.Equal(t, []string{"access", "membership", "authorize", "list", "reserve", "content.store", "audit.begin", "finalize", "effect"}, f.order)
	// Contract: the committed create publishes exactly one Draft update, at the
	// revision the Store committed.
	require.Equal(t, []realtimetest.Call{draftUpdated(t, f.examID, 2)}, f.publisher.Calls())
	require.Equal(t, "Read **carefully**.", f.persistence.finalization.Resource.DescriptionMarkdown)
	require.NotEmpty(t, f.persistence.finalization.Rendition.SHA256)
	require.Equal(t, 1, f.content.storeCalls, "content storage calls")
	wantIdempotency, prepareErr := prepareResourceIdempotency(f.inv, idempotencyOperationAddResource, "test-key",
		f.examID, 1, "", "Reference", "Read **carefully**.", model.ExamResourceMediaMarkdown, 7, strings.Repeat("a", 64), nil)
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.persistence.idempotency)
}

func TestCreateInvalidContentRemainsInvisibleAndUnaudited(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.content.storeErr = testInvalidContentError{}
	_, err := f.service.CreateExamResource(context.Background(), f.inv, CreateCommand{ExamID: f.examID, ExpectedDraftRevision: 1, DisplayName: "Reference", MediaType: model.ExamResourceMediaPDF, Body: strings.NewReader("bad"), Size: 3, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.resource.content_invalid", fault.Code())
	require.Nil(t, f.persistence.finalization, "partial visibility/audit: finalize")
	require.False(t, f.auditor.began, "partial visibility/audit: finalize")
}

func TestContentCapacityRefusalRemainsInvisibleAndUnaudited(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"create", "replace"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			f.content.storeErr = testCapacityError{}
			var err error
			if operation == "create" {
				_, err = f.service.CreateExamResource(context.Background(), f.inv, CreateCommand{ExamID: f.examID, ExpectedDraftRevision: 1, DisplayName: "Reference", MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
			} else {
				resourceID := model.NewExamResourceID()
				f.persistence.items = []store.ExamResourceRecord{testRecord(f.examID, resourceID, model.NewFileEntryID(), model.NewFileRevisionID(), 1)}
				_, err = f.service.ReplaceExamResourceContent(context.Background(), f.inv, ReplaceContentCommand{ExamID: f.examID, ResourceID: resourceID, ExpectedDraftRevision: 1, MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
			}
			var fault *apperror.Error
			require.ErrorAs(t, err, &fault)
			require.Equal(t, "service.busy", fault.Code())
			require.ErrorIs(t, err, f.content.storeErr)
			require.Nil(t, f.persistence.finalization, "partial visibility/audit: finalize")
			require.False(t, f.auditor.began, "partial visibility/audit: finalize")
			require.Empty(t, f.publisher.Calls(), "partial visibility/audit: finalize")
		})
	}
}

func TestFinalizeReplayDoesNotRepublish(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.replayed = true
	_, err := f.service.CreateExamResource(context.Background(), f.inv, CreateCommand{ExamID: f.examID, ExpectedDraftRevision: 1, DisplayName: "Reference", MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Empty(t, f.publisher.Calls(), "replay effects")
}

func TestExactCreateRetryRecoversUnknownCommitWithoutRepublishing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.persistence.replayAfterFirst = true
	command := CreateCommand{ExamID: f.examID, ExpectedDraftRevision: 1, DisplayName: "Reference", MediaType: model.ExamResourceMediaText, Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"}
	command.Body = strings.NewReader("notes")
	first, err := f.service.CreateExamResource(context.Background(), f.inv, command)
	require.NoError(t, err)
	// Simulate a lost success response: the caller retries the exact command
	// with the original expected Draft revision and a fresh request body.
	command.Body = strings.NewReader("notes")
	recovered, err := f.service.CreateExamResource(context.Background(), f.inv, command)
	require.NoError(t, err)
	require.Equal(t, first.Resource.ID, recovered.Resource.ID, "first=%#v recovered=%#v reserve=%d finalize=%d effects", first, recovered, f.persistence.reserveCalls, f.persistence.finalizeCalls)
	require.Equal(t, 2, f.persistence.reserveCalls, "first=%#v recovered=%#v reserve=%d finalize=%d effects", first, recovered, f.persistence.reserveCalls, f.persistence.finalizeCalls)
	require.Equal(t, 2, f.persistence.finalizeCalls, "first=%#v recovered=%#v reserve=%d finalize=%d effects", first, recovered, f.persistence.reserveCalls, f.persistence.finalizeCalls)
	// Contract: the first commit publishes one Draft update and the replayed
	// retry publishes nothing.
	require.Equal(t, []realtimetest.Call{draftUpdated(t, f.examID, 2)}, f.publisher.Calls())
}

func TestExactTenthCreateRetryReachesStoredOutcome(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for position := 0; position < model.ExamResourceMaximumCount-1; position++ {
		record := testRecord(f.examID, model.NewExamResourceID(), model.NewFileEntryID(), model.NewFileRevisionID(), 1)
		record.Resource.Position = position
		f.persistence.items = append(f.persistence.items, record)
	}
	f.persistence.replayAfterFirst = true
	command := CreateCommand{ExamID: f.examID, ExpectedDraftRevision: 1, DisplayName: "Last reference", MediaType: model.ExamResourceMediaText, Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"}
	command.Body = strings.NewReader("notes")
	first, err := f.service.CreateExamResource(context.Background(), f.inv, command)
	require.NoError(t, err)
	f.persistence.items = append(f.persistence.items, first)
	command.Body = strings.NewReader("notes")
	recovered, err := f.service.CreateExamResource(context.Background(), f.inv, command)
	require.NoError(t, err)
	require.Equal(t, first.Resource.ID, recovered.Resource.ID, "first=%#v recovered=%#v reserve=%d finalize=%d effects", first, recovered, f.persistence.reserveCalls, f.persistence.finalizeCalls)
	require.Equal(t, 2, f.persistence.reserveCalls, "first=%#v recovered=%#v reserve=%d finalize=%d effects", first, recovered, f.persistence.reserveCalls, f.persistence.finalizeCalls)
	require.Equal(t, 2, f.persistence.finalizeCalls, "first=%#v recovered=%#v reserve=%d finalize=%d effects", first, recovered, f.persistence.reserveCalls, f.persistence.finalizeCalls)
	// Contract: the first commit publishes one Draft update and the replayed
	// retry publishes nothing.
	require.Equal(t, []realtimetest.Call{draftUpdated(t, f.examID, 2)}, f.publisher.Calls())
}

func TestExactReplacementRetryRecoversUnknownCommitWithoutRepublishing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	current := testRecord(f.examID, model.NewExamResourceID(), model.NewFileEntryID(), model.NewFileRevisionID(), 1)
	f.persistence.items = []store.ExamResourceRecord{current}
	f.persistence.replayAfterFirst = true
	command := ReplaceContentCommand{ExamID: f.examID, ResourceID: current.Resource.ID, ExpectedDraftRevision: 1, MediaType: model.ExamResourceMediaText, Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"}
	command.Body = strings.NewReader("notes")
	first, err := f.service.ReplaceExamResourceContent(context.Background(), f.inv, command)
	require.NoError(t, err)
	// Simulate the same lost-success response as Create: the reservation is
	// disposable and Finalize recovers the durable idempotent outcome.
	command.Body = strings.NewReader("notes")
	recovered, err := f.service.ReplaceExamResourceContent(context.Background(), f.inv, command)
	require.NoError(t, err)
	require.Equal(t, first.Resource.SelectedFileRevisionID, recovered.Resource.SelectedFileRevisionID, "first=%#v recovered=%#v reserve=%d finalize=%d effects", first, recovered, f.persistence.reserveCalls, f.persistence.finalizeCalls)
	require.Equal(t, 2, f.persistence.reserveCalls, "first=%#v recovered=%#v reserve=%d finalize=%d effects", first, recovered, f.persistence.reserveCalls, f.persistence.finalizeCalls)
	require.Equal(t, 2, f.persistence.finalizeCalls, "first=%#v recovered=%#v reserve=%d finalize=%d effects", first, recovered, f.persistence.reserveCalls, f.persistence.finalizeCalls)
	// Contract: the first commit publishes one Draft update and the replayed
	// retry publishes nothing.
	require.Equal(t, []realtimetest.Call{draftUpdated(t, f.examID, 2)}, f.publisher.Calls())
	wantIdempotency, prepareErr := prepareResourceIdempotency(f.inv, idempotencyOperationReplaceResourceContent, "test-key",
		f.examID, 1, current.Resource.ID.String(), "", "", model.ExamResourceMediaText, 5, strings.Repeat("a", 64), nil)
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.persistence.idempotency)
}

func TestCreateMapsStorageFailureToUnavailableWithoutSafeBackendDetails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.content.storeErr = errors.New("secret backend bucket and object key")
	_, err := f.service.CreateExamResource(context.Background(), f.inv, CreateCommand{ExamID: f.examID, ExpectedDraftRevision: 1, DisplayName: "Reference", MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v fault=%#v", err, fault)
	require.Equal(t, "exam.resource.unavailable", fault.Code(), "error=%v fault=%#v", err, fault)
	require.Empty(t, fault.Fields(), "error=%v fault=%#v", err, fault)
}

func TestInvalidMetadataIsRejectedBeforeUploadReservation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.service.CreateExamResource(context.Background(), f.inv, CreateCommand{ExamID: f.examID, ExpectedDraftRevision: 1, DisplayName: "   ", MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.resource.invalid", fault.Code())
	for _, event := range f.order {
		require.NotEqual(t, "reserve", event, "invalid metadata caused %q: order=%v", event, f.order)
		require.NotEqual(t, "content.store", event, "invalid metadata caused %q: order=%v", event, f.order)
		require.NotEqual(t, "audit.begin", event, "invalid metadata caused %q: order=%v", event, f.order)
	}
}

func TestCreateUsesExplicitOverrideWhenCurrentManagerMembershipIsAbsent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.memberships.items = []*model.AcademicUnitMember{}
	_, err := f.service.CreateExamResource(context.Background(), f.inv, CreateCommand{ExamID: f.examID, ExpectedDraftRevision: 1, DisplayName: "Reference", MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, model.ActionExamManageOverride, f.authorizer.action, "action")
	require.NotNil(t, f.persistence.reservation, "action")
	require.True(t, f.persistence.reservation.ManagerOverride, "action")
}

func TestCreateStopsWhenCurrentManagerMembershipCannotBeRead(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	failure := errors.New("membership unavailable")
	f.memberships.err = failure
	_, err := f.service.CreateExamResource(context.Background(), f.inv, CreateCommand{ExamID: f.examID, ExpectedDraftRevision: 1, DisplayName: "Reference", MediaType: model.ExamResourceMediaText, Body: strings.NewReader("notes"), Size: 5, ExpectedSHA256: strings.Repeat("a", 64), IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.resource.unavailable", fault.Code())
	require.ErrorIs(t, err, failure)
	// Without current membership nothing is authorized or reserved.
	require.Empty(t, f.authorizer.action, "authorized without current membership")
	require.Nil(t, f.persistence.reservation, "reserved without current membership")
}
