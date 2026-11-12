// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// testAcademicAdministrationFilterPreview proves the academic
// administration filter preview mode of the onboarding import aggregate: its
// filter round-trips, it is never listed or counted with the CSV imports,
// its frozen rows are bounded, distinct and on its own operation and scope,
// it becomes ready with a 15-minute PostgreSQL deadline, its rows are
// counted by class and reason and read only through the reader's
// visibility, and its first commit records no-op rows as no-ops and inserts
// the batch-level audit entry exactly once.
func testAcademicAdministrationFilterPreview(t *testing.T, ss store.Store) {
	t.Helper()
	ctx := context.Background()
	_, class, actor, _ := invitationAdministrationFixture(t, ctx, ss, "filter-preview")
	session, credentials, _ := saveSession(t, ctx, ss, actor.ID.String(), 10)
	principal := model.Principal{UserID: actor.ID, SessionID: session.ID, CredentialID: model.PrincipalCredentialID(credentials[0].ID),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		MFACompletedAt: model.OptionalTimeFrom(session.AuthenticatedAt), ClientType: session.ClientType, AuthenticatedAt: session.AuthenticatedAt}
	changing, unchanged := saveUser(t, ctx, ss), saveUser(t, ctx, ss)
	_, err := ss.Affiliation().Save(ctx, &model.Affiliation{UserID: unchanged.ID, Kind: model.AffiliationStudent, StartsAt: model.TimeFromMillis(1)})
	require.NoError(t, err)
	_, err = ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: class.ID, UserID: unchanged.ID, StartsAt: model.NowUTC().Add(-time.Hour)})
	require.NoError(t, err)

	filter := &store.AcademicAdministrationFilter{
		Operation:    "class.enroll",
		Query:        "student",
		Affiliations: []model.AffiliationKind{model.AffiliationStudent},
		StartAt:      model.OptionalTimeFrom(model.NowUTC().Add(time.Hour)),
	}
	create := func(t *testing.T, mutate func(*store.OnboardingImportCreation)) (*store.OnboardingImport, error) {
		t.Helper()
		at := model.NowUTC().Add(-time.Minute)
		id := model.NewOnboardingImportID()
		job, jobErr := model.NewJob(model.NewJobID(), model.JobTypeAcademicAdministrationPreviewResolve,
			json.RawMessage(`{"import_id":"`+id.String()+`"}`), "resolve:"+id.String(), at, at, 3)
		require.NoError(t, jobErr)
		audit := saveOnboardingImportAuditAttempt(t, ctx, ss, actor.ID, model.RoleScopeClass, class.ID.String(), "academic_administration_preview.create")
		creation := &store.OnboardingImportCreation{Import: &store.OnboardingImport{ID: id, Mode: model.OnboardingImportAcademicAdministrationFilter,
			State: model.OnboardingImportParsing, ScopeType: model.RoleScopeClass, ScopeID: class.ID.String(), ActorUserID: actor.ID,
			Principal: principal, ParseJobID: job.ID, CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 1,
			Filter: filter}, ParseJob: job, AuditEventID: audit.ID, AuditAt: model.NowUTC()}
		if mutate != nil {
			mutate(creation)
		}
		return ss.OnboardingImport().CreateOnboardingImport(ctx, creation)
	}
	row := func(id model.OnboardingImportID, number int, user model.UserID, code string) store.OnboardingImportRow {
		value := store.OnboardingImportRow{ImportID: id, RowNumber: number, Reference: user.String(), Operation: "class.enroll",
			ScopeType: model.RoleScopeClass, ScopeID: class.ID.String(), TargetRevision: class.Revision, UserID: user,
			StartsAt: filter.StartAt, PreviewStatus: model.OnboardingImportRowValid, Status: model.OnboardingImportRowValid}
		if code != "" {
			value.PreviewStatus, value.Status, value.PreviewCode = model.OnboardingImportRowInvalid, model.OnboardingImportRowInvalid, code
		}
		return value
	}
	requireInvalid := func(t *testing.T, err error) {
		t.Helper()
		var invalid *store.ErrInvalidInput
		require.ErrorAs(t, err, &invalid)
	}

	created, err := create(t, nil)
	require.NoError(t, err)
	t.Run("FilterRoundTrips", func(t *testing.T) {
		read, getErr := ss.OnboardingImport().GetOnboardingImport(ctx, created.ID)
		require.NoError(t, getErr)
		require.NotNil(t, read.Filter)
		require.Equal(t, *filter, *read.Filter)
		require.False(t, read.PreviewExpiresAt.Valid, "a parsing preview has no deadline")
	})
	t.Run("RefusesAMismatchedPackage", func(t *testing.T) {
		for name, mutate := range map[string]func(*store.OnboardingImportCreation){
			"no filter": func(creation *store.OnboardingImportCreation) { creation.Import.Filter = nil },
			"CSV mode with a filter": func(creation *store.OnboardingImportCreation) {
				creation.Import.Mode = model.OnboardingImportAcademicAdministration
			},
			"parse Job": func(creation *store.OnboardingImportCreation) {
				creation.ParseJob.Type = model.JobTypeOnboardingImportParse
			},
			"operation outside the scope": func(creation *store.OnboardingImportCreation) {
				creation.Import.Filter = &store.AcademicAdministrationFilter{Operation: "user.disable"}
			},
			"transfer": func(creation *store.OnboardingImportCreation) {
				creation.Import.Filter = &store.AcademicAdministrationFilter{Operation: "class.transfer"}
			},
			"field another operation owns": func(creation *store.OnboardingImportCreation) {
				creation.Import.Filter = &store.AcademicAdministrationFilter{Operation: "class.enroll", AffiliationKind: model.AffiliationStudent}
			},
			"file name": func(creation *store.OnboardingImportCreation) { creation.Import.FileName = "roster.csv" },
		} {
			t.Run(name, func(t *testing.T) {
				_, createErr := create(t, mutate)
				requireInvalid(t, createErr)
			})
		}
	})
	t.Run("IsNeverListedOrCountedAsAnImport", func(t *testing.T) {
		values, listErr := ss.OnboardingImport().ListOnboardingImports(ctx, store.OnboardingImportListOptions{
			Visibility: store.OnboardingImportVisibilityScope{InstitutionWide: true}, Limit: store.OnboardingImportListMaximum})
		require.NoError(t, listErr)
		require.False(t, slices.ContainsFunc(values, func(value *store.OnboardingImport) bool { return value.ID == created.ID }))
		before, countErr := ss.OnboardingImport().CountOpenOnboardingImports(ctx, store.OnboardingImportVisibilityScope{ClassIDs: []model.ClassID{class.ID}}, store.MaximumListCount)
		require.NoError(t, countErr)
		_, err = create(t, nil)
		require.NoError(t, err)
		after, countErr := ss.OnboardingImport().CountOpenOnboardingImports(ctx, store.OnboardingImportVisibilityScope{ClassIDs: []model.ClassID{class.ID}}, store.MaximumListCount)
		require.NoError(t, countErr)
		require.Equal(t, before, after)
	})
	t.Run("RefusesRowsOutsideThePreview", func(t *testing.T) {
		pending, createErr := create(t, nil)
		require.NoError(t, createErr)
		otherOperation := row(pending.ID, 1, changing.ID, "")
		otherOperation.Operation = "class.end"
		otherScope := row(pending.ID, 1, changing.ID, "")
		otherScope.ScopeID = model.NewClassID().String()
		unreasoned := row(pending.ID, 1, changing.ID, "")
		unreasoned.PreviewStatus, unreasoned.Status = model.OnboardingImportRowInvalid, model.OnboardingImportRowInvalid
		tooMany := make([]store.OnboardingImportRow, store.AcademicAdministrationPreviewMaximumRows+1)
		for index := range tooMany {
			tooMany[index] = row(pending.ID, index+1, model.NewUserID(), "")
		}
		for name, rows := range map[string][]store.OnboardingImportRow{
			"another operation":             {otherOperation},
			"another scope":                 {otherScope},
			"a held row without its reason": {unreasoned},
			"a repeated User":               {row(pending.ID, 1, changing.ID, ""), row(pending.ID, 2, changing.ID, "")},
			"more than the bound":           tooMany,
		} {
			t.Run(name, func(t *testing.T) {
				_, completeErr := ss.OnboardingImport().CompleteOnboardingImportPreview(ctx, &store.OnboardingImportPreviewCompletion{
					ID: pending.ID, ExpectedRevision: pending.Revision, Digest: strings.Repeat("b", sha256.Size*2), Rows: rows, At: model.NowUTC()})
				requireInvalid(t, completeErr)
			})
		}
	})

	digest := strings.Repeat("c", sha256.Size*2)
	before := model.NowUTC()
	ready, err := ss.OnboardingImport().CompleteOnboardingImportPreview(ctx, &store.OnboardingImportPreviewCompletion{
		ID: created.ID, ExpectedRevision: created.Revision, Digest: digest, At: model.NowUTC(), Rows: []store.OnboardingImportRow{
			row(created.ID, 1, actor.ID, "academic_administration_preview.self"),
			row(created.ID, 2, changing.ID, ""),
			row(created.ID, 3, unchanged.ID, store.AcademicAdministrationPreviewNoChange),
		}})
	require.NoError(t, err)
	t.Run("BecomesReadyWithAFifteenMinuteDeadline", func(t *testing.T) {
		require.Equal(t, model.OnboardingImportPreviewReady, ready.State)
		require.True(t, ready.PreviewExpiresAt.Valid)
		require.WithinDuration(t, before.Add(store.AcademicAdministrationPreviewLifetime), ready.PreviewExpiresAt.Time, time.Minute)
		require.Equal(t, 1, ready.ValidRows)
		require.Equal(t, 2, ready.InvalidRows)
	})
	t.Run("CountsRowsByClassAndReason", func(t *testing.T) {
		groups, countErr := ss.OnboardingImport().CountAcademicAdministrationPreviewGroups(ctx,
			store.AcademicAdministrationPreviewGroupCountOptions{ImportID: created.ID})
		require.NoError(t, countErr)
		require.Equal(t, []store.AcademicAdministrationPreviewGroup{
			{Class: store.AcademicAdministrationPreviewNoOp, Rows: 1},
			{Class: store.AcademicAdministrationPreviewSkipped, Reason: "academic_administration_preview.self", Rows: 1},
			{Class: store.AcademicAdministrationPreviewWillChange, Rows: 1},
		}, groups)
	})
	t.Run("ReadsRowsOnlyThroughTheReadersVisibility", func(t *testing.T) {
		read := func(class store.AcademicAdministrationPreviewClass, reason string, visibility store.UserVisibilityScope) []model.UserID {
			t.Helper()
			rows, listErr := ss.OnboardingImport().ListAcademicAdministrationPreviewRows(ctx, store.AcademicAdministrationPreviewRowListOptions{
				ImportID: created.ID, Class: class, Reason: reason, Limit: 10, Visibility: visibility})
			require.NoError(t, listErr)
			users := []model.UserID{}
			for _, value := range rows {
				require.Equal(t, class, value.Class)
				require.NotEmpty(t, value.Username)
				users = append(users, value.UserID)
			}
			return users
		}
		institution := store.UserVisibilityScope{InstitutionWide: true}
		require.Equal(t, []model.UserID{changing.ID}, read(store.AcademicAdministrationPreviewWillChange, "", institution))
		require.Equal(t, []model.UserID{actor.ID}, read(store.AcademicAdministrationPreviewSkipped, "academic_administration_preview.self", institution))
		require.Empty(t, read(store.AcademicAdministrationPreviewSkipped, "academic_administration_preview.system_administrator", institution))
		roster := store.UserVisibilityScope{ClassIDs: []model.ClassID{class.ID}, ActiveAt: model.NowUTC()}
		require.Equal(t, []model.UserID{unchanged.ID}, read(store.AcademicAdministrationPreviewNoOp, "", roster),
			"a roster viewer sees the Class member")
		require.Empty(t, read(store.AcademicAdministrationPreviewWillChange, "", roster), "a roster viewer never sees a User outside the roster")
		require.Empty(t, read(store.AcademicAdministrationPreviewNoOp, "", store.UserVisibilityScope{}), "an empty visibility sees no row")
		groups, countErr := ss.OnboardingImport().CountAcademicAdministrationPreviewGroups(ctx,
			store.AcademicAdministrationPreviewGroupCountOptions{ImportID: created.ID, Visibility: &roster})
		require.NoError(t, countErr)
		require.Equal(t, []store.AcademicAdministrationPreviewGroup{{Class: store.AcademicAdministrationPreviewNoOp, Rows: 1}}, groups,
			"a roster viewer's counts hold only the rows it sees")
		empty := store.UserVisibilityScope{}
		groups, countErr = ss.OnboardingImport().CountAcademicAdministrationPreviewGroups(ctx,
			store.AcademicAdministrationPreviewGroupCountOptions{ImportID: created.ID, Visibility: &empty})
		require.NoError(t, countErr)
		require.Empty(t, groups, "an empty visibility counts no row")
		_, listErr := ss.OnboardingImport().ListAcademicAdministrationPreviewRows(ctx, store.AcademicAdministrationPreviewRowListOptions{
			ImportID: created.ID, Class: store.AcademicAdministrationPreviewNoOp, Reason: "academic_administration_preview.self", Limit: 10,
			Visibility: institution})
		requireInvalid(t, listErr)
	})

	t.Run("CommitRecordsNoOpsAndTheBatchEntryOnce", func(t *testing.T) {
		at := model.NowUTC()
		job, jobErr := model.NewJob(model.NewJobID(), model.JobTypeOnboardingImportExecute, json.RawMessage(`{"import_id":"`+created.ID.String()+`"}`),
			"execute:"+created.ID.String(), at, at, 3)
		require.NoError(t, jobErr)
		marker := "batch-" + model.NewID()
		draft := func() *model.AuditEvent {
			parameters, encodeErr := model.EncodeAuditData(map[string]any{"operation": "academic_administration_preview.batch", "marker": marker})
			require.NoError(t, encodeErr)
			return &model.AuditEvent{ActorID: actor.ID, Action: string(model.ActionOnboardingBatchManage),
				Resource: model.Resource{Type: model.ResourceClass, ID: class.ID.String()}, ScopeType: model.RoleScopeClass, ScopeID: class.ID.String(),
				Status: model.AuditStatusAttempt, NodeID: "filter-preview-store-test", Parameters: parameters}
		}
		commit := func(batch *model.AuditEvent) (*store.OnboardingImport, error) {
			audit := saveOnboardingImportAuditAttempt(t, ctx, ss, actor.ID, model.RoleScopeClass, class.ID.String(), "academic_administration_preview.commit")
			return ss.OnboardingImport().CommitOnboardingImport(ctx, &store.OnboardingImportCommit{ID: created.ID, ActorUserID: actor.ID, Principal: principal,
				ExpectedRevision: ready.Revision, PreviewDigest: digest, Policy: model.OnboardingImportValidRowsOnly,
				IdempotencyKey: sha256.Sum256([]byte("filter-commit")), ExecutionJob: job, At: at, AuditEventID: audit.ID, AuditAt: at,
				BatchAuditEvent: batch})
		}
		_, commitErr := commit(nil)
		requireInvalid(t, commitErr)

		executing, commitErr := commit(draft())
		require.NoError(t, commitErr)
		require.Equal(t, model.OnboardingImportExecuting, executing.State)
		require.Equal(t, 1, executing.NoOpRows)
		require.Equal(t, 1, executing.SkippedRows)
		replayed, commitErr := commit(draft())
		require.NoError(t, commitErr)
		require.Equal(t, executing.ExecutionJobID, replayed.ExecutionJobID)
		require.Equal(t, executing.NoOpRows, replayed.NoOpRows)
		require.Equal(t, executing.SkippedRows, replayed.SkippedRows)

		page, rowsErr := ss.OnboardingImport().ListOnboardingImportRows(ctx, created.ID, 0, 10)
		require.NoError(t, rowsErr)
		statuses := []model.OnboardingImportRowStatus{}
		for _, value := range page.Rows {
			statuses = append(statuses, value.Status)
		}
		require.Equal(t, []model.OnboardingImportRowStatus{model.OnboardingImportRowSkipped, model.OnboardingImportRowPending, model.OnboardingImportRowNoOp}, statuses)

		events, auditErr := ss.Audit().List(ctx, store.AuditListOptions{Actions: []string{string(model.ActionOnboardingBatchManage)}, ActorID: actor.ID,
			Resource: &model.Resource{Type: model.ResourceClass, ID: class.ID.String()}, Limit: 100, Visibility: store.AuditVisibilityScope{InstitutionWide: true}})
		require.NoError(t, auditErr)
		batches := 0
		for _, event := range events {
			if strings.Contains(string(event.Parameters), marker) {
				batches++
				require.Equal(t, model.AuditStatusSuccess, event.Status)
			}
		}
		require.Equal(t, 1, batches, "the batch entry is inserted by the first commit only")
	})
}
