// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/user.go access-token lifecycle.
// Proctor requires finite lifetimes, explicit known-action scopes, recent
// interactive authentication for creation, optional academic-unit ceilings,
// hashed persistence, and durable security auditing.

package identity

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	personalAccessTokenMutationPreparationLifetime = 5 * time.Minute
	// personalAccessTokenGrantableAcademicUnitLimit bounds the delegable
	// Academic Unit roots listed in the grantable-scope catalogue.
	personalAccessTokenGrantableAcademicUnitLimit = 50
)

// CreatePersonalAccessTokenCommand creates a new PAT for the interactive caller.
type CreatePersonalAccessTokenCommand struct {
	Description    string
	Scopes         []string
	AcademicUnitID model.AcademicUnitID
	ExpiresAt      int64
}

// ListPersonalAccessTokensQuery lists the caller's PATs.
type ListPersonalAccessTokensQuery struct{}

// ListGrantablePersonalAccessTokenScopesQuery lists the scopes the caller may
// currently place in a new PAT.
type ListGrantablePersonalAccessTokenScopesQuery struct{}

// PersonalAccessTokenGrantableScopes is the caller's current PAT scope
// catalogue. AcademicUnits holds at most 50 delegable roots ordered by display
// name; AcademicUnitsHasMore reports that more exist. Each scope lists only the
// returned roots at which it is delegable.
type PersonalAccessTokenGrantableScopes struct {
	Scopes               []PersonalAccessTokenGrantableScope
	AcademicUnits        []*model.AcademicUnit
	AcademicUnitsHasMore bool
}

// PersonalAccessTokenGrantableScope is one action the caller may delegate.
type PersonalAccessTokenGrantableScope struct {
	Action          model.Action
	ResourceTypes   []model.ResourceType
	InstitutionWide bool
	AcademicUnitIDs []model.AcademicUnitID
}

// RevokePersonalAccessTokenCommand revokes one owned PAT.
type RevokePersonalAccessTokenCommand struct {
	TokenID model.PersonalAccessTokenID
}

// SetPersonalAccessTokenDisabledCommand enables or disables an owned PAT.
type SetPersonalAccessTokenDisabledCommand struct {
	TokenID  model.PersonalAccessTokenID
	Disabled bool
}

// requirePersonalAccessTokenAdministration checks the collaborators of
// personal access token administration.
func (s *Service) requirePersonalAccessTokenAdministration() error {
	if s.personalAccessTokens == nil {
		return errors.New("personal access token administration store is required")
	}
	if s.users == nil {
		return errors.New("personal access token user store is required")
	}
	if s.academicUnits == nil {
		return errors.New("personal access token academic unit store is required")
	}
	if s.institutions == nil {
		return errors.New("personal access token institution store is required")
	}
	if s.authorizer == nil {
		return errors.New("personal access token scope authorization is required")
	}
	if s.mail == nil {
		return errors.New("personal access token mail preparer is required")
	}
	if s.newCredential == nil {
		return errors.New("personal access token credential generator is required")
	}
	if s.now == nil {
		return errors.New("personal access token clock is required")
	}
	return nil
}

