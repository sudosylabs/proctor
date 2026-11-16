// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/migrations"
	"github.com/sudosylabs/proctor/server/model"
)

// TestBaselineSealsTheModelsEmptyAttemptEvidenceInventory keeps the seed
// the seal trigger digests for an Attempt without Attempt evidence equal
// to the model's empty inventory, and the Go adapter's copy of it.
func TestBaselineSealsTheModelsEmptyAttemptEvidenceInventory(t *testing.T) {
	t.Parallel()
	source, err := migrations.Assets.ReadFile("postgres/000001_baseline.up.sql")
	require.NoError(t, err)
	match := regexp.MustCompile(`'sha256:' \|\| encode\(sha256\(convert_to\('([^']+)', 'UTF8'\)\), 'hex'\), NEW\.id`).FindSubmatch(source)
	require.Len(t, match, 2, "baseline seals no empty Attempt evidence inventory")
	require.Equal(t, model.NewAttemptEvidenceInventory().Digest, model.SHA256Fingerprint(match[1]))
	require.Equal(t, model.NewAttemptEvidenceInventory().Digest, emptyAttemptEvidenceDigest)
}

// TestBaselineDispatchJobIsTheModels keeps the dispatch Job the schema's
// enqueue_domain_event writes equal to model.NewDomainEventDispatchJob's type
// and attempt budget.
func TestBaselineDispatchJobIsTheModels(t *testing.T) {
	t.Parallel()
	source, err := migrations.Assets.ReadFile("postgres/000001_baseline.up.sql")
	require.NoError(t, err)
	match := regexp.MustCompile(`VALUES \(new_opaque_id\(\), '([a-z_.]+)', 'queued', event_at, event_at, event_at,\s+` +
		`jsonb_build_object\('event_id', event_id\), event_id, 'active', (\d+), 1\)`).FindSubmatch(source)
	require.Len(t, match, 3, "baseline enqueues no domain event dispatch Job")
	require.Equal(t, string(model.JobTypeDomainEventDispatch), string(match[1]))
	require.Equal(t, "8", string(match[2]))
	require.Equal(t, 8, model.DomainEventDispatchMaximumAttempts)
}
