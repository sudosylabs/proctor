// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"fmt"

	"github.com/sudosylabs/proctor/server/model"
)

// Admission freezes the published recipe even if its profile was retired later.
// The profile's specification is immutable; retirement affects publication only.
func freezeAttemptAssistant(ctx context.Context, tx *sqlxTxWrapper, attemptID model.ExamAttemptID, revisionID string) error {
	var canonical []byte
	if err := tx.Get(ctx, &canonical, `SELECT policy_canonical FROM exam_revisions WHERE id=?`, revisionID); err != nil {
		return fmt.Errorf("read admitted assistant policy: %w", err)
	}
	policy, err := model.DecodeExamPolicySet(canonical)
	if err != nil {
		return invalidPersistedState("exam_revision", "assistant", err)
	}
	if !policy.Assistant.Enabled {
		return nil
	}
	var row assistantProfileRow
	if err = tx.Get(ctx, &row, `SELECT `+assistantProfileColumns+` FROM assistant_profiles WHERE id=?`, policy.Assistant.ProfileID.String()); err != nil {
		return fmt.Errorf("read admitted assistant profile: %w", err)
	}
	profile, err := row.model()
	if err != nil {
		return err
	}
	document, err := model.EncodeAssistantPolicy(policy.Assistant)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO exam_attempt_assistants
 (id,policy,provider,model,display_name,pseudonymize,created_at)
 VALUES (?,?::jsonb,?,?,?,?,statement_timestamp())`, attemptID.String(), string(document), profile.Provider, profile.Model, profile.DisplayName, profile.Pseudonymize)
	if err != nil {
		return fmt.Errorf("freeze attempt assistant: %w", err)
	}
	return nil
}