// CreatePersonalAccessToken creates a PAT for the interactive caller within
// the actions the caller may delegate, and returns its credential once.
func (s *Service) CreatePersonalAccessToken(
	ctx context.Context,
	invocation invocation.Invocation,
	command CreatePersonalAccessTokenCommand,
) (*model.PersonalAccessTokenCreation, error) {
	principal := invocation.Principal()
	at := model.TimeUTC(s.now())
	if err := requireInteractiveSession(
		principal, true, at, s.recentAuthenticationTTL,
	); err != nil {
		return nil, err
	}

	settings := s.personalAccessTokenPolicy
	if command.ExpiresAt <= 0 {
		return nil, invalidPersonalAccessTokenRequest("expires_at")
	}

	normalizedScopes, err := normalizePersonalAccessTokenScopes(command.Scopes)
	if err != nil {
		return nil, err
	}

	if command.AcademicUnitID != "" {
		if !model.AcademicUnitID(command.AcademicUnitID).IsValid() {
			return nil, invalidPersonalAccessTokenRequest("academic_unit_id")
		}
		if _, err = s.academicUnits.Get(ctx, command.AcademicUnitID); err != nil {
			return nil, personalAccessTokenFailure("academic_unit", err)
		}
	}

	targetScopeType := model.RoleScopeInstitution
	targetScopeID := ""
	if command.AcademicUnitID != "" {
		targetScopeType = model.RoleScopeAcademicUnit
		targetScopeID = command.AcademicUnitID.String()
	} else {
		institution, lookupErr := s.institutions.GetSingleton(ctx)
		if lookupErr != nil {
			return nil, personalAccessTokenFailure("institution", lookupErr)
		}

		targetScopeID = institution.ID.String()
	}

	allowed, err := s.authorizer.CanDelegateActionsAtScope(
		ctx, principal, normalizedScopes, targetScopeType, targetScopeID,
	)
	if err != nil {
		return nil, personalAccessTokenFailure("authorization", err)
	}
	if !allowed {
		return nil, invalidPersonalAccessTokenRequest("scopes")
	}

	rawCredential := s.newCredential()
	candidate := &model.PersonalAccessToken{
		UserID:         principal.UserID,
		Description:    command.Description,
		TokenHash:      model.HashToken(rawCredential),
		Scopes:         normalizedScopes,
		AcademicUnitID: model.AcademicUnitID(command.AcademicUnitID),
		ExpiresAt:      model.TimeFromMillis(command.ExpiresAt),
	}
	parameters := map[string]any{
		"description":      command.Description,
		"scopes":           normalizedScopes,
		"academic_unit_id": command.AcademicUnitID,
		"expires_at":       command.ExpiresAt,
	}
	resource, err := s.personalAccessTokenAuditResource(ctx)
	if err != nil {
		return nil, err
	}

	auditDraft, appErr := s.preparePersonalAccessTokenAudit(
		ctx,
		principal,
		model.ActionPersonalAccessTokenCreate,
		resource,
		invocation.RequestMetadata(),
		parameters,
		nil,
	)
	if appErr != nil {
		return nil, appErr
	}

	prepared, err := s.personalAccessTokens.PrepareMutation(ctx, &store.PersonalAccessTokenMutationPreparation{
		UserID:     principal.UserID,
		Kind:       store.PersonalAccessTokenMutationCreate,
		AuditEvent: auditDraft,
		Lifetime:   personalAccessTokenMutationPreparationLifetime,
	})
	if err != nil {
		return nil, personalAccessTokenFailure("personal_access_token", err)
	}

	notice, err := s.preparePersonalAccessTokenNotice(
		ctx,
		principal.UserID,
		candidate,
		model.MailTemplateIdentityPersonalAccessTokenCreated,
		prepared.ActionAt,
	)
	if err != nil {
		return nil, s.failPersonalAccessTokenMutation(ctx, prepared.ID.String(), err)
	}

	result, err := s.personalAccessTokens.Create(ctx, &store.PersonalAccessTokenCreationMutation{
		Token:           candidate,
		MaximumActive:   settings.MaximumPerUser,
		MinimumLifetime: settings.MinimumLifetime,
		MaximumLifetime: settings.MaximumLifetime,
		PreparationID:   prepared.ID,
		Notice:          notice,
	})
	if err != nil {
		return nil, s.failPersonalAccessTokenMutation(ctx, prepared.ID.String(), err)
	}

	return &model.PersonalAccessTokenCreation{
		Token:      result.Token,
		Credential: rawCredential,
	}, nil
}

// ListPersonalAccessTokens lists the caller's PATs.
func (s *Service) ListPersonalAccessTokens(
	ctx context.Context,
	invocation invocation.Invocation,
	_ ListPersonalAccessTokensQuery,
) ([]*model.PersonalAccessToken, error) {
	principal := invocation.Principal()
	if err := requireInteractiveSession(principal, false, s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}
	tokens, err := s.personalAccessTokens.ListByUser(ctx, principal.UserID)
	if err != nil {
		return nil, personalAccessTokenFailure("personal_access_token", err)
	}
	return tokens, nil
}

