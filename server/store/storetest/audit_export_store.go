// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AuditExportSQLProbe reaches what the AuditExportStore contract keeps
// private.
type AuditExportSQLProbe struct {
	SetPolicy     func(t *testing.T, ctx context.Context, days int)
	Expire        func(t *testing.T, ctx context.Context, id model.AuditExportID)
	ArtifactCount func(t *testing.T, ctx context.Context, id model.AuditExportID) int
}

func TestAuditExportStore(t *testing.T, ss store.Store, probe AuditExportSQLProbe) {
	ctx := context.Background()
	saveInstitution(t, ctx, ss)
	requester := saveUser(t, ctx, ss)
	exports := ss.AuditExport()
	root := model.NewAcademicUnitID()
	until := model.TimeUTC(time.Now().Add(-time.Minute))
	query := store.AuditListOptions{
		Actions: []string{string(model.ActionRoleManage)}, Status: model.AuditStatusSuccess, Ascending: true,
		Resource: &model.Resource{Type: model.ResourceUser, ID: requester.ID.String()},
		Since:    until.Add(-time.Hour), Until: until,
		Visibility: store.AuditVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{root}, AllowedActions: model.AcademicAuditActions()},
	}
	newCreation := func(t *testing.T, format model.AuditExportFormat) *store.AuditExportCreation {
		t.Helper()
		audit, err := ss.Audit().Save(ctx, &model.AuditEvent{
			ActorID: requester.ID, Action: string(model.ActionAcademicAuditView),
			Resource:  model.Resource{Type: model.ResourceAcademicUnit, ID: root.String()},
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: root.String(),
			Status: model.AuditStatusAttempt, NodeID: "test-node",
		})
		require.NoError(t, err)
		return &store.AuditExportCreation{
			Export: &model.AuditExport{
				ID: model.NewAuditExportID(), RequesterUserID: requester.ID, Format: format, Redacted: true,
				JobID: model.NewJobID(), EventCount: 3,
			},
			Query: query, AuditEventID: audit.ID, AuditAt: model.NowUTC(),
		}
	}
	claim := func(t *testing.T, export *model.AuditExport) store.AuditExportBuild {
		t.Helper()
		token, err := model.NewJobClaimToken()
		require.NoError(t, err)
		claimed, err := ss.Job().ClaimNext(ctx, &store.JobClaimRequest{
			Types: []model.JobType{model.JobTypeAuditExportBuild}, NodeID: "audit-export-test", ClaimToken: token, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.NotNil(t, claimed)
		require.Equal(t, export.JobID, claimed.Job.ID)
		return store.AuditExportBuild{
			AuditExportArtifact: store.AuditExportArtifact{ExportID: export.ID, AttemptID: claimed.Attempt.ID},
			JobID:               claimed.Job.ID, ClaimToken: token,
		}
	}

	input := newCreation(t, model.AuditExportCSV)
	command := examCommand(requester.ID, store.AuditExportCreateOperation, "audit-export", "audit-export")
	_, err := exports.Create(ctx, input, command)
	require.Truef(t, store.IsConflict(err), "unconfigured expiry accepted: %v", err)
	probe.SetPolicy(t, ctx, 1)

	created, err := exports.Create(ctx, input, command)
	require.NoError(t, err)
	require.Equal(t, model.ExamExportQueued, created.State)
	require.Equal(t, 24*time.Hour, created.ExpiresAt.Sub(created.CreatedAt))
	require.True(t, created.Redacted)
	buildJob, err := ss.Job().Get(ctx, input.Export.JobID)
	require.NoError(t, err)
	require.Equal(t, model.JobTypeAuditExportBuild, buildJob.Type)
	require.Equal(t, requester.ID, buildJob.CreatedByUserID)
	completed, err := ss.Audit().Get(ctx, input.AuditEventID)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusSuccess, completed.Status, "the request was not audited with the export")

	replayed, err := exports.Create(ctx, newCreation(t, model.AuditExportCSV), command)
	require.NoError(t, err)
	require.Equal(t, created.ID, replayed.ID, "a retry created a second export")
	require.Equal(t, created.ExpiresAt, replayed.ExpiresAt, "a retry extended the expiry")

	read, err := exports.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, query.Visibility, read.Visibility, "the frozen visibility changed")
	require.Zero(t, read.Artifact)

	build := claim(t, created)
	snapshot, err := exports.BeginBuild(ctx, &build)
	require.NoError(t, err)
	require.Equal(t, query, snapshot.Query, "the build read other filters than were frozen")
	_, err = exports.BeginBuild(ctx, &build)
	require.True(t, store.IsConflict(err), "one attempt began two builds")
	purge, err := exports.BeginPurgeBatch(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, purge, "a running writer's artifact was offered for removal")

	stale := build
	stale.ClaimToken, _ = model.NewJobClaimToken()
	err = exports.Publish(ctx, &store.AuditExportPublication{AuditExportBuild: stale, SizeBytes: 42, SHA256: strings.Repeat("a", 64), EventCount: 2})
	require.True(t, store.IsConflict(err), "a stale claim published")
	publication := &store.AuditExportPublication{AuditExportBuild: build, SizeBytes: 42, SHA256: strings.Repeat("a", 64), EventCount: 2}
	require.NoError(t, exports.Publish(ctx, publication))
	require.NoError(t, exports.Publish(ctx, publication), "an exact republication was refused")

	read, err = exports.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, model.ExamExportReady, read.Export.State)
	require.Equal(t, build.AuditExportArtifact, read.Artifact)
	require.Equal(t, 2, read.Export.EventCount, "the published count is the file's")
	require.Equal(t, query.Visibility, read.Visibility, "publication dropped the frozen visibility")
	purge, err = exports.BeginPurgeBatch(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, purge, "the published file was offered for removal")

	// Expiry hides the file at once and the sweep then removes it.
	probe.Expire(t, ctx, created.ID)
	read, err = exports.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, model.ExamExportExpired, read.Export.State)
	require.Zero(t, read.Artifact, "an expired export still names its file")
	reconciled, err := exports.Reconcile(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, reconciled)
	purge, err = exports.BeginPurgeBatch(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, []store.AuditExportArtifact{build.AuditExportArtifact}, purge)
	require.NoError(t, exports.CompletePurge(ctx, purge[0]))
	require.Equal(t, 0, probe.ArtifactCount(t, ctx, created.ID), "a removed file kept its reference")

	// A build that fails leaves its writer's key for cleanup until the writer
	// is known to have returned.
	failed := newCreation(t, model.AuditExportJSONL)
	failedExport, err := exports.Create(ctx, failed, examCommand(requester.ID, store.AuditExportCreateOperation, "audit-export-failed", "audit-export-failed"))
	require.NoError(t, err)
	failedBuild := claim(t, failedExport)
	_, err = exports.BeginBuild(ctx, &failedBuild)
	require.NoError(t, err)
	_, err = ss.Job().Complete(ctx, &store.JobCompletion{
		AttemptID: failedBuild.AttemptID, ClaimToken: failedBuild.ClaimToken,
		Kind: store.JobCompletionPermanentFailure, PublicErrorCode: "audit.export.unavailable",
	})
	require.NoError(t, err)
	reconciled, err = exports.Reconcile(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, reconciled)
	read, err = exports.Get(ctx, failedExport.ID)
	require.NoError(t, err)
	require.Equal(t, model.ExamExportFailed, read.Export.State)
	purge, err = exports.BeginPurgeBatch(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, []store.AuditExportArtifact{failedBuild.AuditExportArtifact}, purge)
	require.NoError(t, exports.CompletePurge(ctx, purge[0]))
	require.Equal(t, 1, probe.ArtifactCount(t, ctx, failedExport.ID), "an unfinished writer lost its cleanup reference")
	require.NoError(t, exports.FinishWriter(ctx, failedBuild.AuditExportArtifact))
	require.NoError(t, exports.CompletePurge(ctx, failedBuild.AuditExportArtifact))
	require.Equal(t, 0, probe.ArtifactCount(t, ctx, failedExport.ID), "a finished writer kept its cleanup reference")
}
