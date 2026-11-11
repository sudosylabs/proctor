// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestOnboardingImportReportRequiresTerminalState(t *testing.T) {
	t.Parallel()
	current := &store.OnboardingImport{ID: model.NewOnboardingImportID(), State: model.OnboardingImportPreviewReady,
		ScopeType: model.RoleScopeInstitution, ScopeID: model.NewInstitutionID().String()}
	service := &Service{imports: onboardingImportPersistenceFake{value: current}, authorizer: &authorizerFake{}}
	err := service.OnboardingImportReport(context.Background(), invocation.Invocation{}, current.ID, &strings.Builder{})
	require.True(t, apperror.Is(err, "onboarding_import.conflict"), "preview report error = %v", err)
}

func TestOnboardingImportReportEscapesEverySpreadsheetFormulaPrefix(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"=x", "+x", "-x", "@x", "\tx", "\rx"} {
		require.Equal(t, "'"+value, escapeSpreadsheetFormula(value), "escapeSpreadsheetFormula(%q)", value)
	}
	require.Equal(t, "safe", escapeSpreadsheetFormula("safe"))
}
