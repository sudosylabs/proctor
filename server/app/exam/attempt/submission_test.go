// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSubmitSealsExactAcknowledgedStateBeforePublishingEffects(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	credential := model.NewCredentialToken()
	participationID := model.NewAttemptParticipationID()
	workspaceID := model.NewExamAttemptWorkspaceID()
	revisionID := model.NewExamRevisionID()
	submissionID, receiptCode := model.NewSubmissionID(), model.NewSubmissionReceiptCode()
	f.submissionID, f.receiptCode = submissionID, receiptCode
	digest := strings.Repeat("d", 64)
	f.submissions.target = &store.ExamSubmissionSealTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, WorkspaceID: workspaceID, CurrentRevisionID: revisionID}
	f.submissions.target.SealAt = f.at
	f.submissions.sealResult = &store.ExamSubmissionSealResult{Receipt: store.ExamSubmissionReceipt{BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1},
		SubmissionID: submissionID, ReceiptCode: receiptCode, AttemptID: f.attemptID, ExamRevisionID: revisionID, State: model.ExamAttemptSubmitted,
		WorkspaceCursor: 11, ManifestDigest: digest, SubmittedAt: f.at}, ExamID: f.sitting.ExamID,
		SittingID: f.sitting.ID, ClassID: f.sitting.ClassID, CandidateUserID: f.userID,
		ParticipationID: participationID, Generation: 3, ConnectionID: f.connectionID}

	result, err := f.service.SubmitExamAttempt(context.Background(), f.inv, SubmitCommand{
		Access: WorkspaceMutationAccess{CandidateAccess: CandidateAccess{AttemptID: f.attemptID,
			ConnectionID: f.connectionID, ContinuityCredential: credential}, ParticipationID: participationID, Generation: 3},
		ExpectedCurrentRevisionID: revisionID, ExpectedWorkspaceCursor: 11, FinalFocusLossSequence: 7,
		IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.Equal(t, []model.ExamAttemptID{f.attemptID}, f.settler.settled, "the environment settles before the seal")
	principal := f.inv.Principal()
	sealAccess := f.submissions.resolvedAccess
	require.Equal(t, f.attemptID, sealAccess.AttemptID, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.Equal(t, participationID, sealAccess.ParticipationID, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.Equal(t, int64(3), sealAccess.Generation, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.Equal(t, f.connectionID, sealAccess.ConnectionID, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.Equal(t, principal.UserID, sealAccess.CandidateUserID, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.Equal(t, principal.SessionID, sealAccess.SessionID, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.Equal(t, model.HashToken(credential), sealAccess.ContinuityCredentialHash, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.Equal(t, revisionID, sealAccess.ExpectedCurrentRevisionID, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.Equal(t, int64(11), sealAccess.ExpectedWorkspaceCursor, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.Equal(t, int64(7), sealAccess.FinalFocusLossSequence, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.NotNil(t, f.submissions.seal, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.True(t, f.submissions.seal.SubmissionID.IsValid(), "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.True(t, f.submissions.seal.AuditEventID.IsValid(), "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.True(t, f.submissions.seal.DomainEventID.IsValid(), "the proposed domain event reaches the seal")
	require.True(t, f.submissions.seal.DomainEventJobID.IsValid(), "the proposed dispatch Job reaches the seal")
	require.Equal(t, submissionID, result.SubmissionID, "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	require.Equal(t, receiptCode, f.submissions.seal.ReceiptCode, "the proposed receipt code reaches the seal")
	require.Equal(t, receiptCode, result.ReceiptCode, "the sealed receipt code reaches the candidate")
	require.Equal(t, receiptCode, f.mail.receipt().ReceiptCode, "the receipt mail carries the proposed code")
	require.Equal(t, submittedCalls(t, sealedSubmission(*f.submissions.sealResult, model.ExamSubmissionCandidateSubmitted), false), f.publisher.Calls(), "access=%#v seal=%#v result=%#v effects=%#v", sealAccess, f.submissions.seal, result, f.publisher.Calls())
	audit := fmt.Sprintf("%#v", f.audit().values)
	for _, private := range []string{credential, model.HashToken(credential), participationID.String()} {
		require.NotContains(t, audit, private)
	}
	require.Len(t, f.audit().values, 1)
	require.Equal(t, f.attemptID.String(), f.audit().values["exam_attempt_id"])
	wantIdempotency, prepareErr := prepareSubmissionIdempotency(f.inv, "test-key", f.attemptID, revisionID, 11, 7)
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.submissions.idempotency)
	require.Equal(t, f.userID, f.mail.receipt().CandidateUserID, "mail request=%#v seal=%#v", f.mail.receipt(), f.submissions.seal)
	require.Equal(t, f.sitting.ExamID, f.mail.receipt().ExamID, "mail request=%#v seal=%#v", f.mail.receipt(), f.submissions.seal)
	require.Equal(t, f.sitting.ID, f.mail.receipt().SittingID, "mail request=%#v seal=%#v", f.mail.receipt(), f.submissions.seal)
	require.Equal(t, submissionID, f.mail.receipt().SubmissionID, "mail request=%#v seal=%#v", f.mail.receipt(), f.submissions.seal)
	require.True(t, f.mail.receipt().SealedAt.Equal(f.at), "mail request=%#v seal=%#v", f.mail.receipt(), f.submissions.seal)
	require.Equal(t, model.ExamSubmissionCandidateSubmitted, f.mail.receipt().Provenance, "mail request=%#v seal=%#v", f.mail.receipt(), f.submissions.seal)
	require.NotNil(t, f.submissions.seal.Notice, "mail request=%#v seal=%#v", f.mail.receipt(), f.submissions.seal)
	require.Equal(t, int64(2), f.submissions.seal.ExpectedRecipientRevision, "mail request=%#v seal=%#v", f.mail.receipt(), f.submissions.seal)
	require.True(t, model.TimeUTC(f.at).Equal(f.submissions.seal.AuditAt), "mail request=%#v seal=%#v", f.mail.receipt(), f.submissions.seal)
	// Contract (guardrails 8 and 14): the audit begins before the durable write,
	// and the write commits before its effect.
	require.Equal(t, []string{"submission.resolve", "audit-begin:exam.sitting.participate", "submission.mail", "submission.seal", "effects"}, f.order)
}

func TestSubmitReplayReturnsRetainedReceiptAndSuppressesEffects(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	proposedID, retainedID := model.NewSubmissionID(), model.NewSubmissionID()
	proposedCode, retainedCode := model.NewSubmissionReceiptCode(), model.NewSubmissionReceiptCode()
	revisionID := model.NewExamRevisionID()
	f.submissionID, f.receiptCode = proposedID, proposedCode
	mutationAccess := validWorkspaceMutationAccess(f)
	f.submissions.target = &store.ExamSubmissionSealTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, WorkspaceID: model.NewExamAttemptWorkspaceID(),
		CurrentRevisionID: revisionID, Replayed: true, SealAt: f.at}
	f.submissions.sealResult = &store.ExamSubmissionSealResult{Receipt: store.ExamSubmissionReceipt{BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1}, SubmissionID: retainedID, ReceiptCode: retainedCode,
		AttemptID: f.attemptID, ExamRevisionID: revisionID, State: model.ExamAttemptSubmitted, WorkspaceCursor: 0,
		ManifestDigest: strings.Repeat("e", 64), SubmittedAt: f.at}, ExamID: f.sitting.ExamID,
		SittingID: f.sitting.ID, ClassID: f.sitting.ClassID, CandidateUserID: f.userID,
		ParticipationID: mutationAccess.ParticipationID, Generation: mutationAccess.Generation, ConnectionID: mutationAccess.ConnectionID, Replayed: true}

	result, err := f.service.SubmitExamAttempt(context.Background(), f.inv, SubmitCommand{Access: mutationAccess,
		ExpectedCurrentRevisionID: revisionID, ExpectedWorkspaceCursor: 0, FinalFocusLossSequence: 0,
		IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, retainedID, result.SubmissionID, "result=%#v seal=%#v effects=%#v", result, f.submissions.seal, f.publisher.Calls())
	require.Equal(t, proposedID, f.submissions.seal.SubmissionID, "result=%#v seal=%#v effects=%#v", result, f.submissions.seal, f.publisher.Calls())
	require.Equal(t, retainedCode, result.ReceiptCode, "an exact replay returned a newly minted receipt code")
	require.Equal(t, proposedCode, f.submissions.seal.ReceiptCode, "result=%#v seal=%#v", result, f.submissions.seal)
	require.Empty(t, f.publisher.Calls(), "result=%#v seal=%#v effects=%#v", result, f.submissions.seal, f.publisher.Calls())
	require.Nil(t, f.submissions.seal.Notice, "result=%#v seal=%#v effects=%#v", result, f.submissions.seal, f.publisher.Calls())
	require.Equal(t, int64(0), f.submissions.seal.ExpectedRecipientRevision, "result=%#v seal=%#v effects=%#v", result, f.submissions.seal, f.publisher.Calls())
	require.False(t, f.mail.receipt().SubmissionID.IsValid(), "result=%#v seal=%#v effects=%#v", result, f.submissions.seal, f.publisher.Calls())
	require.Empty(t, f.settler.settled, "a replay settles nothing")
}

func TestManagedSubmissionNestedOwnershipMismatchIsConcealed(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// Submissions are read by the Exam's Manager.
	useManagerCall(f)
	submissionID := model.NewSubmissionID()
	f.submissions.authorization = &store.ExamSubmissionAuthorization{SubmissionID: submissionID,
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, CandidateUserID: model.NewUserID(), AcademicUnitID: f.unitID}
	_, err := f.service.GetExamSubmission(context.Background(), f.inv, GetSubmissionQuery{ExamID: model.NewExamID(),
		SittingID: f.sitting.ID, AttemptID: f.attemptID, SubmissionID: submissionID})
	require.True(t, apperror.Is(err, "resource.not_found"), "error=%v order=%v", err, f.order)
	require.True(t, chainHasCode(err, "exam.attempt.not_found"), "error=%v order=%v", err, f.order)
	// A concealed ownership mismatch never reads the Submission.
	require.Equal(t, []string{"submission.resolve_owner", "grants", "decision:" + string(model.ActionSubmissionView) + ":success", "submission.resolve_owner"}, f.order)
}

func TestOpenSubmissionFileAuthorizesThenStreamsRetainedStarterBytes(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// Submissions are read by the Exam's Manager.
	useManagerCall(f)
	submission := submissionFixture(t, f, 5)
	entryID, version, starterID := model.NewAttemptWorkspaceEntryID(), model.NewWorkspaceContentVersion(), model.NewStarterWorkspaceObjectID()
	f.submissions.authorization = &store.ExamSubmissionAuthorization{SubmissionID: submission.ID,
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, CandidateUserID: model.NewUserID(), AcademicUnitID: f.unitID}
	f.submissions.file = &store.ExamSubmissionFileSelector{Entry: store.ExamSubmissionManifestItem{EntryID: entryID,
		Kind: model.StarterWorkspaceEntryFile, Path: "main.go", ContentVersion: version, MediaType: "text/x-go",
		SizeBytes: 4, SHA256: strings.Repeat("a", 64)}, StorageOrigin: model.AttemptWorkspaceStorageStarter,
		StarterObjectID: starterID, ContentVersion: version}

	opened, err := f.service.OpenExamSubmissionFile(context.Background(), f.inv, OpenSubmissionFileQuery{
		GetSubmissionQuery: GetSubmissionQuery{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
			AttemptID: f.attemptID, SubmissionID: submission.ID}, EntryID: entryID})
	require.NoError(t, err)
	defer opened.Body.Close()
	body, _ := io.ReadAll(opened.Body)
	require.Equal(t, "workspace", string(body), "opened=%#v starter=%s attempt=%s body", opened, f.content.starterID, f.content.openAttemptID)
	require.Equal(t, version, opened.ContentVersion, "opened=%#v starter=%s attempt=%s body", opened, f.content.starterID, f.content.openAttemptID)
	require.Equal(t, strings.Repeat("a", 64), opened.SHA256, "opened=%#v starter=%s attempt=%s body", opened, f.content.starterID, f.content.openAttemptID)
	require.Equal(t, starterID, f.content.starterID, "opened=%#v starter=%s attempt=%s body", opened, f.content.starterID, f.content.openAttemptID)
	require.False(t, f.content.openAttemptID.IsValid(), "opened=%#v starter=%s attempt=%s body", opened, f.content.starterID, f.content.openAttemptID)
}

func TestOpenSubmissionFileStreamsRetainedAttemptOriginBytes(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// Submissions are read by the Exam's Manager.
	useManagerCall(f)
	submission := submissionFixture(t, f, 5)
	entryID, version, objectID := model.NewAttemptWorkspaceEntryID(), model.NewWorkspaceContentVersion(), model.NewAttemptWorkspaceObjectID()
	f.submissions.authorization = &store.ExamSubmissionAuthorization{SubmissionID: submission.ID,
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, CandidateUserID: model.NewUserID(), AcademicUnitID: f.unitID}
	f.submissions.file = &store.ExamSubmissionFileSelector{Entry: store.ExamSubmissionManifestItem{EntryID: entryID,
		Kind: model.StarterWorkspaceEntryFile, Path: "answer.txt", ContentVersion: version, MediaType: "text/plain",
		SizeBytes: 4, SHA256: strings.Repeat("b", 64)}, StorageOrigin: model.AttemptWorkspaceStorageAttempt,
		AttemptObjectID: objectID, ContentVersion: version}
	f.content.openAttemptBody = "work"

	opened, err := f.service.OpenExamSubmissionFile(context.Background(), f.inv, OpenSubmissionFileQuery{
		GetSubmissionQuery: GetSubmissionQuery{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
			AttemptID: f.attemptID, SubmissionID: submission.ID}, EntryID: entryID})
	require.NoError(t, err)
	defer opened.Body.Close()
	body, _ := io.ReadAll(opened.Body)
	require.Equal(t, "work", string(body), "opened=%#v starter=%s attempt=%s body", opened, f.content.starterID, f.content.openAttemptID)
	require.Equal(t, version, opened.ContentVersion, "opened=%#v starter=%s attempt=%s body", opened, f.content.starterID, f.content.openAttemptID)
	require.Equal(t, strings.Repeat("b", 64), opened.SHA256, "opened=%#v starter=%s attempt=%s body", opened, f.content.starterID, f.content.openAttemptID)
	require.Equal(t, objectID, f.content.openAttemptID, "opened=%#v starter=%s attempt=%s body", opened, f.content.starterID, f.content.openAttemptID)
	require.False(t, f.content.starterID.IsValid(), "opened=%#v starter=%s attempt=%s body", opened, f.content.starterID, f.content.openAttemptID)
}

func TestOpenSubmissionFileFailsClosedOnRetainedSelectorMismatch(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// Submissions are read by the Exam's Manager.
	useManagerCall(f)
	submission := submissionFixture(t, f, 5)
	entryID := model.NewAttemptWorkspaceEntryID()
	f.submissions.authorization = &store.ExamSubmissionAuthorization{SubmissionID: submission.ID,
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, CandidateUserID: model.NewUserID(), AcademicUnitID: f.unitID}
	f.submissions.file = &store.ExamSubmissionFileSelector{Entry: store.ExamSubmissionManifestItem{
		EntryID: model.NewAttemptWorkspaceEntryID(), Kind: model.StarterWorkspaceEntryFile, Path: "answer.txt",
		ContentVersion: model.NewWorkspaceContentVersion(), MediaType: "text/plain", SizeBytes: 4,
		SHA256: strings.Repeat("b", 64)}, StorageOrigin: model.AttemptWorkspaceStorageAttempt,
		AttemptObjectID: model.NewAttemptWorkspaceObjectID(), ContentVersion: model.NewWorkspaceContentVersion()}

	_, err := f.service.OpenExamSubmissionFile(context.Background(), f.inv, OpenSubmissionFileQuery{
		GetSubmissionQuery: GetSubmissionQuery{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
			AttemptID: f.attemptID, SubmissionID: submission.ID}, EntryID: entryID})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v starter=%s attempt=%s", err, f.content.starterID, f.content.openAttemptID)
	require.Equal(t, "exam.attempt.unavailable", fault.Code(), "error=%v starter=%s attempt=%s", err, f.content.starterID, f.content.openAttemptID)
	require.False(t, f.content.openAttemptID.IsValid(), "error=%v starter=%s attempt=%s", err, f.content.starterID, f.content.openAttemptID)
	require.False(t, f.content.starterID.IsValid(), "error=%v starter=%s attempt=%s", err, f.content.starterID, f.content.openAttemptID)
}

func TestListSubmissionManifestReturnsBoundedEntryIDKeysetAfterAuthorization(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// Submissions are read by the Exam's Manager.
	useManagerCall(f)
	submission := submissionFixture(t, f, 5)
	f.submissions.authorization = &store.ExamSubmissionAuthorization{SubmissionID: submission.ID,
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, CandidateUserID: model.NewUserID(), AcademicUnitID: f.unitID}
	ids := []model.AttemptWorkspaceEntryID{model.NewAttemptWorkspaceEntryID(), model.NewAttemptWorkspaceEntryID(), model.NewAttemptWorkspaceEntryID()}
	slices.SortFunc(ids, func(left, right model.AttemptWorkspaceEntryID) int {
		return strings.Compare(left.String(), right.String())
	})
	f.submissions.manifest = &store.ExamSubmissionManifestPage{SubmissionID: submission.ID, WorkspaceCursor: 5,
		ManifestDigest: submission.ManifestDigest, Items: []store.ExamSubmissionManifestItem{
			{EntryID: ids[1], Kind: model.StarterWorkspaceEntryDirectory, Path: "src"},
			{EntryID: ids[2], Kind: model.StarterWorkspaceEntryFile, Path: "src/main.go",
				ContentVersion: model.NewWorkspaceContentVersion(), MediaType: "text/x-go", SizeBytes: 4, SHA256: strings.Repeat("a", 64)},
		}}

	page, err := f.service.ListExamSubmissionManifest(context.Background(), f.inv, ListSubmissionManifestQuery{
		GetSubmissionQuery: GetSubmissionQuery{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
			AttemptID: f.attemptID, SubmissionID: submission.ID}, AfterEntryID: ids[0], Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, submission.ID, page.SubmissionID, "page=%#v options=%#v", page, f.submissions.manifestOptions)
	require.Len(t, page.Items, 2, "page=%#v options=%#v", page, f.submissions.manifestOptions)
	require.Equal(t, "src/main.go", page.Items[1].Path, "page=%#v options=%#v", page, f.submissions.manifestOptions)
	require.Equal(t, ids[0], f.submissions.manifestOptions.AfterEntryID, "page=%#v options=%#v", page, f.submissions.manifestOptions)
	require.Equal(t, 2, f.submissions.manifestOptions.Limit, "page=%#v options=%#v", page, f.submissions.manifestOptions)
}

func TestGetSubmissionAuthorizesCanonicalResourceBeforeProtectedRead(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// Submissions are read by the Exam's Manager.
	useManagerCall(f)
	submission := submissionFixture(t, f, 5)
	f.submissions.authorization = &store.ExamSubmissionAuthorization{SubmissionID: submission.ID,
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID, CandidateUserID: model.NewUserID(), AcademicUnitID: f.unitID}
	f.submissions.submission = submission

	got, err := f.service.GetExamSubmission(context.Background(), f.inv, GetSubmissionQuery{ExamID: f.sitting.ExamID,
		SittingID: f.sitting.ID, AttemptID: f.attemptID, SubmissionID: submission.ID})
	require.NoError(t, err)
	require.Equal(t, *f.submissions.authorization, got.Authorization, "got=%#v authorized", got)
	require.Equal(t, submission.ID, got.Submission.ID, "got=%#v authorized", got)
	decisions := f.control.Audits.Decisions()
	require.Equal(t, submission.ID.String(), decisions[len(decisions)-1].Resource.ID, "got=%#v authorized", got)
	// Contract: authorization precedes reading the Submission.
	require.Equal(t, []string{"submission.resolve_owner", "grants", "decision:" + string(model.ActionSubmissionView) + ":success", "submission.resolve_owner", "submission.get"}, f.order)
}

func submissionFixture(t *testing.T, f *fixture, cursor int64) *model.ExamSubmission {
	t.Helper()
	manifest, err := model.NewExamSubmissionManifest(cursor, nil)
	require.NoError(t, err)
	submission, err := model.NewExamSubmission(model.ExamSubmissionSpecification{ID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
		AttemptID: f.attemptID, ExamRevisionID: model.NewExamRevisionID(), WorkspaceID: model.NewExamAttemptWorkspaceID(), Manifest: manifest,
		BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1},
		Provenance:      model.ExamSubmissionCandidateSubmitted, SubmittedAt: f.at.Add(time.Minute)})
	require.NoError(t, err)
	return submission
}

type submissionStoreFake struct {
	f                  *fixture
	resolvedAccess     store.ExamSubmissionSealAccess
	target             *store.ExamSubmissionSealTarget
	seal               *store.ExamSubmissionSeal
	sealResult         *store.ExamSubmissionSealResult
	err                error
	authorization      *store.ExamSubmissionAuthorization
	submission         *model.ExamSubmission
	manifest           *store.ExamSubmissionManifestPage
	manifestOptions    store.ExamSubmissionManifestListOptions
	file               *store.ExamSubmissionFileSelector
	automaticTargets   []store.ExamSubmissionAutomaticSealTarget
	automaticOptions   store.ExamSubmissionAutomaticSealListOptions
	automaticInput     *store.ExamSubmissionAutomaticSeal
	automaticResult    *store.ExamSubmissionAutomaticSealResult
	managerRequest     store.ExamSubmissionManagerEndRequest
	managerPreparation *store.ExamSubmissionManagerEndPreparation
	managerInput       *store.ExamSubmissionManagerEnd
	managerResult      *store.ExamSubmissionManagerEndResult
	idempotency        *store.CommandIdempotency
}

// receiptMail is the production receipt preparation, SubmissionReceiptMail
// over the real mail Composer and renderer. Reading the candidate enters the
// call order as "submission.mail".
type receiptMail struct {
	f        *fixture
	preparer SubmissionReceiptMail
	sealer   *secretseal.Sealer
	// candidate, examID and sittingID are what the preparation read: the
	// candidate it addressed and the Exam and Sitting it read.
	candidate model.UserID
	examID    model.ExamID
	sittingID model.ExamSittingID
}

// preparedReceipt is what a frozen receipt says, in the terms of the request
// that produced it. It is zero when no receipt was prepared.
type preparedReceipt struct {
	CandidateUserID model.UserID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
	SubmissionID    model.SubmissionID
	ReceiptCode     model.SubmissionReceiptCode
	SealedAt        time.Time
	Provenance      model.ExamSubmissionProvenance
}

func newReceiptMail(t *testing.T, f *fixture) *receiptMail {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	composer, err := mail.NewComposer(mail.ComposerDependencies{Renderer: mailtest.NewRenderer(t),
		Sender: receiptSender{}, Sealer: sealer})
	require.NoError(t, err)
	m := &receiptMail{f: f, sealer: sealer}
	m.preparer = SubmissionReceiptMail{Composer: composer, Users: receiptUsers{m: m},
		Sittings: receiptSittings{m: m}, Revisions: receiptRevisions{}}
	return m
}

// receiptSealedAt finds the sealing time a receipt shows under whichever
// label its provenance uses.
var receiptSealedAt = regexp.MustCompile(`(?m)^(?:Submitted|Ended|Sealed): (.+)$`)

// receipt is what the receipt the Store received says, read back from the
// sealed message the candidate would get.
func (m *receiptMail) receipt() preparedReceipt {
	t := m.f.t
	var notice *store.PreparedMail
	switch submissions := m.f.submissions; {
	case submissions.seal != nil && submissions.seal.Notice != nil:
		notice = submissions.seal.Notice
	case submissions.automaticInput != nil && submissions.automaticInput.Notice != nil:
		notice = submissions.automaticInput.Notice
	case submissions.managerInput != nil && submissions.managerInput.Notice != nil:
		notice = submissions.managerInput.Notice
	default:
		return preparedReceipt{}
	}
	message, err := mail.OpenDelivery(m.sealer, notice.Delivery)
	require.NoError(t, err)
	submissionID, err := model.ParseSubmissionID(string(notice.Occurrence.ID))
	require.NoError(t, err, "a receipt occurs once per Submission")
	_, codeText, found := strings.Cut(message.Text, "Receipt code: ")
	require.True(t, found, "a receipt shows its code: %s", message.Text)
	code, err := model.ParseSubmissionReceiptCode(strings.SplitN(codeText, "\n", 2)[0])
	require.NoError(t, err)
	sealed := receiptSealedAt.FindStringSubmatch(message.Text)
	require.Len(t, sealed, 2, "a receipt shows when it was sealed: %s", message.Text)
	sealedAt, err := time.Parse("Monday 2 January 2006, 15:04:05 MST", sealed[1])
	require.NoError(t, err)
	provenance := map[model.MailTemplateKey]model.ExamSubmissionProvenance{
		model.MailTemplateExamSubmissionReceived:            model.ExamSubmissionCandidateSubmitted,
		model.MailTemplateExamSubmissionManagerEnded:        model.ExamSubmissionManagerEndedAttempt,
		model.MailTemplateExamSubmissionAutomaticallySealed: model.ExamSubmissionSittingClosed,
	}[notice.Delivery.TemplateKey]
	return preparedReceipt{CandidateUserID: notice.Delivery.TargetUserID, ExamID: m.examID, SittingID: m.sittingID,
		SubmissionID: submissionID, ReceiptCode: code, SealedAt: sealedAt, Provenance: provenance}
}

type receiptSender struct{}

func (receiptSender) Enabled() bool { return true }

func (receiptSender) From() mail.Address {
	return mail.Address{Name: "Proctor", Address: "no-reply@example.test"}
}

func (receiptSender) Send(context.Context, mail.Outbound) (mail.TransportOutcome, error) {
	return mail.TransportUnknown, nil
}

// receiptUsers reads the candidate at revision 2.
type receiptUsers struct {
	store.UserStore
	m *receiptMail
}

func (u receiptUsers) Get(_ context.Context, id model.UserID) (*model.User, error) {
	u.m.f.order = append(u.m.f.order, "submission.mail")
	u.m.candidate = id
	user := &model.User{Username: "candidate", Email: "candidate@example.edu", DisplayName: "Candidate",
		EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(id, time.UnixMilli(1))
	user.Revision = 2
	return user, nil
}

type receiptSittings struct {
	store.ExamSittingStore
	m *receiptMail
}

func (s receiptSittings) Get(_ context.Context, examID model.ExamID, sittingID model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	s.m.examID, s.m.sittingID = examID, sittingID
	return &store.ExamSittingSnapshot{Sitting: s.m.f.sitting}, nil
}

type receiptRevisions struct{ store.ExamRevisionStore }

func (receiptRevisions) GetSummary(context.Context, model.ExamID, model.ExamRevisionID) (*store.ExamRevisionSummary, error) {
	return &store.ExamRevisionSummary{Title: "Algorithms final"}, nil
}

func (f *submissionStoreFake) ListAutomaticSealTargets(_ context.Context, options store.ExamSubmissionAutomaticSealListOptions) ([]store.ExamSubmissionAutomaticSealTarget, error) {
	f.automaticOptions = options
	return f.automaticTargets, f.err
}

func (f *submissionStoreFake) PrepareAutomaticSeal(context.Context, store.ExamSubmissionAutomaticSealTarget) (*store.ExamSubmissionAutomaticSealPreparation, error) {
	return &store.ExamSubmissionAutomaticSealPreparation{Replayed: f.automaticResult != nil && f.automaticResult.Replayed,
		SealAt: f.f.at}, f.err
}

func (f *submissionStoreFake) PrepareManagerEnd(_ context.Context, request store.ExamSubmissionManagerEndRequest) (*store.ExamSubmissionManagerEndPreparation, error) {
	f.managerRequest = request
	return f.managerPreparation, f.err
}

func (f *submissionStoreFake) EndByManager(_ context.Context, input *store.ExamSubmissionManagerEnd,
	commandIdempotency *store.CommandIdempotency,
) (*store.ExamSubmissionManagerEndResult, error) {
	f.managerInput, f.idempotency = input, commandIdempotency
	return f.managerResult, f.err
}

func (f *submissionStoreFake) SealForSittingClose(_ context.Context, input *store.ExamSubmissionAutomaticSeal) (*store.ExamSubmissionAutomaticSealResult, error) {
	f.automaticInput = input
	return f.automaticResult, f.err
}

func (f *submissionStoreFake) ResolveSealTarget(_ context.Context, sealAccess store.ExamSubmissionSealAccess) (*store.ExamSubmissionSealTarget, error) {
	f.f.order = append(f.f.order, "submission.resolve")
	f.resolvedAccess = sealAccess
	return f.target, f.err
}

func (f *submissionStoreFake) Seal(_ context.Context, input *store.ExamSubmissionSeal, command *store.CommandIdempotency) (*store.ExamSubmissionSealResult, error) {
	f.f.order = append(f.f.order, "submission.seal")
	f.seal, f.idempotency = input, command
	return f.sealResult, f.err
}

func (f *submissionStoreFake) Resolve(context.Context, model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	f.f.order = append(f.f.order, "submission.resolve_owner")
	return f.authorization, f.err
}

func (f *submissionStoreFake) ResolveAttempt(context.Context, model.ExamAttemptID) (*store.ExamSubmissionAuthorization, error) {
	f.f.order = append(f.f.order, "submission.resolve_attempt")
	return f.authorization, f.err
}

func (f *submissionStoreFake) Get(context.Context, model.SubmissionID) (*model.ExamSubmission, error) {
	f.f.order = append(f.f.order, "submission.get")
	return f.submission, f.err
}

func (f *submissionStoreFake) ListManifest(_ context.Context, options store.ExamSubmissionManifestListOptions) (*store.ExamSubmissionManifestPage, error) {
	f.manifestOptions = options
	return f.manifest, f.err
}

func (f *submissionStoreFake) ResolveFile(context.Context, model.SubmissionID, model.AttemptWorkspaceEntryID) (*store.ExamSubmissionFileSelector, error) {
	f.f.order = append(f.f.order, "submission.resolve_file")
	return f.file, f.err
}
