// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func requireSnapshot(snapshot *store.ExamSittingSnapshot) (store.ExamSittingSnapshot, error) {
	if !snapshot.ConsistentAndValid() {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store returned an incomplete snapshot"))
	}

	return *snapshot, nil
}

func requireCommandResult(result *store.ExamSittingCommandResult) (store.ExamSittingSnapshot, error) {
	if result == nil || result.Value == nil {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(
			errors.New("Exam Sitting Store returned an incomplete result"),
		)
	}

	return requireSnapshot(result.Value)
}

func requireOwnedCommandResult(
	result *store.ExamSittingCommandResult,
	examID model.ExamID,
	sittingID model.ExamSittingID,
) (store.ExamSittingSnapshot, error) {
	value, err := requireCommandResult(result)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	if value.Sitting.ExamID != examID || value.Sitting.ID != sittingID {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(
			errors.New("Exam Sitting Store returned a mismatched result"),
		)
	}

	return value, nil
}

func requireScheduledCommandResult(
	result *store.ExamSittingCommandResult,
	examID model.ExamID,
	generatedID model.ExamSittingID,
) (store.ExamSittingSnapshot, error) {
	value, err := requireCommandResult(result)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	if value.Sitting.ExamID != examID || (!result.Replayed && value.Sitting.ID != generatedID) {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(
			errors.New("Exam Sitting Store returned a mismatched result"),
		)
	}

	return value, nil
}