// ListGrantablePersonalAccessTokenScopes reports which actions the interactive
// caller may currently delegate to a new PAT, and at which Academic Unit roots.
// It applies creation's delegation ceiling, so every offered placement is one
// CreatePersonalAccessToken accepts for the same principal.
func (s *Service) ListGrantablePersonalAccessTokenScopes(
	ctx context.Context,
	invocation invocation.Invocation,
	_ ListGrantablePersonalAccessTokenScopesQuery,
) (*PersonalAccessTokenGrantableScopes, error) {
	principal := invocation.Principal()
	if err := requireInteractiveSession(principal, false, s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}

	delegation, err := s.authorizer.PersonalAccessTokenDelegation(ctx, principal)
	if err != nil {
		return nil, personalAccessTokenFailure("authorization", err)
	}

	units := make([]*model.AcademicUnit, 0, len(delegation.AcademicUnitRootIDs))
	for _, id := range delegation.AcademicUnitRootIDs {
		unit, lookupErr := s.academicUnits.Get(ctx, id)
		if lookupErr != nil {
			// The root was resolved moments ago, so a missing unit is a race
			// with archival rather than a caller error.
			return nil, apperror.New("personal_access_token.unavailable").
				WithField("resource", "academic_unit").Wrap(lookupErr)
		}

		units = append(units, unit)
	}

	sort.SliceStable(units, func(i, j int) bool {
		if units[i].DisplayName != units[j].DisplayName {
			return units[i].DisplayName < units[j].DisplayName
		}

		return units[i].ID < units[j].ID
	})
	result := &PersonalAccessTokenGrantableScopes{
		Scopes: make([]PersonalAccessTokenGrantableScope, 0, len(delegation.Actions)),
	}
	if len(units) > personalAccessTokenGrantableAcademicUnitLimit {
		units = units[:personalAccessTokenGrantableAcademicUnitLimit]
		result.AcademicUnitsHasMore = true
	}

	result.AcademicUnits = units
	listed := make(map[model.AcademicUnitID]struct{}, len(units))
	for _, unit := range units {
		listed[unit.ID] = struct{}{}
	}

	for _, action := range delegation.Actions {
		definition, ok := model.DefinitionForAction(action.Action)
		if !ok {
			continue
		}

		scope := PersonalAccessTokenGrantableScope{
			Action:          action.Action,
			ResourceTypes:   definition.ResourceTypes(),
			InstitutionWide: action.InstitutionWide,
			AcademicUnitIDs: []model.AcademicUnitID{},
		}
		for _, id := range action.AcademicUnitIDs {
			if _, ok := listed[id]; ok {
				scope.AcademicUnitIDs = append(scope.AcademicUnitIDs, id)
			}
		}

		result.Scopes = append(result.Scopes, scope)
	}

	return result, nil
}

// RevokePersonalAccessToken revokes one of the caller's PATs.
func (s *Service) RevokePersonalAccessToken(
	ctx context.Context,
	invocation invocation.Invocation,
	command RevokePersonalAccessTokenCommand,
) (*model.PersonalAccessToken, error) {
	principal := invocation.Principal()
	if err := requireInteractiveSession(principal, false, s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}
	if !command.TokenID.IsValid() {
		return nil, invalidPersonalAccessTokenRequest("personal_access_token_id")
	}

	current, err := s.personalAccessTokens.Get(ctx, command.TokenID)
	if err != nil || current.UserID != principal.UserID {
		if err == nil {
			err = store.NewErrNotFound("personal_access_token", command.TokenID.String())
		}

		return nil, personalAccessTokenFailure("personal_access_token", err)
	}

	if current.RevokedAt.Valid {
		return current, nil
	}

	resource, err := s.personalAccessTokenAuditResource(ctx)
	if err != nil {
		return nil, err
	}

	auditDraft, appErr := s.preparePersonalAccessTokenAudit(
		ctx,
		principal,
		model.ActionPersonalAccessTokenRevoke,
		resource,
		invocation.RequestMetadata(),
		map[string]any{"personal_access_token_id": command.TokenID},
		current.Auditable(),
	)
	if appErr != nil {
		return nil, appErr
	}

	prepared, err := s.personalAccessTokens.PrepareMutation(ctx, &store.PersonalAccessTokenMutationPreparation{
		UserID:     principal.UserID,
		TokenID:    command.TokenID,
		Kind:       store.PersonalAccessTokenMutationRevoke,
		AuditEvent: auditDraft,
		Lifetime:   personalAccessTokenMutationPreparationLifetime,
	})
	if err != nil {
		return nil, personalAccessTokenFailure("personal_access_token", err)
	}

	notice, err := s.preparePersonalAccessTokenNotice(
		ctx,
		principal.UserID,
		current,
		model.MailTemplateIdentityPersonalAccessTokenRevoked,
		prepared.ActionAt,
	)
	if err != nil {
		return nil, s.failPersonalAccessTokenMutation(ctx, prepared.ID.String(), err)
	}

	result, err := s.personalAccessTokens.Revoke(ctx, &store.PersonalAccessTokenRevocation{
		ID:            command.TokenID,
		UserID:        principal.UserID,
		PreparationID: prepared.ID,
		Notice:        notice,
	})
	if err != nil {
		return nil, s.failPersonalAccessTokenMutation(ctx, prepared.ID.String(), err)
	}

	return result.Token, nil
}

