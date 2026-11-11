// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"strings"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type BeginProviderConnectionCommand struct {
	ProviderID string
	ReturnTo   string
	Source     string
}

// BeginProviderConnection starts linking a provider to the current User after
// strong recent authentication and audits the attempt it begins.
func (s *Service) BeginProviderConnection(
	ctx context.Context,
	invocation invocation.Invocation,
	command BeginProviderConnectionCommand,
) (*model.ExternalAuthenticationStart, error) {
	if err := access.RequireStrongRecentSession(invocation.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     model.ActionExternalIdentityManage,
		Resource:   model.Resource{Type: model.ResourceUser, ID: invocation.Principal().UserID.String()},
		Operation:  "connect_provider",
		Value:      map[string]any{"provider": strings.ToLower(strings.TrimSpace(command.ProviderID))},
	})
	if err != nil {
		return nil, err
	}

	start, appErr := s.beginForPurpose(
		ctx,
		command.ProviderID,
		command.ReturnTo,
		model.SessionClientWeb,
		"",
		"",
		command.Source,
		model.ExternalAuthenticationPurposeConnect,
		invocation.Principal().UserID,
		auditID,
		"",
		"",
		nil,
	)
	if appErr != nil {
		if failErr := s.auditor.Fail(ctx, auditID, appErrorCode(appErr)); failErr != nil {
			return nil, failErr
		}
	}

	return start, appErr
}

func (s *Service) completeProviderConnection(
	ctx context.Context,
	state *model.ExternalLoginState,
	assertion *model.ExternalAuthenticationAssertion,
	metadata model.RequestMetadata,
	institution *model.Institution,
) (*model.ExternalAuthenticationCompletion, error) {
	if state == nil ||
		state.Purpose != model.ExternalAuthenticationPurposeConnect ||
		!state.TargetUserID.IsValid() ||
		!state.ConsumedAt.Valid ||
		assertion == nil {
		return nil, invalidExternalAuthenticationError()
	}

	connectedAt := model.TimeUTC(state.ConsumedAt.Time)
	result, err := s.externalIdentities.Link(ctx, &store.ExternalIdentityLink{
		ExternalLoginStateID: state.ID,
		Identity: &model.ExternalIdentity{
			UserID:     state.TargetUserID,
			Provider:   state.Provider,
			Subject:    assertion.Subject,
			LastSeenAt: model.OptionalTimeFrom(connectedAt),
		},
		Capabilities: AccessDeploymentCapabilities(s.capabilities.Snapshot()),
		AuditEventID: model.AuditEventID(state.AuditEventID),
		AuditAt:      connectedAt,
	})
	if err != nil {
		mapped := authenticationMethodStoreErrors.Translate(err)
		if failErr := s.auditor.Fail(ctx, model.AuditEventID(state.AuditEventID), appErrorCode(mapped)); failErr != nil {
			return nil, failErr
		}

		return nil, mapped
	}

	_ = metadata
	_ = institution
	_ = result
	return &model.ExternalAuthenticationCompletion{ReturnTo: state.ReturnTo}, nil
}

func (s *Service) failConsumedProviderConnection(ctx context.Context, state *model.ExternalLoginState, failure error) error {
	if state == nil ||
		(state.Purpose != model.ExternalAuthenticationPurposeConnect && state.Purpose != model.ExternalAuthenticationPurposeReauthenticate) ||
		state.AuditEventID == "" {
		return failure
	}

	if err := s.auditor.Fail(ctx, model.AuditEventID(state.AuditEventID), appErrorCode(failure)); err != nil {
		return err
	}

	return failure
}
