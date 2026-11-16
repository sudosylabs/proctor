// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
)

func TestWriteErrorLocalizesPresentationWithoutChangingMachineCode(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/protected", nil)
	request = request.WithContext(WithLocalization(context.Background(), problemTestLocalizer{}, "fr"))
	response := httptest.NewRecorder()
	WriteError(response, request, apperror.New("authorization.denied").Wrap(errors.New("private cause")))

	var problem Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "authorization.denied", problem.Code)
	require.Equal(t, http.StatusForbidden, problem.Status)
	require.Equal(t, "Accès refusé", problem.Title)
	require.Equal(t, "Cette action n’est pas autorisée.", problem.Detail)
}

func TestProblemPresentationHasNoHardcodedProseFallback(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/protected", nil)
	title, detail := localizedProblemPresentation(request, http.StatusForbidden)
	require.Empty(t, title, "presentation without a catalog")
	require.Empty(t, detail, "presentation without a catalog")

	response := httptest.NewRecorder()
	writeProblem(response, Problem{Status: http.StatusTeapot})
	var problem Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Empty(t, problem.Title)
}
