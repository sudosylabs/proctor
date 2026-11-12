// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"encoding/csv"
	"io"
	"strconv"
	"strings"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// OnboardingImportReport writes a finished onboarding import's row report.
func (s *Service) OnboardingImportReport(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
	output io.Writer,
) error {
	return s.reportImport(ctx, inv, id, output, "")
}

func (s *Service) reportImport(
	ctx context.Context,
	inv invocation.Invocation,
	id model.OnboardingImportID,
	output io.Writer,
	expectedMode model.OnboardingImportMode,
) error {
	if output == nil {
		return apperror.New("request.invalid")
	}
	if err := s.requireImports(); err != nil {
		return err
	}

	current, err := s.imports.GetOnboardingImport(ctx, id)
	if err != nil {
		return onboardingImportStoreErrors.Translate(err)
	}
	if err = s.authorizeImport(ctx, inv, current, false); err != nil {
		return err
	}
	if err = requireImportMode(current, expectedMode); err != nil {
		return err
	}

	if current.State != model.OnboardingImportCompleted &&
		current.State != model.OnboardingImportCompletedWithErrors &&
		current.State != model.OnboardingImportCanceled &&
		current.State != model.OnboardingImportFailed {
		return apperror.New("onboarding_import.conflict")
	}

	writer := csv.NewWriter(output)
	if err = writer.Write([]string{"row", "reference", "operation", "status", "invitation_id", "resource_id", "public_code"}); err != nil {
		return apperror.New("onboarding_import.unavailable").Wrap(err)
	}

	for after := 0; ; {
		page, pageErr := s.imports.ListOnboardingImportRows(ctx, id, after, store.OnboardingImportPageSize)
		if pageErr != nil {
			return onboardingImportStoreErrors.Translate(pageErr)
		}

		for _, row := range page.Rows {
			if err = writer.Write([]string{
				strconv.Itoa(row.RowNumber),
				escapeSpreadsheetFormula(row.Reference),
				row.Operation,
				string(row.Status),
				row.InvitationID.String(),
				row.ResourceID,
				row.PublicCode,
			}); err != nil {
				return apperror.New("onboarding_import.unavailable").Wrap(err)
			}

			after = row.RowNumber
		}

		if !page.More {
			break
		}
	}

	writer.Flush()
	if err = writer.Error(); err != nil {
		return apperror.New("onboarding_import.unavailable").Wrap(err)
	}

	return nil
}

func escapeSpreadsheetFormula(value string) string {
	if value == "" {
		return value
	}
	if strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}
