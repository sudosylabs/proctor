// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSittingMailReconciliationPeriodicRunnerPreparesOneBoundedFanout(t *testing.T) {
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	record, err := model.NewExamSitting(model.NewExamSittingID(), model.NewExamID(), model.NewExamRevisionID(), model.NewClassID(),
		at.Add(time.Hour), at.Add(2*time.Hour), at)
	require.NoError(t, err)
	persistence := &sittingMailStoreFake{candidates: []store.ExamSittingMailReconciliationCandidate{{
		Sitting: record, ActorUserID: model.NewUserID(),
	}}}
	actor := persistence.candidates[0].ActorUserID
	composer, err := mail.NewSittingComposer(mail.SittingComposerDependencies{Renderer: mailtest.NewRenderer(t), Sender: &mailSenderFake{enabled: true,
		from: mail.Address{Name: "Proctor", Address: "no-reply@example.test"}}, Sealer: mailTestSealer(t), Now: func() time.Time { return at }})
	require.NoError(t, err)
	classes := &sittingMailClassStoreFake{class: &model.Class{ID: record.ClassID, DisplayName: "Class A"}}
	preparer := sitting.ScheduleMail{Composer: composer, Classes: classes,
		Revisions: sittingMailRevisionStoreFake{summary: &store.ExamRevisionSummary{ID: record.ExamRevisionID, ExamID: record.ExamID, Title: "Algorithms"}}}
	err = (sittingMailReconciliationRunner{sittings: persistence, mail: preparer}).Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, persistence.reconciled, "reconciliation=%#v", persistence.reconciled)
	require.Equal(t, record.ID, persistence.reconciled.SittingID, "reconciliation=%#v", persistence.reconciled)
	require.Equal(t, record.Revision, persistence.reconciled.ExpectedRevision, "the reconciliation is fenced at the listed revision")
	prepared := persistence.reconciled.Mail
	require.NotNil(t, prepared, "the reconciliation carries the prepared fan-out")
	require.Equal(t, store.ExamSittingMailReconciled, prepared.ChangeKind)
	require.Equal(t, actor, prepared.Occurrence.ActorUserID)
	require.NotNil(t, prepared.Bundle, "an enabled sender freezes the notice bundle")
	// A reconciliation names the Sitting's own Class as the prior one, so
	// only that Class is read.
	require.Equal(t, []model.ClassID{record.ClassID}, classes.reads)
}

// sittingMailClassStoreFake answers the Class a schedule notice names.
type sittingMailClassStoreFake struct {
	store.ClassStore
	class *model.Class
	reads []model.ClassID
}

func (f *sittingMailClassStoreFake) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	f.reads = append(f.reads, id)
	if f.class == nil || f.class.ID != id {
		return nil, store.NewErrNotFound("class", id.String())
	}
	return f.class, nil
}

// sittingMailRevisionStoreFake answers the published revision a schedule
// notice titles.
type sittingMailRevisionStoreFake struct {
	store.ExamRevisionStore
	summary *store.ExamRevisionSummary
}

func (f sittingMailRevisionStoreFake) GetSummary(context.Context, model.ExamID, model.ExamRevisionID) (*store.ExamRevisionSummary, error) {
	return f.summary, nil
}

type sittingMailStoreFake struct {
	store.ExamSittingStore
	candidates []store.ExamSittingMailReconciliationCandidate
	reconciled *store.ExamSittingMailReconciliation
}

func (f *sittingMailStoreFake) ListMailReconciliationDue(context.Context,
	store.ExamSittingMailReconciliationOptions,
) ([]store.ExamSittingMailReconciliationCandidate, error) {
	result := f.candidates
	f.candidates = nil
	return result, nil
}

func (f *sittingMailStoreFake) ReconcileMail(_ context.Context,
	input *store.ExamSittingMailReconciliation,
) (*store.ExamSittingMailFanoutSnapshot, error) {
	f.reconciled = input
	return &store.ExamSittingMailFanoutSnapshot{}, nil
}
