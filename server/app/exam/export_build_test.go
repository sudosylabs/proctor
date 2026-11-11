// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type exportUncertainWriteError struct{}

func (*exportUncertainWriteError) Error() string { return "write uncertain" }

func (*exportUncertainWriteError) ExamExportWriteUncertain() {}

func TestExamExportBuildKeepsUnverifiedRemoteWriterReservation(t *testing.T) {
	_, service, s, _, content := newExportFixture(t)
	content.err = &exportUncertainWriteError{}
	input := store.ExamExportBuild{ExamExportArtifact: store.ExamExportArtifact{ExportID: s.value.ID, AttemptID: model.NewJobAttemptID()}, JobID: model.NewJobID()}
	input.ClaimToken, _ = model.NewJobClaimToken()
	require.Error(t, service.BuildFromJob(context.Background(), invocation.Job(input.JobID, input.AttemptID), input), "unverified remote write lost its uncertain cleanup reservation")
	require.Equal(t, 0, s.finished, "unverified remote write lost its uncertain cleanup reservation")
	require.Equal(t, 0, s.published, "unverified remote write lost its uncertain cleanup reservation")
	require.Equal(t, 0, content.purges, "unverified remote write lost its uncertain cleanup reservation")
}

func TestExamExportBuildKeepsUnknownPublicationAndFinishesWriterOutsideCancellation(t *testing.T) {
	for _, cancelDuringBuild := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown publication", true: "canceled copy"}[cancelDuringBuild], func(t *testing.T) {
			_, service, s, _, content := newExportFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelDuringBuild {
				content.cancel = cancel
				content.err = context.Canceled
			} else {
				s.publishErr = errors.New("commit acknowledgement lost")
			}
			input := store.ExamExportBuild{ExamExportArtifact: store.ExamExportArtifact{ExportID: s.value.ID, AttemptID: model.NewJobAttemptID()}, JobID: model.NewJobID()}
			input.ClaimToken, _ = model.NewJobClaimToken()
			require.Error(t, service.BuildFromJob(ctx, invocation.Job(input.JobID, input.AttemptID), input), "uncertain construction reported success")
			require.Equal(t, 1, s.finished, "cleanup/expiry: finished=%d canceled=%v purges=%d deadline=%v", s.finished, s.finishCanceled, content.purges, content.deadline)
			require.False(t, s.finishCanceled, "cleanup/expiry: finished=%d canceled=%v purges=%d deadline=%v", s.finished, s.finishCanceled, content.purges, content.deadline)
			require.Equal(t, 0, content.purges, "cleanup/expiry: finished=%d canceled=%v purges=%d deadline=%v", s.finished, s.finishCanceled, content.purges, content.deadline)
			require.True(t, content.deadline.Equal(s.value.SourceExpiresAt), "cleanup/expiry: finished=%d canceled=%v purges=%d deadline=%v", s.finished, s.finishCanceled, content.purges, content.deadline)
			if cancelDuringBuild {
				require.Equal(t, 0, s.published, "failed copy reached publication")
			}
		})
	}
}

func TestExamExportBuildRequiresTheClaimingJobIdentity(t *testing.T) {
	_, service, s, _, content := newExportFixture(t)
	input := store.ExamExportBuild{ExamExportArtifact: store.ExamExportArtifact{ExportID: s.value.ID, AttemptID: model.NewJobAttemptID()}, JobID: model.NewJobID()}
	input.ClaimToken, _ = model.NewJobClaimToken()
	for name, system := range map[string]invocation.System{
		"missing":       {},
		"other Job":     invocation.Job(model.NewJobID(), input.AttemptID),
		"other attempt": invocation.Job(input.JobID, model.NewJobAttemptID()),
		"runtime task":  invocation.Task("exam-export"),
	} {
		t.Run(name, func(t *testing.T) {
			err := service.BuildFromJob(context.Background(), system, input)
			require.Equal(t, "exam.export.unavailable", apperror.CodeOf(err, ""))
			require.Equal(t, 0, s.published)
			require.Equal(t, 0, content.purges)
		})
	}
}
