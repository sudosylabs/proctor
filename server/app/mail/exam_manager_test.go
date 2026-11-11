// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/model"
)

func TestExamManagerMailPreparerCreatesOneOrdinaryBoundedNotice(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	user := &model.User{Username: "manager", Email: "manager@example.edu", DisplayName: "Exam Manager"}
	user.PrepareCreate(model.NewUserID(), at.Add(-time.Hour))
	renderer := &examManagerRendererFake{}
	preparer, err := newDirectMailPreparer(renderer, &mailSenderFake{enabled: true, from: Address{Name: "Proctor", Address: "no-reply@example.test"}}, mailTestSealer(t), nil)
	require.NoError(t, err)
	prepared, err := preparer.PrepareManagerMail(exam.ManagerMailPreparation{
		Recipient: user, OccurrenceID: model.NewMailOccurrenceID(), TemplateKey: model.MailTemplateExamManagerAdded,
		ExamTitle: `Algorithms & <Systems>`, Relationship: exam.ManagerMailRelationshipManager, ActionAt: at,
	})
	require.NoError(t, err)
	require.Equal(t, model.MailOccurrenceExamManagement, prepared.Occurrence.Kind)
	require.Equal(t, user.ID, prepared.Occurrence.ActorUserID)
	require.Equal(t, user.ID, prepared.Delivery.TargetUserID)
	require.Equal(t, model.MailTemplateExamManagerAdded, prepared.Delivery.TemplateKey)
	require.Equal(t, 72*time.Hour, prepared.Delivery.Deadline.Sub(prepared.Delivery.CreatedAt))
	require.Equal(t, model.JobTypeMailDeliver, prepared.Job.Type)
	require.Equal(t, `Algorithms & <Systems>`, renderer.details.Title)
	require.Equal(t, string(exam.ManagerMailRelationshipManager), renderer.details.Relationship)
	require.True(t, renderer.details.ActionAt.Equal(at))
}

type examManagerRendererFake struct{ details ExamManagerDetails }

func (r *examManagerRendererFake) Render(request RenderRequest) (FrozenContent, error) {
	details, ok := request.Presentation.(ExamManagerDetails)
	if !ok {
		return FrozenContent{}, nil
	}
	r.details = details
	return FrozenContent{Subject: "Exam changed", Text: details.Title, HTML: "<p>Exam changed</p>"}, nil
}
