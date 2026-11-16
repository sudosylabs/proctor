// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"fmt"
	"slices"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The code catalogues below are closed: a code outside them is refused
// before it is persisted, so the published enums stay exact. They may grow;
// clients present an unknown value generically.

// OnboardingImportFailureCodes is every failure_code a failed import or
// student progression may record.
func OnboardingImportFailureCodes() []string {
	return sortedCodes(
		[]string{"authentication.token_invalid", "onboarding_import.file_invalid"},
		invitationRowErrorCodes(),
		model.StudentProgressionFailureCodes(),
		model.AcademicAdministrationPreviewFailureCodes(),
	)
}

// OnboardingImportPreviewCodes is every preview_code a preview row may record.
func OnboardingImportPreviewCodes() []string {
	return sortedCodes(
		[]string{"request.invalid", "onboarding_batch.duplicate"},
		invitationRowErrorCodes(),
		model.StudentProgressionPreviewCodes(),
		AcademicAdministrationPreviewSkipReasons(),
		[]string{store.AcademicAdministrationPreviewNoChange},
	)
}

// OnboardingImportRowCodes is every public_code a failed executed row may
// record.
func OnboardingImportRowCodes() []string {
	return sortedCodes(
		[]string{"authentication.token_invalid", academicAdministrationPreviewSystemAdministrator},
		invitationRowErrorCodes(),
		AcademicAdministrationBatchErrorCodes(),
	)
}

func sortedCodes(sets ...[]string) []string {
	var codes []string
	for _, set := range sets {
		codes = append(codes, set...)
	}
	slices.Sort(codes)
	return slices.Compact(codes)
}

func requireOnboardingImportFailureCode(code string) error {
	if !slices.Contains(OnboardingImportFailureCodes(), code) {
		return fmt.Errorf("onboarding import failure code %q is outside the catalogue", code)
	}
	return nil
}

func requireOnboardingImportPreviewCodes(rows []store.OnboardingImportRow) error {
	codes := OnboardingImportPreviewCodes()
	for _, row := range rows {
		if row.PreviewCode != "" && !slices.Contains(codes, row.PreviewCode) {
			return fmt.Errorf("onboarding import preview code %q is outside the catalogue", row.PreviewCode)
		}
	}
	return nil
}

func (s *Service) failImport(ctx context.Context, id model.OnboardingImportID, code string) (*store.OnboardingImport, error) {
	if err := requireOnboardingImportFailureCode(code); err != nil {
		return nil, err
	}
	return s.imports.FailOnboardingImport(ctx, &store.OnboardingImportFailure{ID: id, Code: code, At: model.TimeUTC(s.now())})
}

func requireOnboardingImportRowCode(code string) error {
	if code != "" && !slices.Contains(OnboardingImportRowCodes(), code) {
		return fmt.Errorf("onboarding import row code %q is outside the catalogue", code)
	}
	return nil
}