// SetPersonalAccessTokenDisabled disables or enables one of the caller's
// PATs.
func (s *Service) SetPersonalAccessTokenDisabled(
	ctx context.Context,
	invocation invocation.Invocation,
	command SetPersonalAccessTokenDisabledCommand,
) (*model.PersonalAccessToken, error) {
	principal := invocation.Principal()
	at := model.TimeUTC(s.now())
	action := model.ActionPersonalAccessTokenDisable
	if !command.Disabled {
		action = model.ActionPersonalAccessTokenEnable
	}

	if err := requireInteractiveSession(
		principal, !command.Disabled, at, s.recentAuthenticationTTL,
	); err != nil {
		return nil, err
	}

	if !command.TokenID.IsValid() {
		return nil, invalidPersonalAccessTokenRequest("personal_access_token_id")
	}

	current, err := s.personalAccessTokens.Get(ctx, command.TokenID)
	if err != nil || current.UserID != principal.UserID {
		if err == nil {
			err = store.NewErrNotFound("personal_access_token", command.TokenID.String())
		}

		return nil, personalAccessTokenFailure("personal_access_token", err)
	}

	if command.Disabled == current.DisabledAt.Valid {
		return current, nil
	}

	resource, err := s.personalAccessTokenAuditResource(ctx)
	if err != nil {
		return nil, err
	}

	auditDraft, appErr := s.preparePersonalAccessTokenAudit(
		ctx,
		principal,
		action,
		resource,
		invocation.RequestMetadata(),
		map[string]any{
			"personal_access_token_id": command.TokenID,
			"disabled":                 command.Disabled,
		},
		current.Auditable(),
	)
	if appErr != nil {
		return nil, appErr
	}

	key := model.MailTemplateIdentityPersonalAccessTokenEnabled
	if command.Disabled {
		key = model.MailTemplateIdentityPersonalAccessTokenDisabled
	}

	kind := store.PersonalAccessTokenMutationEnable
	if command.Disabled {
		kind = store.PersonalAccessTokenMutationDisable
	}

	prepared, err := s.personalAccessTokens.PrepareMutation(ctx, &store.PersonalAccessTokenMutationPreparation{
		UserID:     principal.UserID,
		TokenID:    command.TokenID,
		Kind:       kind,
		AuditEvent: auditDraft,
		Lifetime:   personalAccessTokenMutationPreparationLifetime,
	})
	if err != nil {
		return nil, personalAccessTokenFailure("personal_access_token", err)
	}

	notice, err := s.preparePersonalAccessTokenNotice(ctx, principal.UserID, current, key, prepared.ActionAt)
	if err != nil {
		return nil, s.failPersonalAccessTokenMutation(ctx, prepared.ID.String(), err)
	}

	result, err := s.personalAccessTokens.ChangeState(ctx, &store.PersonalAccessTokenStateMutation{
		ID:            command.TokenID,
		UserID:        principal.UserID,
		Disabled:      command.Disabled,
		MaximumActive: s.personalAccessTokenPolicy.MaximumPerUser,
		PreparationID: prepared.ID,
		Notice:        notice,
	})
	if err != nil {
		return nil, s.failPersonalAccessTokenMutation(ctx, prepared.ID.String(), err)
	}

	return result.Token, nil
}

func (s *Service) preparePersonalAccessTokenNotice(
	ctx context.Context,
	userID model.UserID,
	token *model.PersonalAccessToken,
	key model.MailTemplateKey,
	at time.Time,
) (store.PersonalAccessTokenSecurityNotice, error) {
	user, err := s.users.Get(ctx, userID)
	if err != nil || user == nil || user.ID != userID {
		return store.PersonalAccessTokenSecurityNotice{}, personalAccessTokenFailure("user", err)
	}

	prepared, err := s.mail.PreparePersonalAccessTokenSecurityNotice(mail.PersonalAccessTokenPreparation{
		Recipient:          user,
		TemplateKey:        key,
		Description:        token.Description,
		ExpiresAt:          token.ExpiresAt,
		ActionAt:           at,
		ActionCount:        len(token.Scopes),
		AcademicUnitScoped: !token.AcademicUnitID.IsZero(),
	})
	if err != nil {
		return store.PersonalAccessTokenSecurityNotice{}, apperror.New("mail.unavailable").Wrap(err)
	}

	return store.PersonalAccessTokenSecurityNotice{
		Occurrence: prepared.Occurrence,
		Delivery:   prepared.Delivery,
		Job:        prepared.Job,
		ExpiresAt:  token.ExpiresAt,
	}, nil
}

