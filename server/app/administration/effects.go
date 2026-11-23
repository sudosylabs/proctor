// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// publishRoleChanged reports a failed publication without failing the use
// case.
func (s *Service) publishRoleChanged(ctx context.Context, institution model.Resource, roleID model.RoleID) {
	institutionID, err := model.ParseInstitutionID(institution.ID)
	var event realtime.Event
	if err == nil {
		event, err = realtime.NewRoleChangedEvent(institutionID, roleID)
	}
	if err == nil {
		err = s.publisher.Publish(ctx, event)
	}
	if err != nil {
		s.publisher.Report(ctx, "role.changed", err)
	}
}

// publishRoleBindingChanged reports a failed publication without failing the
// use case.
func (s *Service) publishRoleBindingChanged(ctx context.Context, binding *model.RoleBinding) {
	if err := realtime.PublishRoleBindingChanged(ctx, s.publisher, s.classes, s.academicUnits, binding); err != nil {
		s.publisher.Report(ctx, "role_binding.changed", err)
	}
}

// publishAccessPolicyChanged closes the sessions a replacement revoked, drops
// the cached authentication of those it shortened, and then tells everyone
// who may view the policy to refetch it.
func (s *Service) publishAccessPolicyChanged(
	ctx context.Context,
	institutionID model.InstitutionID,
	replacement *store.AccessPolicyReplacementResult,
) error {
	for _, revocation := range replacement.SessionRevocations {
		s.publisher.SessionsRevoked(ctx, revocation.UserID, revocation.SessionIDs, revocation.AccessTokenHashes)
	}
	// Shortened sessions stay live, so only their cached authentication is
	// dropped.
	for _, deadline := range replacement.SessionDeadlineChanges {
		s.publisher.AuthenticationCacheInvalidated(ctx, deadline.UserID, deadline.AccessTokenHashes)
	}
	event, err := realtime.NewAccessPolicyChangedEvent(institutionID, replacement.Snapshot.Policy.Revision)
	if err != nil {
		return err
	}
	return s.publisher.Publish(ctx, event)
}
