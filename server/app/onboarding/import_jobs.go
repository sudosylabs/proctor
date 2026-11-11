// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"errors"
	"slices"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// onboardingImportJobs translates the onboarding import Job entry points into
// the port the concrete onboarding import Jobs call.
type onboardingImportJobs struct{ service *Service }

func (a onboardingImportJobs) PreviewProgression(ctx context.Context, id model.OnboardingImportID) (string, error) {
	err := a.service.previewProgression(ctx, id)
	if err == nil {
		return "", nil
	}
	if failure, ok := apperror.As(err); ok {
		if code := failure.Code(); code == "authorization.denied" || code == "authentication.token_invalid" {
			return "student_progression.authorization_lost", err
		}
		if slices.Contains(model.StudentProgressionFailureCodes(), failure.Code()) {
			return failure.Code(), err
		}
	} else if store.IsNotFound(err) || store.IsConflict(err) {
		return "student_progression.target_conflict", err
	}
	return "", err
}

func (a onboardingImportJobs) Parse(ctx context.Context, id model.OnboardingImportID) (string, error) {
	err := a.service.parseImport(ctx, id)
	if err == nil {
		return "", nil
	}
	if failure, ok := apperror.As(err); ok && (failure.Code() == "authentication.token_invalid" || failure.Code() == "authorization.denied") {
		return "onboarding_import.authorization_lost", err
	}
	if errors.Is(err, errOnboardingImportInvalidFile) {
		return "onboarding_import.file_invalid", err
	}
	return "", err
}

func (a onboardingImportJobs) Get(ctx context.Context, id model.OnboardingImportID) (*store.OnboardingImport, error) {
	return a.service.imports.GetOnboardingImport(ctx, id)
}

func (a onboardingImportJobs) Execute(
	ctx context.Context,
	id model.OnboardingImportID,
	after int,
	checkpoint func(int, int) error,
) error {
	return a.service.executeImport(ctx, id, after, checkpoint)
}

func (a onboardingImportJobs) Fail(ctx context.Context, id model.OnboardingImportID, code string) error {
	_, err := a.service.failImport(ctx, id, code)
	return err
}