func requireInteractiveSession(
	principal model.Principal,
	recent bool,
	now time.Time,
	recentAuthenticationTTL time.Duration,
) error {
	if principal.Validate() != nil ||
		principal.CredentialType != model.CredentialSessionAccess {
		return apperror.New("authentication.session_required")
	}

	if recent &&
		!principal.IsRecentlyAuthenticated(
			now,
			recentAuthenticationTTL,
		) {
		return apperror.New("authentication.reauthentication_required")
	}

	return nil
}

func normalizePersonalAccessTokenScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 || len(scopes) > model.PersonalAccessTokenScopeMaxCount {
		return nil, invalidPersonalAccessTokenRequest("scopes")
	}
	result := append([]string(nil), scopes...)
	sort.Strings(result)
	for index, scope := range result {
		if !model.IsPersonalAccessTokenAction(scope) ||
			(index > 0 && result[index-1] == scope) {
			return nil, invalidPersonalAccessTokenRequest("scopes")
		}
	}
	return result, nil
}

func (s *Service) personalAccessTokenAuditResource(
	ctx context.Context,
) (model.Resource, error) {
	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil {
		return model.Resource{}, personalAccessTokenFailure("institution", err)
	}
	return model.Resource{
		Type: model.ResourceInstitution,
		ID:   institution.ID.String(),
	}, nil
}

func (s *Service) failPersonalAccessTokenMutation(
	ctx context.Context,
	preparationID string,
	err error,
) error {
	mapped := personalAccessTokenFailure("personal_access_token", err)
	code := "personal_access_token.unavailable"
	if failure, ok := apperror.As(mapped); ok {
		code = failure.Code()
	}

	if terminalErr := s.personalAccessTokens.FailMutation(
		ctx,
		&store.PersonalAccessTokenMutationFailure{PreparationID: model.PersonalAccessTokenPreparationID(preparationID), ErrorCode: code},
	); terminalErr != nil {
		return apperror.New("audit.unavailable").Wrap(terminalErr)
	}

	return mapped
}

// preparePersonalAccessTokenAudit builds the bounded audit draft a durable
// token mutation preparation owns.
func (s *Service) preparePersonalAccessTokenAudit(
	ctx context.Context,
	principal model.Principal,
	action model.Action,
	resource model.Resource,
	metadata model.RequestMetadata,
	parameters map[string]any,
	prior map[string]any,
) (*model.AuditEvent, error) {
	if s.securityAudit == nil {
		return nil, access.AuditUnavailable(errors.New("personal access token audit service is unavailable"))
	}
	event, err := s.securityAudit.PrepareCriticalAction(
		ctx, principal, action, resource, metadata, parameters, prior,
	)
	if err != nil {
		return nil, err
	}
	return event, nil
}

func invalidPersonalAccessTokenRequest(field string) error {
	return apperror.New("personal_access_token.invalid").WithField("field", field)
}

// personalAccessTokenFailure presents a failed token operation. Application
// errors from notice preparation and delegation checks are not the caller's
// concern, so they are presented as token unavailability like any other
// non-Store failure.
func personalAccessTokenFailure(resource string, err error) error {
	if _, ok := apperror.As(err); ok {
		return apperror.New("personal_access_token.unavailable").WithField("resource", resource).Wrap(err)
	}

	return apperror.StoreTable{
		Rules: []apperror.StoreRule{{
			Kind:       apperror.StoreConflict,
			Entity:     "personal_access_token",
			Constraint: "personal_access_tokens_maximum_per_user",
			Code:       "personal_access_token.maximum_reached",
		}},
		NotFound:    "resource.not_found",
		Unavailable: "personal_access_token.unavailable",
		Fields:      map[string]string{"resource": resource},
	}.Translate(err)
}
