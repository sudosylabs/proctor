// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// OnboardingImportFileNameMaximumRunes bounds a remembered upload file name.
const OnboardingImportFileNameMaximumRunes = 255

// NormalizeOnboardingImportFileName trims the uploader's file name and reports
// whether the result is an acceptable base name: valid UTF-8 of 1 to 255 runes,
// with no path separator or control character, and neither "." nor "..".
func NormalizeOnboardingImportFileName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	return name, ValidOnboardingImportFileName(name)
}

// ValidOnboardingImportFileName reports whether name is an already-trimmed
// acceptable base name (see NormalizeOnboardingImportFileName).
func ValidOnboardingImportFileName(name string) bool {
	if !utf8.ValidString(name) || name != strings.TrimSpace(name) || name == "." || name == ".." {
		return false
	}
	if count := utf8.RuneCountInString(name); count < 1 || count > OnboardingImportFileNameMaximumRunes {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// StudentProgressionFailureCodes is the closed catalogue of codes a student
// progression preview fails with: its Job's public failure codes beyond the
// shared onboarding ones, and the progression's own failure_code.
func StudentProgressionFailureCodes() []string {
	return []string{
		"student_progression.authorization_lost",
		"student_progression.target_conflict",
		"student_progression.lineage_conflict",
		"student_progression.effective_date_conflict",
		"student_progression.roster_too_large",
	}
}

// StudentProgressionPreviewCodes is the closed catalogue of row preview codes
// a student progression preview assigns to a source roster member.
func StudentProgressionPreviewCodes() []string {
	return []string{
		"student_progression.ineligible_target",
		"student_progression.destination_exists",
		"student_progression.destination_conflict",
		"student_progression.effective_date_conflict",
	}
}

// AcademicAdministrationPreviewFailureCodes is the closed catalogue of codes
// an academic administration filter preview's resolution fails with: its
// Job's public failure codes beyond the shared onboarding ones, and the
// preview's own failure_code. The caller lost the authority it asked with,
// the scope or Role changed, or the filter matched more Users than one
// preview holds.
func AcademicAdministrationPreviewFailureCodes() []string {
	return []string{
		"academic_administration_preview.authorization_lost",
		"academic_administration_preview.target_conflict",
		"academic_administration_preview.too_many_users",
	}
}
