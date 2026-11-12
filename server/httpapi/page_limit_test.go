// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

// invalidPageParameters are the page values every collection rejects instead
// of silently reading the default page.
var invalidPageParameters = []string{
	"limit=0",
	"limit=201",
	"limit=-1",
	"limit=ten",
	"limit=",
	"limit=10&limit=20",
	"cursor=",
}

// TestHandWrittenListParsersRejectInvalidPages pins that each list parser that
// reads its own query applies the shared page rule. An out-of-range limit once
// returned a nil error, so the request ran without its filters and cursor.
func TestHandWrittenListParsersRejectInvalidPages(t *testing.T) {
	t.Parallel()

	parsers := map[string]func(*http.Request) error{
		"audits": func(r *http.Request) error { _, err := auditQueryFromRequest(r); return err },
		"jobs":   func(r *http.Request) error { _, err := listJobsQuery(r); return err },
		"job attempts": func(r *http.Request) error {
			_, _, err := attemptPagination(r)
			return err
		},
		"mail deliveries": func(r *http.Request) error { _, err := listMailDeliveriesQuery(r); return err },
		"invitations":     func(r *http.Request) error { _, err := invitationListQuery(r); return err },
		"memberships": func(r *http.Request) error {
			_, _, err := membershipPageQuery(r, model.Resource{Type: model.ResourceClass, ID: model.NewID()})
			return err
		},
	}
	for name, parse := range parsers {
		for _, raw := range invalidPageParameters {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/list?"+raw, nil)
			require.Error(t, parse(request), "%s ?%s", name, raw)
		}
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/list?limit=200", nil)
		require.NoError(t, parse(request), "%s accepts the maximum limit", name)
	}
}

func TestAuditAndJobQueriesKeepTheRequestedLimit(t *testing.T) {
	t.Parallel()

	audits, err := auditQueryFromRequest(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/audits?limit=10", nil))
	require.NoError(t, err)
	require.Equal(t, 10, audits.Limit)
	jobs, err := listJobsQuery(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/jobs?limit=25", nil))
	require.NoError(t, err)
	require.Equal(t, 25, jobs.Limit)
	limit, _, err := attemptPagination(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/jobs/x/attempts", nil))
	require.NoError(t, err)
	require.Equal(t, 50, limit)
}
