// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package operator_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/operator"
)

func TestListJobsRejectsAnInvalidQueryBeforeOpeningTheDatabase(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing.json")
	tests := []struct {
		name  string
		query operator.JobQuery
		want  string
	}{
		{name: "type", query: operator.JobQuery{Types: []string{"mail.nope"}}, want: `job type "mail.nope" is unknown`},
		{name: "state", query: operator.JobQuery{States: []string{"paused"}}, want: `job state "paused" is unknown`},
		{name: "limit", query: operator.JobQuery{Limit: operator.JobListMaximumLimit + 1}, want: "job list limit must be between 1 and 200"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := operator.ListJobs(context.Background(), missing, tt.query)
			require.EqualError(t, err, tt.want)
		})
	}
}

func TestShowJobRejectsAnInvalidID(t *testing.T) {
	t.Parallel()

	_, err := operator.ShowJob(context.Background(), filepath.Join(t.TempDir(), "missing.json"), "not a job id")
	require.EqualError(t, err, `job ID "not a job id" is invalid`)
}

func TestJobVocabularyIsTheModelVocabulary(t *testing.T) {
	t.Parallel()

	require.Len(t, operator.JobTypes(), len(model.JobTypes()))
	require.Contains(t, operator.JobTypes(), string(model.JobTypeMailDeliver))
	require.Equal(t, []string{"queued", "running", "cancel_requested", "succeeded", "failed", "canceled"}, operator.JobStates())
}
