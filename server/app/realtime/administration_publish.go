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
)

// ClassUnits finds the academic unit that owns a Class, which class.changed
// names; store.ClassStore satisfies it.
type ClassUnits interface {
	GetAcademicUnitID(context.Context, model.ClassID) (model.AcademicUnitID, error)
}

// PublishClassesChanged publishes class.changed once for each distinct
// Class. One failure never stops the rest; failures are returned joined.
func PublishClassesChanged(ctx context.Context, publisher Publisher, units ClassUnits, classIDs ...model.ClassID) error {
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
// scope that it changed. A binding at a Class changes that Class's teachers,
// so its viewers hear class.changed too; the holder's own clients hear
// current_user.context.changed from the authorization invalidation.
func PublishRoleBindingChanged(ctx context.Context, publisher Publisher, units ClassUnits, binding *model.RoleBinding) error {
	if publisher == nil {
		return errors.New("Role Binding change publication dependencies are invalid")
	}
	event, err := NewRoleBindingChangedEvent(binding)
	if err != nil {
		return err
	}
	errs := []error{publisher.Publish(ctx, event)}
	if binding.ScopeType == model.RoleScopeClass {
		errs = append(errs, PublishClassesChanged(ctx, publisher, units, model.ClassID(binding.ScopeID)))
	}
	return errors.Join(errs...)
}
