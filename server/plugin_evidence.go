// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/store"
)

// storePluginEvidence adapts the Store's environment evidence append to the
// plugin host, which may not import store. The host names the plugin's
// policy kind; this adapter proposes the record's identity, translates the
// binding and answers with the plugin package's errors, so no store type
// reaches a plugin. The Store checks the binding and every bound, and no
// plugin code runs inside its transaction.
type storePluginEvidence struct {
	store store.ExamAttemptWorkspaceStore
}

var _ pluginhost.EvidenceStore = storePluginEvidence{}

// newStorePluginEvidence returns the adapter, or nil without persistence so
// the host reports evidence unavailable.
func newStorePluginEvidence(workspaces store.ExamAttemptWorkspaceStore) pluginhost.EvidenceStore {
	if workspaces == nil {
		return nil
	}
	return storePluginEvidence{store: workspaces}
}

func (e storePluginEvidence) Append(
	ctx context.Context,
	policyKind string,
	environment plugin.BoundEnvironment,
	record plugin.EvidenceRecord,
) (plugin.EvidenceReceipt, error) {
	access := store.ExamAttemptWorkspaceEnvironmentAccess{
		AttemptID: model.ExamAttemptID(environment.AttemptID),
		BindingID: model.AttemptEnvironmentBindingID(environment.BindingID),
		Identity: model.AttemptEnvironmentIdentity{GrantID: environment.Identity.GrantID, Epoch: environment.Identity.Epoch,
			Incarnation: environment.Identity.Incarnation},
		Generation: environment.Generation,
	}
	input := &store.ExamAttemptEnvironmentEvidenceAppend{
		Access: access, EvidenceID: model.NewIntegrityEvidenceID(), Kind: model.IntegrityPolicyKind(policyKind),
		Sequence: record.Sequence, Gap: record.Gap, Detail: record.Detail, ObservedAt: record.ObservedAt,
	}
	if !access.Valid() || record.ObservedAt.IsZero() ||
		!model.ValidCapabilityEvidenceClaim(input.Kind, record.Sequence, record.Detail) {
		return plugin.EvidenceReceipt{}, plugin.ErrEvidenceInvalid
	}

	result, err := e.store.AppendEnvironmentEvidence(ctx, input)
	if err != nil {
		return plugin.EvidenceReceipt{}, pluginEvidenceError(err)
	}
	if !result.Consistent(input) {
		return plugin.EvidenceReceipt{}, errors.New("store returned capability evidence inconsistent with the append")
	}
	if result.Refused {
		return plugin.EvidenceReceipt{}, plugin.ErrEvidenceBudgetExhausted
	}
	return plugin.EvidenceReceipt{ID: result.Evidence.ID.String(), RecordedAt: result.Evidence.RecordedAt,
		Repeated: result.Repeated}, nil
}

// pluginEvidenceError translates a Store refusal into the plugin contract.
// An unknown failure keeps its cause for the server log but names no store
// type to the plugin.
func pluginEvidenceError(err error) error {
	var conflict *store.ErrConflict
	switch {
	case store.IsNotFound(err):
		return plugin.ErrBindingNotCurrent
	case errors.As(err, &conflict) && conflict.Constraint == "capability_evidence_sequence":
		return plugin.ErrEvidenceSequenceConflict
	case errors.As(err, new(*store.ErrInvalidInput)):
		return plugin.ErrEvidenceInvalid
	default:
		return fmt.Errorf("append capability evidence: %w", err)
	}
}
