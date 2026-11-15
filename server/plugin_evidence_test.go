// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/store"
)

type evidenceWorkspaceStore struct {
	store.ExamAttemptWorkspaceStore
	input  *store.ExamAttemptEvidenceAppend
	result func(*store.ExamAttemptEvidenceAppend) *store.ExamAttemptEvidenceAppendResult
	err    error
}

func (s *evidenceWorkspaceStore) AppendAttemptEvidence(_ context.Context, input *store.ExamAttemptEvidenceAppend) (*store.ExamAttemptEvidenceAppendResult, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	return s.result(input), nil
}

func storedEvidence(input *store.ExamAttemptEvidenceAppend) *store.ExamAttemptEvidenceAppendResult {
	at := input.ObservedAt.UTC().Truncate(time.Millisecond)
	return &store.ExamAttemptEvidenceAppendResult{Evidence: model.AttemptEvidence{ID: input.EvidenceID,
		AttemptID: input.Access.AttemptID, Kind: input.Kind, Author: input.Access.WorkspaceAuthor(), Sequence: input.Sequence,
		Detail: input.Detail, ObservedAt: at, RecordedAt: at}}
}

func testBoundEnvironment() plugin.BoundEnvironment {
	return plugin.BoundEnvironment{AttemptID: model.NewExamAttemptID().String(), BindingID: model.NewAttemptEnvironmentBindingID().String(),
		Identity: plugin.EnvironmentIdentity{GrantID: "grant-1", Epoch: "epoch-1"}, Generation: 2}
}

func TestPluginEvidenceBoundMatchesTheModel(t *testing.T) {
	t.Parallel()

	require.Equal(t, model.IntegrityEvidenceDetailMaxBytes, plugin.MaxEvidenceDetailBytes)
}

func TestPluginEvidenceAppendsThroughTheEnvironmentAccess(t *testing.T) {
	t.Parallel()

	workspaces := &evidenceWorkspaceStore{result: storedEvidence}
	adapter := newStorePluginEvidence(workspaces)
	environment := testBoundEnvironment()
	record := plugin.EvidenceRecord{Sequence: 4, ObservedAt: time.Now(), Detail: json.RawMessage(`{"gap":"environment_restarted"}`)}
	receipt, err := adapter.Append(context.Background(), "execution.gap", environment, record)
	require.NoError(t, err)
	require.Equal(t, workspaces.input.EvidenceID.String(), receipt.ID, "the core proposes the record's identity")
	require.False(t, receipt.Repeated)
	require.Equal(t, store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: model.ExamAttemptID(environment.AttemptID),
		BindingID: model.AttemptEnvironmentBindingID(environment.BindingID),
		Identity:  model.AttemptEnvironmentIdentity{GrantID: "grant-1", Epoch: "epoch-1"}, Generation: 2}, workspaces.input.Access)
	require.Equal(t, model.AttemptEvidenceGap, workspaces.input.Kind)

	require.Nil(t, newStorePluginEvidence(nil), "no persistence, no evidence")
}

func TestPluginEvidenceRefusesBeforeTheStore(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*plugin.BoundEnvironment, *plugin.EvidenceRecord, *string){
		"native kind":   func(_ *plugin.BoundEnvironment, _ *plugin.EvidenceRecord, kind *string) { *kind = "native.display" },
		"bad binding":   func(e *plugin.BoundEnvironment, _ *plugin.EvidenceRecord, _ *string) { e.BindingID = "x" },
		"no generation": func(e *plugin.BoundEnvironment, _ *plugin.EvidenceRecord, _ *string) { e.Generation = 0 },
		"array detail": func(_ *plugin.BoundEnvironment, r *plugin.EvidenceRecord, _ *string) {
			r.Detail = json.RawMessage(`[]`)
		},
		"no time": func(_ *plugin.BoundEnvironment, r *plugin.EvidenceRecord, _ *string) { r.ObservedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			workspaces := &evidenceWorkspaceStore{result: storedEvidence}
			environment, kind := testBoundEnvironment(), "execution.process"
			record := plugin.EvidenceRecord{Sequence: 1, ObservedAt: time.Now(), Detail: json.RawMessage(`{}`)}
			mutate(&environment, &record, &kind)
			_, err := newStorePluginEvidence(workspaces).Append(context.Background(), kind, environment, record)
			require.ErrorIs(t, err, plugin.ErrEvidenceInvalid)
			require.Nil(t, workspaces.input, "the store was not called")
		})
	}
}

func TestPluginEvidenceStoreOutcomesReachPluginsAsPluginErrors(t *testing.T) {
	t.Parallel()

	transient := errors.New("connection reset")
	for _, test := range []struct {
		name   string
		err    error
		result func(*store.ExamAttemptEvidenceAppend) *store.ExamAttemptEvidenceAppendResult
		want   error
	}{
		{name: "binding not current", err: store.NewErrNotFound("exam_attempt_access", "a"), want: plugin.ErrBindingNotCurrent},
		{name: "sequence conflict", err: store.NewErrConflict("attempt_evidence", "attempt_evidence_sequence", nil),
			want: plugin.ErrEvidenceSequenceConflict},
		{name: "invalid", err: store.NewErrInvalidInput("attempt_evidence", "append", nil), want: plugin.ErrEvidenceInvalid},
		{name: "transient", err: transient, want: transient},
		{name: "refused", result: func(*store.ExamAttemptEvidenceAppend) *store.ExamAttemptEvidenceAppendResult {
			return &store.ExamAttemptEvidenceAppendResult{Refused: true}
		}, want: plugin.ErrEvidenceBudgetExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspaces := &evidenceWorkspaceStore{err: test.err, result: test.result}
			_, err := newStorePluginEvidence(workspaces).Append(context.Background(), "execution.process", testBoundEnvironment(),
				plugin.EvidenceRecord{Sequence: 1, ObservedAt: time.Now(), Detail: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, test.want)
		})
	}

	inconsistent := &evidenceWorkspaceStore{result: func(input *store.ExamAttemptEvidenceAppend) *store.ExamAttemptEvidenceAppendResult {
		result := storedEvidence(input)
		result.Evidence.Sequence++
		return result
	}}
	_, err := newStorePluginEvidence(inconsistent).Append(context.Background(), "execution.process", testBoundEnvironment(),
		plugin.EvidenceRecord{Sequence: 1, ObservedAt: time.Now(), Detail: json.RawMessage(`{}`)})
	require.Error(t, err, "the core verifies what the Store answered")
}
