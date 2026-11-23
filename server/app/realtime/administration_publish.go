// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// PublishClassesChanged publishes class.changed once for each distinct
// Class. One failure never stops the rest; failures are returned joined.
func PublishClassesChanged(ctx context.Context, publisher Publisher, units store.ClassStore, classIDs ...model.ClassID) error {
	if publisher == nil || units == nil {
		return errors.New("Class change publication dependencies are invalid")
	}
	var errs []error
	seen := make(map[model.ClassID]struct{}, len(classIDs))
	for _, classID := range classIDs {
		if _, done := seen[classID]; done {
			continue
		}
		seen[classID] = struct{}{}
		unitID, err := units.GetAcademicUnitID(ctx, classID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		event, err := NewClassChangedEvent(classID, unitID)
		if err == nil {
			err = publisher.Publish(ctx, event)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// PublishUsersChanged publishes user.changed once for each distinct User.
func PublishUsersChanged(ctx context.Context, publisher Publisher, userIDs ...model.UserID) error {
	if publisher == nil {
		return errors.New("User change publication dependencies are invalid")
	}
	var errs []error
	seen := make(map[model.UserID]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if _, done := seen[userID]; done {
			continue
		}
		seen[userID] = struct{}{}
		event, err := NewUserChangedEvent(userID)
		if err == nil {
			err = publisher.Publish(ctx, event)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// PublishRoleBindingChanged tells role_binding.view holders at the binding's
// scope that it changed. Scoped bindings can also change Class teachers or
// Academic Unit administrators. The authorization invalidation tells the
// holder's own clients.
func PublishRoleBindingChanged(ctx context.Context, publisher Publisher, classes store.ClassStore,
	units store.AcademicUnitStore, binding *model.RoleBinding,
) error {
	if publisher == nil {
		return errors.New("Role Binding change publication dependencies are invalid")
	}
	event, err := NewRoleBindingChangedEvent(binding)
	if err != nil {
		return err
	}
	errs := []error{publisher.Publish(ctx, event)}
	switch binding.ScopeType {
	case model.RoleScopeClass:
		errs = append(errs, PublishClassesChanged(ctx, publisher, classes, model.ClassID(binding.ScopeID)))
	case model.RoleScopeAcademicUnit:
		errs = append(errs, publishBindingUnitChanged(ctx, publisher, units, model.AcademicUnitID(binding.ScopeID)))
	}
	return errors.Join(errs...)
}

func publishBindingUnitChanged(ctx context.Context, publisher Publisher, units store.AcademicUnitStore, id model.AcademicUnitID) error {
	if units == nil {
		return errors.New("academic unit change publication requires an academic unit store")
	}
	unit, err := units.Get(ctx, id)
	if err != nil {
		return err
	}
	if unit == nil || unit.ID != id {
		return errors.New("academic unit change publication requires the bound unit")
	}
	event, err := NewAcademicUnitUpdatedEvent(unit.ID, unit.ParentID)
	if err != nil {
		return err
	}
	return publisher.Publish(ctx, event)
}
