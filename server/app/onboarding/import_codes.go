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

// The public code catalogues of onboarding imports and student progressions.
// Each is closed: the import service records only codes its catalogue lists
// and refuses to persist any other, so the published enums stay exact. Each
// may grow, and clients present an unknown value generically.

// OnboardingImportFailureCodes is every failure_code a failed import or
// student progression records: a lost credential, an unreadable file, an
// upload revalidation failure as an Invitation batch would record it, and the
// progression and academic administration filter preview failures.
func OnboardingImportFailureCodes() []string {
	return sortedCodes(
		[]string{"authentication.token_invalid", "onboarding_import.file_invalid"},
		invitationRowErrorCodes(),
		model.StudentProgressionFailureCodes(),
		model.AcademicAdministrationPreviewFailureCodes(),
	)
}

// OnboardingImportPreviewCodes is every preview_code a preview row records:
// malformed or duplicate CSV rows, target resolution failures as an
// Invitation batch would record them, the student progression row
// outcomes, and the academic administration filter preview skip reasons and
// no-change marker.
func OnboardingImportPreviewCodes() []string {
	return sortedCodes(
		[]string{"request.invalid", "onboarding_batch.duplicate"},
		invitationRowErrorCodes(),
		model.StudentProgressionPreviewCodes(),
		AcademicAdministrationPreviewSkipReasons(),
		[]string{store.AcademicAdministrationPreviewNoChange},
	)
}

// OnboardingImportRowCodes is every public_code an executed row records when
// it fails: a lost credential, and the item failures of the Invitation and
// academic administration batches that execute the row, which include
// student_progression.conflict for a changed source membership, and the
// System Administrator a filter preview row reached after it froze.
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

// requireOnboardingImportFailureCode refuses a failure_code outside the
// catalogue before it is persisted.
func requireOnboardingImportFailureCode(code string) error {
	if !slices.Contains(OnboardingImportFailureCodes(), code) {
		return fmt.Errorf("onboarding import failure code %q is outside the catalogue", code)
	}
	return nil
}

// requireOnboardingImportPreviewCodes refuses preview rows whose code lies
// outside the catalogue before they are persisted.
func requireOnboardingImportPreviewCodes(rows []store.OnboardingImportRow) error {
	codes := OnboardingImportPreviewCodes()
	for _, row := range rows {
		if row.PreviewCode != "" && !slices.Contains(codes, row.PreviewCode) {
			return fmt.Errorf("onboarding import preview code %q is outside the catalogue", row.PreviewCode)
		}
	}
	return nil
}

// failImport marks an import failed with a catalogued failure_code.
func (s *Service) failImport(ctx context.Context, id model.OnboardingImportID, code string) (*store.OnboardingImport, error) {
	if err := requireOnboardingImportFailureCode(code); err != nil {
		return nil, err
	}
	return s.imports.FailOnboardingImport(ctx, &store.OnboardingImportFailure{ID: id, Code: code, At: model.TimeUTC(s.now())})
}

// requireOnboardingImportRowCode refuses an executed row's public code
// outside the catalogue before it is persisted.
func requireOnboardingImportRowCode(code string) error {
	if code != "" && !slices.Contains(OnboardingImportRowCodes(), code) {
		return fmt.Errorf("onboarding import row code %q is outside the catalogue", code)
	}
	return nil
}
