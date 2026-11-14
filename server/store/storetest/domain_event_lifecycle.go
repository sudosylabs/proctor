// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// LifecycleDomainEvent is one outbox row and the dispatch Job written with
// it.
type LifecycleDomainEvent struct {
	Event *model.DomainEvent
	JobID model.JobID
}

// LifecycleDomainEventFinder returns, oldest first, the undelivered events of
// topic whose Data member names value. The public contract has no listing,
// so only the adapter can find rows it wrote by itself.
type LifecycleDomainEventFinder func(t *testing.T, ctx context.Context, topic model.DomainEventTopic, member, value string) []LifecycleDomainEvent

// TestLifecycleDomainEvents verifies that a Participation generation
// starting and ending, and a Sitting pausing, resuming and entering Closing,
// each commit one identifier-only event with its dispatch Job, including
// when the generation ends through the Session revocation fence.
func TestLifecycleDomainEvents(t *testing.T, ss store.Store, find LifecycleDomainEventFinder) {
	t.Helper()
	ctx := context.Background()
	fixture := newExamAttemptFixture(t, ctx, ss)
	sittingData := map[string]any{"sitting_id": fixture.sitting.ID.String(), "exam_id": fixture.examID.String()}

	connected, _ := connectFocusLossFixture(t, ctx, ss, fixture, "lifecycle-events-connect")
	participationData := map[string]any{
		"attempt_id": connected.Attempt.ID.String(), "participation_id": connected.Participation.ID.String(),
		"generation": float64(connected.Participation.Generation), "sitting_id": fixture.sitting.ID.String(),
		"exam_id": fixture.examID.String(),
	}
	started := requireOneLifecycleEvent(t, ctx, ss, find, model.DomainEventTopicExamAttemptParticipationStarted,
		"participation_id", connected.Participation.ID.String(), participationData)
	require.True(t, started.OccurredAt.Equal(connected.Participation.StartedAt), "started at %s, generation started at %s",
		started.OccurredAt, connected.Participation.StartedAt)

	paused, err := ss.ExamSitting().Pause(ctx, &store.ExamSittingManagerTransition{ExamID: fixture.examID,
		SittingID: fixture.sitting.ID, ActorUserID: fixture.manager.ID, ExpectedRevision: fixture.sitting.Revision,
		PrivateReason: "lifecycle event pause", ChangedAt: model.NowUTC(),
		AuditEventID: saveExamSittingAudit(t, ctx, ss, fixture.manager.ID, fixture.examID, fixture.unitID).ID, AuditAt: model.NowUTC()},
		examCommand(fixture.manager.ID, "exam.sitting.pause", "lifecycle-events-pause", "lifecycle-events-pause"))
	require.NoError(t, err)
	requireOneLifecycleEvent(t, ctx, ss, find, model.DomainEventTopicExamSittingPaused, "sitting_id", fixture.sitting.ID.String(), sittingData)

	resumed, err := ss.ExamSitting().Resume(ctx, &store.ExamSittingManagerTransition{ExamID: fixture.examID,
		SittingID: fixture.sitting.ID, ActorUserID: fixture.manager.ID, ExpectedRevision: paused.Value.Sitting.Revision,
		PrivateReason: "lifecycle event resume", ChangedAt: model.NowUTC(),
		AuditEventID: saveExamSittingAudit(t, ctx, ss, fixture.manager.ID, fixture.examID, fixture.unitID).ID, AuditAt: model.NowUTC()},
		examCommand(fixture.manager.ID, "exam.sitting.resume", "lifecycle-events-resume", "lifecycle-events-resume"))
	require.NoError(t, err)
	requireOneLifecycleEvent(t, ctx, ss, find, model.DomainEventTopicExamSittingResumed, "sitting_id", fixture.sitting.ID.String(), sittingData)

	// Revoking the candidate's Session ends the generation in SQL, through
	// the revocation fence, not through any Store operation of the Attempt.
	_, err = RevokeAllUserSessions(ctx, ss, fixture.candidate.ID, model.NowUTC())
	require.NoError(t, err)
	endedData := map[string]any{"reason": string(model.AttemptParticipationEndPolicySuspended)}
	for key, value := range participationData {
		endedData[key] = value
	}
	requireOneLifecycleEvent(t, ctx, ss, find, model.DomainEventTopicExamAttemptParticipationEnded,
		"participation_id", connected.Participation.ID.String(), endedData)
	require.Empty(t, find(t, ctx, model.DomainEventTopicExamAttemptParticipationStarted, "attempt_id", connected.Attempt.ID.String())[1:],
		"ending a generation starts none")

	closeAt := model.NowUTC()
	_, err = ss.ExamSitting().EarlyClose(ctx, &store.ExamSittingManagerTransition{ExamID: fixture.examID,
		SittingID: fixture.sitting.ID, ActorUserID: fixture.manager.ID, ExpectedRevision: resumed.Value.Sitting.Revision,
		FinalizeJob:   newExamSittingFinalizeJob(t, fixture.sitting.ID, resumed.Value.Sitting.Revision+1, closeAt),
		PrivateReason: "lifecycle event close", ChangedAt: closeAt,
		AuditEventID: saveExamSittingAudit(t, ctx, ss, fixture.manager.ID, fixture.examID, fixture.unitID).ID, AuditAt: model.NowUTC()},
		examCommand(fixture.manager.ID, "exam.sitting.close", "lifecycle-events-close", "lifecycle-events-close"))
	require.NoError(t, err)
	requireOneLifecycleEvent(t, ctx, ss, find, model.DomainEventTopicExamSittingClosing, "sitting_id", fixture.sitting.ID.String(), sittingData)
	require.Len(t, find(t, ctx, model.DomainEventTopicExamSittingPaused, "sitting_id", fixture.sitting.ID.String()), 1,
		"closing records no second pause")
}

// requireOneLifecycleEvent asserts that exactly one event of topic names
// value, that its Data is exactly want, and that its dispatch Job is the one
// model.NewDomainEventDispatchJob would build.
func requireOneLifecycleEvent(t *testing.T, ctx context.Context, ss store.Store, find LifecycleDomainEventFinder,
	topic model.DomainEventTopic, member, value string, want map[string]any,
) *model.DomainEvent {
	t.Helper()
	found := find(t, ctx, topic, member, value)
	require.Len(t, found, 1, "%s events naming %s", topic, value)
	event := found[0].Event
	require.Equal(t, topic, event.Topic)
	require.NoError(t, event.Validate())
	var data map[string]any
	require.NoError(t, json.Unmarshal(event.Data, &data))
	require.Equal(t, want, data)

	job, err := ss.Job().Get(ctx, found[0].JobID)
	require.NoError(t, err)
	expected, err := model.NewDomainEventDispatchJob(found[0].JobID, event)
	require.NoError(t, err)
	require.Equal(t, expected.Type, job.Type)
	require.Equal(t, model.JobStatusQueued, job.Status)
	require.Equal(t, expected.DedupeKey, job.DedupeKey)
	require.Equal(t, expected.DedupePolicy, job.DedupePolicy)
	require.Equal(t, expected.MaximumAttempts, job.MaximumAttempts)
	require.True(t, job.CreatedAt.Equal(event.OccurredAt) && job.AvailableAt.Equal(event.OccurredAt),
		"dispatch Job created %s available %s, event %s", job.CreatedAt, job.AvailableAt, event.OccurredAt)
	command, err := model.DecodeDomainEventDispatchCommand(job.Command)
	require.NoError(t, err)
	require.Equal(t, event.ID, command.EventID)
	return event
}
