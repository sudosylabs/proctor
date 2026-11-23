// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestAuditRepeatStore covers the repeat count beside an audit page.
func TestAuditRepeatStore(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	actor := saveUser(t, ctx, ss)
	unit := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "repeat-unit-"+model.NewID()[:6])
	save := func(resourceID string) *model.AuditEvent {
		t.Helper()
		event, err := ss.Audit().Save(ctx, &model.AuditEvent{
			ActorID: actor.ID, Action: string(model.ActionAcademicUnitManage),
			Resource:  model.Resource{Type: model.ResourceAcademicUnit, ID: resourceID},
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: unit.ID.String(),
			Status: model.AuditStatusSuccess, NodeID: "test-node",
		})
		require.NoError(t, err)
		time.Sleep(time.Millisecond)
		return event
	}
	first, second, third := save(unit.ID.String()), save(unit.ID.String()), save(unit.ID.String())
	other := save(model.NewAcademicUnitID().String())
	system, err := ss.Audit().Save(ctx, &model.AuditEvent{
		Action:    string(model.ActionAcademicUnitManage),
		Resource:  model.Resource{Type: model.ResourceAcademicUnit, ID: unit.ID.String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: unit.ID.String(), Status: model.AuditStatusSuccess, NodeID: "test-node",
	})
	require.NoError(t, err)

	count := func(visibility store.AuditVisibilityScope, maximum int) map[model.AuditEventID]int {
		t.Helper()
		repeats, countErr := ss.Audit().CountRepeats(ctx, store.AuditRepeatOptions{
			EventIDs:   []model.AuditEventID{first.ID, second.ID, third.ID, other.ID, system.ID},
			Visibility: visibility, Window: 10 * time.Minute, Maximum: maximum,
		})
		require.NoError(t, countErr)
		counted := map[model.AuditEventID]int{}
		for _, repeat := range repeats {
			counted[repeat.EventID] = repeat.Count
		}
		return counted
	}
	wide := store.AuditVisibilityScope{InstitutionWide: true}
	require.Equal(t, map[model.AuditEventID]int{first.ID: 1, second.ID: 2, third.ID: 3, other.ID: 1}, count(wide, 100),
		"each event counts itself and the earlier ones; an actorless event has no entry")
	require.Equal(t, 2, count(wide, 2)[third.ID], "the count stops at the maximum")

	hidden := store.AuditVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unit.ID}, AllowedActions: []string{string(model.ActionClassView)}}
	require.Equal(t, 0, count(hidden, 100)[third.ID], "an event the caller can't list is not counted")
}
