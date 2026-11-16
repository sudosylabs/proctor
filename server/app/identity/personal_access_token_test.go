// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestPersonalAccessTokenScopesRejectRelationshipOnlyParticipationAction(t *testing.T) {
	t.Parallel()

	_, err := normalizePersonalAccessTokenScopes([]string{string(model.ActionExamSittingParticipate)})
	require.Error(t, err, "normalizePersonalAccessTokenScopes() error")
	require.True(t, apperror.Is(err, "personal_access_token.invalid"), "normalizePersonalAccessTokenScopes() error")
}

func TestPersonalAccessTokenScopesRejectInteractiveOnlyAdministration(t *testing.T) {
	t.Parallel()

	for _, action := range []model.Action{
		model.ActionAccessPolicyManage,
		model.ActionExternalIdentityManage,
		model.ActionRoleBindingManage,
		model.ActionRoleManage,
	} {
		_, err := normalizePersonalAccessTokenScopes([]string{string(action)})
		require.True(t, apperror.Is(err, "personal_access_token.invalid"), "scope %q error = %v, want personal_access_token.invalid", action, err)
	}
}

func TestPersonalAccessTokenCreationCannotExceedCurrentRoleAuthority(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	userID := model.NewUserID()
	unitID := model.NewAcademicUnitID()
	tokens := &personalAccessTokenStoreFake{events: &[]string{}}
	// The caller holds no Role, in a unit the authorizer resolves as the
	// Service does.
	authorization := accessWith(t, nil, accesstest.Options{Units: []*model.AcademicUnit{{ID: unitID, Name: "unit"}}})
	service, err := personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: &personalAccessTokenUserStoreFake{}, academicUnits: &personalAccessTokenAcademicUnitStoreFake{unit: &model.AcademicUnit{ID: unitID}}, institutions: &personalAccessTokenInstitutionStoreFake{}, securityAudit: newSecurityAuditLog(t, nil).auditor, authorizer: authorization.Authorizer, mail: personalAccessTokenTestMail(t), personalAccessTokenPolicy: PersonalAccessTokenPolicy{MinimumLifetime: time.Minute, MaximumLifetime: 24 * time.Hour, MaximumPerUser: 3}, recentAuthenticationTTL: 15 * time.Minute, newCredential: func() string { return "must-not-be-generated" }, now: func() time.Time { return now }})
	require.NoError(t, err)
	_, err = service.CreatePersonalAccessToken(
		context.Background(),
		invocation.New(personalAccessTokenSessionPrincipal(userID, now), model.RequestMetadata{}),
		CreatePersonalAccessTokenCommand{
			Scopes: []string{string(model.ActionClassManage)}, AcademicUnitID: unitID,
			ExpiresAt: now.Add(time.Hour).UnixMilli(),
		},
	)
	require.True(t, apperror.Is(err, "personal_access_token.invalid"), "error = %v, want personal_access_token.invalid", err)
	require.Equal(t, 0, tokens.saveCalls, "save calls")
}

func TestPersonalAccessTokenAdministrationCreatesThroughFocusedContracts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	raw := "raw-credential-returned-once"
	userID := model.NewUserID()
	unitID := model.NewAcademicUnitID()
	institutionID := model.NewInstitutionID()
	events := []string{}
	tokens := &personalAccessTokenStoreFake{events: &events, saveAt: now}
	units := &personalAccessTokenAcademicUnitStoreFake{events: &events, unit: &model.AcademicUnit{ID: unitID}}
	institutions := &personalAccessTokenInstitutionStoreFake{
		events: &events, institution: &model.Institution{ID: institutionID},
	}
	authorization := grantedAccess(t, &events, accesstest.Grant{
		UserID: userID, Actions: []model.Action{model.ActionClassView},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
	})
	preparer, rendered := newTestMail(t, true)
	service, err := personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: &personalAccessTokenUserStoreFake{}, academicUnits: units, institutions: institutions, securityAudit: newSecurityAuditLog(t, nil).auditor, authorizer: authorization.Authorizer, mail: preparer, personalAccessTokenPolicy: PersonalAccessTokenPolicy{
		MinimumLifetime: time.Minute, MaximumLifetime: 24 * time.Hour, MaximumPerUser: 3,
	}, recentAuthenticationTTL: 15 * time.Minute, newCredential: func() string { return raw }, now: func() time.Time { return now }})
	require.NoError(t, err)

	created, err := service.CreatePersonalAccessToken(
		context.Background(),
		invocation.New(personalAccessTokenSessionPrincipal(userID, now), model.RequestMetadata{RequestID: "request-a"}),
		CreatePersonalAccessTokenCommand{
			Description: "automation", Scopes: []string{string(model.ActionClassView)},
			AcademicUnitID: unitID, ExpiresAt: now.Add(time.Hour).UnixMilli(),
		},
	)
	require.NoError(t, err)
	require.Equal(t, raw, created.Credential)
	require.Same(t, tokens.saved, created.Token)
	require.Equal(t, model.HashToken(raw), tokens.candidate.TokenHash)
	require.NotEqual(t, raw, tokens.candidate.TokenHash)
	require.Equal(t, userID, tokens.candidate.UserID, "save candidate = %#v, maximum", tokens.candidate)
	require.Equal(t, 3, tokens.maximum, "save candidate = %#v, maximum", tokens.candidate)
	require.Equal(t, 1, rendered.renders(), "mail requests")
	require.Equal(t, model.MailTemplateIdentityPersonalAccessTokenCreated, tokens.notice.Delivery.TemplateKey)
	notice := openedMail(t, tokens.notice.Delivery)
	require.Contains(t, notice.Text, "Token: automation")
	require.Contains(t, notice.Text, "Access: Limited to one academic unit")
	require.Contains(t, notice.Text, "Expires: Wednesday 12 August 2026\n", "the expiry is rendered")
	require.Contains(t, notice.Text, "Created: Wednesday 12 August 2026, 12:00 UTC", "the action time is rendered")
	for _, secret := range []string{raw, tokens.candidate.TokenHash, string(model.ActionClassView)} {
		require.NotContains(t, notice.Subject+notice.Text+notice.HTML, secret)
	}
	// The audit draft travels inside the durable preparation, which records
	// it as the token aggregate's pending attempt.
	require.Len(t, tokens.audits, 1, "audit drafts")
	audit := tokens.audits[0]
	require.Equal(t, string(model.ActionPersonalAccessTokenCreate), audit.Action, "audit action")
	require.Equal(t, model.AuditStatusAttempt, audit.Status, "audit status")
	require.Equal(t, userID, audit.ActorID, "audit actor")
	require.Equal(t, "request-a", audit.RequestID, "audit request")
	require.Equal(t, model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()}, audit.Resource, "audit action")
	require.NotContains(t, string(audit.Parameters)+string(audit.PriorState)+string(audit.Result), raw)
	// Contract (guardrail 8): authorization reads the caller's grants before
	// anything durable, and the preparation that carries the audit draft
	// precedes the durable write.
	require.Equal(t, []string{"academic_unit", "grants", "institution", "prepare", "create"}, events)
}

func TestPersonalAccessTokenAdministrationExactStateReplayEmitsNoAuditOrMail(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	userID := model.NewUserID()
	token := personalAccessTokenForTest(userID, now)
	token.DisabledAt = model.OptionalTimeFrom(now)
	tokens := &personalAccessTokenStoreFake{events: &[]string{}, current: token}
	preparer, rendered := newTestMail(t, true)
	service, err := personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: &personalAccessTokenUserStoreFake{}, academicUnits: &personalAccessTokenAcademicUnitStoreFake{}, institutions: &personalAccessTokenInstitutionStoreFake{}, securityAudit: newSecurityAuditLog(t, nil).auditor, authorizer: grantedAccess(t, nil).Authorizer, mail: preparer, personalAccessTokenPolicy: PersonalAccessTokenPolicy{MinimumLifetime: time.Minute, MaximumLifetime: 24 * time.Hour, MaximumPerUser: 3}, recentAuthenticationTTL: 15 * time.Minute, newCredential: func() string { return "credential" }, now: func() time.Time { return now }})
	require.NoError(t, err)

	got, appErr := service.SetPersonalAccessTokenDisabled(context.Background(), invocation.New(personalAccessTokenSessionPrincipal(userID, now), model.RequestMetadata{}), SetPersonalAccessTokenDisabledCommand{TokenID: token.ID, Disabled: true})
	require.NoError(t, appErr, "SetDisabled(replay)")
	require.Same(t, token, got, "SetDisabled(replay)")
	require.Empty(t, tokens.audits, "replay audit")
	require.Zero(t, rendered.renders(), "replay audit")
	require.Equal(t, 0, tokens.stateCalls, "replay audit")

	token.RevokedAt = model.OptionalTimeFrom(now)
	got, appErr = service.RevokePersonalAccessToken(context.Background(), invocation.New(personalAccessTokenSessionPrincipal(userID, now), model.RequestMetadata{}), token.ID)
	require.NoError(t, appErr, "Revoke(replay)")
	require.Same(t, token, got, "Revoke(replay)")
	require.Empty(t, tokens.audits, "replay audit")
	require.Zero(t, rendered.renders(), "replay audit")
	require.Equal(t, 0, tokens.revokeCalls, "replay audit")
}

func TestPersonalAccessTokenAdministrationConcealsAnotherUsersToken(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	callerID := model.NewUserID()
	token := personalAccessTokenForTest(model.NewUserID(), now)
	tokens := &personalAccessTokenStoreFake{events: &[]string{}, current: token}
	service := mustPersonalAccessTokenAdministrationService(
		t, tokens, &personalAccessTokenAcademicUnitStoreFake{},
		&personalAccessTokenInstitutionStoreFake{}, now,
	)

	_, err := service.RevokePersonalAccessToken(
		context.Background(),
		invocation.New(personalAccessTokenSessionPrincipal(callerID, now), model.RequestMetadata{}),
		token.ID,
	)
	require.True(t, apperror.Is(err, "resource.not_found"), "error = %v, want resource.not_found", err)
	require.Equal(t, 0, tokens.revokeCalls, "revoke calls")
	require.Empty(t, tokens.audits, "revoke calls")
}

func TestPersonalAccessTokenAdministrationUsesControlledClockForStateChanges(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	userID := model.NewUserID()
	token := personalAccessTokenForTest(userID, now)
	tokens := &personalAccessTokenStoreFake{events: &[]string{}, current: token}
	preparer, rendered := newTestMail(t, true)
	service, err := personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: &personalAccessTokenUserStoreFake{}, academicUnits: &personalAccessTokenAcademicUnitStoreFake{}, institutions: &personalAccessTokenInstitutionStoreFake{institution: &model.Institution{ID: model.NewInstitutionID()}}, securityAudit: newSecurityAuditLog(t, nil).auditor, authorizer: grantedAccess(t, nil).Authorizer, mail: preparer, personalAccessTokenPolicy: PersonalAccessTokenPolicy{MinimumLifetime: time.Minute, MaximumLifetime: 24 * time.Hour, MaximumPerUser: 3}, recentAuthenticationTTL: 15 * time.Minute, newCredential: func() string { return "credential" }, now: func() time.Time { return now }})
	require.NoError(t, err)
	inv := invocation.New(personalAccessTokenSessionPrincipal(userID, now), model.RequestMetadata{})

	_, err = service.SetPersonalAccessTokenDisabled(
		context.Background(), inv,
		SetPersonalAccessTokenDisabledCommand{TokenID: token.ID, Disabled: true},
	)
	require.NoError(t, err)
	require.Equal(t, now.UnixMilli(), tokens.stateAt, "state change at")
	require.Equal(t, 3, tokens.stateMaximum, "state change at")
	require.Equal(t, model.MailTemplateIdentityPersonalAccessTokenDisabled, tokens.notice.Delivery.TemplateKey, "disable notice")
	_, err = service.SetPersonalAccessTokenDisabled(
		context.Background(), inv,
		SetPersonalAccessTokenDisabledCommand{TokenID: token.ID, Disabled: false},
	)
	require.NoError(t, err)
	require.Equal(t, model.MailTemplateIdentityPersonalAccessTokenEnabled, tokens.notice.Delivery.TemplateKey, "enable notice")

	tokens.current = token
	_, err = service.RevokePersonalAccessToken(
		context.Background(), inv,
		token.ID,
	)
	require.NoError(t, err)
	require.Equal(t, now.UnixMilli(), tokens.revokedAt, "revoked at")
	require.Equal(t, model.MailTemplateIdentityPersonalAccessTokenRevoked, tokens.notice.Delivery.TemplateKey, "revoke notice")
	require.Equal(t, 3, rendered.renders(), "mail requests")
}

func TestPersonalAccessTokenAdministrationRequiresRecentSessionForCreationAndEnablement(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 12, 0, 0, 123701000, time.FixedZone("test", 2*60*60))
	userID := model.NewUserID()
	tokens := &personalAccessTokenStoreFake{events: &[]string{}}
	service := mustPersonalAccessTokenAdministrationService(
		t, tokens, &personalAccessTokenAcademicUnitStoreFake{},
		&personalAccessTokenInstitutionStoreFake{}, now,
	)
	stale := personalAccessTokenSessionPrincipal(userID, now.Add(-15*time.Minute-time.Microsecond))
	inv := invocation.New(stale, model.RequestMetadata{})

	_, err := service.CreatePersonalAccessToken(context.Background(), inv, CreatePersonalAccessTokenCommand{})
	require.True(t, apperror.Is(err, "authentication.reauthentication_required"), "create error = %v", err)
	_, err = service.SetPersonalAccessTokenDisabled(context.Background(), inv, SetPersonalAccessTokenDisabledCommand{
		TokenID: model.NewPersonalAccessTokenID(), Disabled: false,
	})
	require.True(t, apperror.Is(err, "authentication.reauthentication_required"), "enable error = %v", err)
	require.Equal(t, 0, tokens.saveCalls, "store calls: save")
	require.Equal(t, 0, tokens.stateCalls, "store calls: save")
}

func TestPersonalAccessTokenAdministrationJudgesRecencyByInjectedClock(t *testing.T) {
	t.Parallel()

	// The injected clock is decades away from wall time, so a recency decision
	// made against time.Now would reject the fresh principal and accept the
	// stale one.
	now := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	userID := model.NewUserID()
	service := mustPersonalAccessTokenAdministrationService(
		t, &personalAccessTokenStoreFake{events: &[]string{}}, &personalAccessTokenAcademicUnitStoreFake{},
		&personalAccessTokenInstitutionStoreFake{}, now,
	)
	fresh := invocation.New(personalAccessTokenSessionPrincipal(userID, now), model.RequestMetadata{})
	_, err := service.CreatePersonalAccessToken(context.Background(), fresh, CreatePersonalAccessTokenCommand{})
	require.True(t, apperror.Is(err, "personal_access_token.invalid"), "fresh create error = %v, want the expires_at validation after the recency check", err)
	stale := invocation.New(personalAccessTokenSessionPrincipal(userID, now.Add(-16*time.Minute)), model.RequestMetadata{})
	_, err = service.CreatePersonalAccessToken(context.Background(), stale, CreatePersonalAccessTokenCommand{})
	require.True(t, apperror.Is(err, "authentication.reauthentication_required"), "stale create error = %v", err)
}

func TestPersonalAccessTokenAdministrationRequiresFocusedDependencies(t *testing.T) {
	t.Parallel()

	tokens := &personalAccessTokenStoreFake{}
	users := &personalAccessTokenUserStoreFake{}
	units := &personalAccessTokenAcademicUnitStoreFake{}
	institutions := &personalAccessTokenInstitutionStoreFake{}
	audit := newSecurityAuditLog(t, nil).auditor
	// The caller of the unaudited creation below may delegate class.view
	// Institution-wide, so it reaches the audit.
	callerID := model.NewUserID()
	granted := grantedAccess(t, nil, institutionGrant(callerID, model.ActionClassView))
	authorization := granted.Authorizer
	preparer := personalAccessTokenTestMail(t)
	policy := PersonalAccessTokenPolicy{MinimumLifetime: time.Minute, MaximumLifetime: time.Hour, MaximumPerUser: 1}
	generator := func() string { return "credential" }

	tests := []struct {
		name string
		make func() (*Service, error)
	}{
		{"token store", func() (*Service, error) {
			return personalAccessTokenTestService(&Service{personalAccessTokens: nil, users: users, academicUnits: units, institutions: institutions, securityAudit: audit, authorizer: authorization, mail: preparer, personalAccessTokenPolicy: policy, recentAuthenticationTTL: time.Minute, newCredential: generator, now: time.Now})
		}},
		{"user store", func() (*Service, error) {
			return personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: nil, academicUnits: units, institutions: institutions, securityAudit: audit, authorizer: authorization, mail: preparer, personalAccessTokenPolicy: policy, recentAuthenticationTTL: time.Minute, newCredential: generator, now: time.Now})
		}},
		{"academic unit store", func() (*Service, error) {
			return personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: users, academicUnits: nil, institutions: institutions, securityAudit: audit, authorizer: authorization, mail: preparer, personalAccessTokenPolicy: policy, recentAuthenticationTTL: time.Minute, newCredential: generator, now: time.Now})
		}},
		{"institution store", func() (*Service, error) {
			return personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: users, academicUnits: units, institutions: nil, securityAudit: audit, authorizer: authorization, mail: preparer, personalAccessTokenPolicy: policy, recentAuthenticationTTL: time.Minute, newCredential: generator, now: time.Now})
		}},
		{"audit", func() (*Service, error) {
			return personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: users, academicUnits: units, institutions: institutions, securityAudit: nil, authorizer: authorization, mail: preparer, personalAccessTokenPolicy: policy, recentAuthenticationTTL: time.Minute, newCredential: generator, now: time.Now})
		}},
		{"authorization", func() (*Service, error) {
			return personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: users, academicUnits: units, institutions: institutions, securityAudit: audit, authorizer: nil, mail: preparer, personalAccessTokenPolicy: policy, recentAuthenticationTTL: time.Minute, newCredential: generator, now: time.Now})
		}},
		{"mail", func() (*Service, error) {
			return personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: users, academicUnits: units, institutions: institutions, securityAudit: audit, authorizer: authorization, mail: nil, personalAccessTokenPolicy: policy, recentAuthenticationTTL: time.Minute, newCredential: generator, now: time.Now})
		}},
		{"generator", func() (*Service, error) {
			return personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: users, academicUnits: units, institutions: institutions, securityAudit: audit, authorizer: authorization, mail: preparer, personalAccessTokenPolicy: policy, recentAuthenticationTTL: time.Minute, newCredential: nil, now: time.Now})
		}},
		{"clock", func() (*Service, error) {
			return personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: users, academicUnits: units, institutions: institutions, securityAudit: audit, authorizer: authorization, mail: preparer, personalAccessTokenPolicy: policy, recentAuthenticationTTL: time.Minute, newCredential: generator, now: nil})
		}},
	}
	var withoutAudit func() (*Service, error)
	for _, test := range tests {
		if test.name == "audit" {
			withoutAudit = test.make
			continue
		}
		t.Run(test.name, func(t *testing.T) {
			_, err := test.make()
			require.Error(t, err, "nil dependency was accepted")
		})
	}
	// Composition without an Auditor still builds, as it did when the token
	// audit was an adapter over a nil Auditor; every audited token mutation
	// then fails closed before it prepares anything durable.
	t.Run("audit", func(t *testing.T) {
		require.NotNil(t, withoutAudit, "the dependency table has no audit case")
		service, err := withoutAudit()
		require.NoError(t, err)
		service.now = func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) }
		service.institutions = &personalAccessTokenInstitutionStoreFake{institution: granted.Institution}
		now := service.now()
		_, err = service.CreatePersonalAccessToken(context.Background(),
			invocation.New(personalAccessTokenSessionPrincipal(callerID, now), model.RequestMetadata{}),
			CreatePersonalAccessTokenCommand{Scopes: []string{string(model.ActionClassView)}, ExpiresAt: now.Add(time.Hour).UnixMilli()})
		require.True(t, apperror.Is(err, "audit.unavailable"), "unaudited creation = %v", err)
		require.Zero(t, tokens.prepareCalls, "unaudited creation prepared a durable mutation")
	})
}

func TestPersonalAccessTokenAdministrationDoesNotRetainRootStore(t *testing.T) {
	t.Parallel()

	rootStoreType := reflect.TypeOf((*store.Store)(nil)).Elem()
	serviceType := reflect.TypeOf(Service{})
	for index := range serviceType.NumField() {
		field := serviceType.Field(index)
		require.NotEqual(t, rootStoreType, field.Type, "field %q retains store.Store", field.Name)
		require.False(t, field.Type.Implements(rootStoreType), "field %q retains store.Store", field.Name)
	}
}

// personalAccessTokenTestService checks the personal access token
// administration collaborators of a test Service, as New does.
func personalAccessTokenTestService(service *Service) (*Service, error) {
	return service, service.requirePersonalAccessTokenAdministration()
}

func mustPersonalAccessTokenAdministrationService(
	t *testing.T,
	tokens store.PersonalAccessTokenStore,
	units store.AcademicUnitStore,
	institutions store.InstitutionStore,
	now time.Time,
) *Service {
	t.Helper()
	// The caller holds no Role: these tests never reach delegation.
	service, err := personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: &personalAccessTokenUserStoreFake{}, academicUnits: units, institutions: institutions, securityAudit: newSecurityAuditLog(t, nil).auditor, authorizer: grantedAccess(t, nil).Authorizer, mail: personalAccessTokenTestMail(t), personalAccessTokenPolicy: PersonalAccessTokenPolicy{MinimumLifetime: time.Minute, MaximumLifetime: 24 * time.Hour, MaximumPerUser: 3}, recentAuthenticationTTL: 15 * time.Minute, newCredential: func() string { return "credential" }, now: func() time.Time { return now }})
	require.NoError(t, err)
	return service
}

// personalAccessTokenTestMail is the production mail Composer for tests that
// do not read the notices it renders.
func personalAccessTokenTestMail(t *testing.T) *mail.Composer {
	t.Helper()
	composer, _ := newTestMail(t, true)
	return composer
}

func TestPersonalAccessTokenAdministrationRendersPostgreSQLPreparedActionTime(t *testing.T) {
	t.Parallel()

	nodeAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	databaseAt := nodeAt.Add(2 * time.Hour)
	userID := model.NewUserID()
	token := personalAccessTokenForTest(userID, nodeAt)
	tokens := &personalAccessTokenStoreFake{current: token, preparedAt: databaseAt}
	sealer := mailTestSealer(t)
	composer, err := mail.NewComposer(mail.ComposerDependencies{Renderer: mailtest.NewRenderer(t), Sender: &mailSenderFake{enabled: true, from: mail.Address{Name: "Proctor", Address: "no-reply@example.test"}}, Sealer: sealer, Institution: nil})
	require.NoError(t, err)
	service, err := personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: &personalAccessTokenUserStoreFake{}, academicUnits: &personalAccessTokenAcademicUnitStoreFake{}, institutions: &personalAccessTokenInstitutionStoreFake{institution: &model.Institution{ID: model.NewInstitutionID()}}, securityAudit: newSecurityAuditLog(t, nil).auditor, authorizer: grantedAccess(t, nil).Authorizer, mail: composer, personalAccessTokenPolicy: PersonalAccessTokenPolicy{MinimumLifetime: time.Minute, MaximumLifetime: 24 * time.Hour, MaximumPerUser: 3}, recentAuthenticationTTL: 15 * time.Minute, newCredential: func() string { return "credential" }, now: func() time.Time { return nodeAt }})
	require.NoError(t, err)
	_, appErr := service.SetPersonalAccessTokenDisabled(context.Background(), invocation.New(personalAccessTokenSessionPrincipal(userID, nodeAt), model.RequestMetadata{}), SetPersonalAccessTokenDisabledCommand{TokenID: token.ID, Disabled: true})
	require.NoError(t, appErr)
	require.NotNil(t, tokens.notice.Delivery)
	require.NotEmpty(t, tokens.notice.Delivery.EncryptedPayload)
	payload, err := mail.OpenDelivery(sealer, tokens.notice.Delivery)
	require.NoError(t, err)
	require.Contains(t, payload.Text, "Disabled: Thursday 20 August 2026, 14:00 UTC", "rendered text %q does not contain PostgreSQL action time %s", payload.Text, databaseAt)
	require.NotContains(t, payload.Text, "Thursday 20 August 2026, 12:00 UTC", "rendered text %q shows the node clock %s", payload.Text, nodeAt)
}

func TestPersonalAccessTokenAdministrationTerminalReplayIsSuccessful(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	userID := model.NewUserID()
	token := personalAccessTokenForTest(userID, now)
	fresh := false
	tokens := &personalAccessTokenStoreFake{current: token, preparedAt: now, fresh: &fresh}
	service := mustPersonalAccessTokenAdministrationService(t, tokens, &personalAccessTokenAcademicUnitStoreFake{},
		&personalAccessTokenInstitutionStoreFake{institution: &model.Institution{ID: model.NewInstitutionID()}}, now)
	got, err := service.SetPersonalAccessTokenDisabled(context.Background(), invocation.New(personalAccessTokenSessionPrincipal(userID, now), model.RequestMetadata{}), SetPersonalAccessTokenDisabledCommand{TokenID: token.ID, Disabled: true})
	require.NoError(t, err, "SetDisabled(concurrent replay)")
	require.Same(t, token, got, "SetDisabled(concurrent replay)")
}

func TestPersonalAccessTokenAdministrationTerminalizesPreparationWhenRenderingFails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	userID := model.NewUserID()
	token := personalAccessTokenForTest(userID, now)
	tokens := &personalAccessTokenStoreFake{current: token, preparedAt: now}
	preparer, rendered := newTestMail(t, true)
	rendered.fail()
	service, err := personalAccessTokenTestService(&Service{personalAccessTokens: tokens, users: &personalAccessTokenUserStoreFake{}, academicUnits: &personalAccessTokenAcademicUnitStoreFake{}, institutions: &personalAccessTokenInstitutionStoreFake{institution: &model.Institution{ID: model.NewInstitutionID()}}, securityAudit: newSecurityAuditLog(t, nil).auditor, authorizer: grantedAccess(t, nil).Authorizer, mail: preparer, personalAccessTokenPolicy: PersonalAccessTokenPolicy{MinimumLifetime: time.Minute, MaximumLifetime: 24 * time.Hour, MaximumPerUser: 3}, recentAuthenticationTTL: 15 * time.Minute, newCredential: func() string { return "credential" }, now: func() time.Time { return now }})
	require.NoError(t, err)
	_, appErr := service.SetPersonalAccessTokenDisabled(context.Background(), invocation.New(personalAccessTokenSessionPrincipal(userID, now), model.RequestMetadata{}), SetPersonalAccessTokenDisabledCommand{TokenID: token.ID, Disabled: true})
	require.True(t, apperror.Is(appErr, "personal_access_token.unavailable"), "SetDisabled(render failure) error = %v", appErr)
	require.Equal(t, 1, tokens.failCalls, "render failure terminalizations")
	require.Equal(t, 0, tokens.stateCalls, "render failure terminalizations")
}

func personalAccessTokenSessionPrincipal(userID model.UserID, authenticatedAt time.Time) model.Principal {
	return model.Principal{
		UserID: userID, SessionID: model.NewSessionID(),
		CredentialID:   model.PrincipalCredentialID(model.NewSessionCredentialID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType:             model.SessionClientWeb, AuthenticatedAt: authenticatedAt,
	}
}

func personalAccessTokenForTest(userID model.UserID, at time.Time) *model.PersonalAccessToken {
	token := &model.PersonalAccessToken{
		UserID: userID, Description: "automation", TokenHash: model.HashToken("credential"),
		Scopes: []string{string(model.ActionClassView)}, ExpiresAt: at.Add(time.Hour),
	}
	token.PrepareCreate(model.NewPersonalAccessTokenID(), at)
	return token
}

type personalAccessTokenStoreFake struct {
	store.PersonalAccessTokenStore
	events       *[]string
	saveAt       time.Time
	preparedAt   time.Time
	fresh        *bool
	candidate    *model.PersonalAccessToken
	saved        *model.PersonalAccessToken
	current      *model.PersonalAccessToken
	maximum      int
	stateMaximum int
	stateAt      int64
	revokedAt    int64
	saveCalls    int
	stateCalls   int
	revokeCalls  int
	prepareCalls int
	failCalls    int
	notice       store.PersonalAccessTokenSecurityNotice
	// audits are the audit drafts the preparations carried, in order.
	audits []*model.AuditEvent
}

func (s *personalAccessTokenStoreFake) PrepareMutation(_ context.Context, input *store.PersonalAccessTokenMutationPreparation) (*store.PreparedPersonalAccessTokenMutation, error) {
	if s.events != nil {
		*s.events = append(*s.events, "prepare")
	}
	s.prepareCalls++
	s.audits = append(s.audits, input.AuditEvent)
	at := s.preparedAt
	if at.IsZero() {
		at = s.saveAt
	}
	if at.IsZero() && s.current != nil {
		at = s.current.CreatedAt
	}
	if at.IsZero() {
		at = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	}
	return &store.PreparedPersonalAccessTokenMutation{ID: model.NewPersonalAccessTokenPreparationID(), ActionAt: at, ExpiresAt: at.Add(input.Lifetime)}, nil
}

func (s *personalAccessTokenStoreFake) FailMutation(context.Context, *store.PersonalAccessTokenMutationFailure) error {
	s.failCalls++
	return nil
}

func (s *personalAccessTokenStoreFake) MaintainMutationPreparations(context.Context, int) (*store.PersonalAccessTokenPreparationMaintenanceResult, error) {
	return &store.PersonalAccessTokenPreparationMaintenanceResult{}, nil
}

func (s *personalAccessTokenStoreFake) Create(_ context.Context, input *store.PersonalAccessTokenCreationMutation) (*store.PersonalAccessTokenMutationResult, error) {
	if s.events != nil {
		*s.events = append(*s.events, "create")
	}
	s.saveCalls++
	s.candidate, s.maximum = input.Token, input.MaximumActive
	s.notice = input.Notice
	at := s.saveAt
	if at.IsZero() {
		at = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	}
	s.candidate.PrepareCreate(model.NewPersonalAccessTokenID(), at)
	s.saved = s.candidate
	return &store.PersonalAccessTokenMutationResult{Token: s.saved, Fresh: s.resultFresh()}, nil
}

func (s *personalAccessTokenStoreFake) Get(context.Context, model.PersonalAccessTokenID) (*model.PersonalAccessToken, error) {
	if s.current == nil {
		return nil, store.NewErrNotFound("personal_access_token", "")
	}
	return s.current, nil
}

func (s *personalAccessTokenStoreFake) ListByUser(context.Context, model.UserID) ([]*model.PersonalAccessToken, error) {
	return []*model.PersonalAccessToken{s.current}, nil
}

func (s *personalAccessTokenStoreFake) ChangeState(_ context.Context, input *store.PersonalAccessTokenStateMutation) (*store.PersonalAccessTokenMutationResult, error) {
	s.stateCalls++
	s.stateAt, s.stateMaximum = s.current.CreatedAt.UnixMilli(), input.MaximumActive
	s.notice = input.Notice
	if input.Disabled {
		s.current.DisabledAt = model.OptionalTimeFrom(s.current.CreatedAt)
	} else {
		s.current.DisabledAt = model.OptionalTime{}
	}
	return &store.PersonalAccessTokenMutationResult{Token: s.current, Fresh: s.resultFresh()}, nil
}

func (s *personalAccessTokenStoreFake) Revoke(_ context.Context, input *store.PersonalAccessTokenRevocation) (*store.PersonalAccessTokenMutationResult, error) {
	s.revokeCalls++
	s.notice = input.Notice
	s.revokedAt = s.current.CreatedAt.UnixMilli()
	s.current.RevokedAt = model.OptionalTimeFrom(s.current.CreatedAt)
	return &store.PersonalAccessTokenMutationResult{Token: s.current, Fresh: s.resultFresh()}, nil
}

func (s *personalAccessTokenStoreFake) resultFresh() bool {
	return s.fresh == nil || *s.fresh
}

type personalAccessTokenUserStoreFake struct{ store.UserStore }

func (*personalAccessTokenUserStoreFake) Get(_ context.Context, id model.UserID) (*model.User, error) {
	user := &model.User{
		ID: id, Username: "operator", Email: "operator@example.test",
		EmailVerified: true, DisplayName: "Operator", Locale: model.DefaultLocale,
	}
	user.PrepareCreate(id, time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC))
	return user, nil
}

type personalAccessTokenAcademicUnitStoreFake struct {
	store.AcademicUnitStore
	events *[]string
	unit   *model.AcademicUnit
}

func (s *personalAccessTokenAcademicUnitStoreFake) Get(context.Context, model.AcademicUnitID) (*model.AcademicUnit, error) {
	if s.events != nil {
		*s.events = append(*s.events, "academic_unit")
	}
	if s.unit == nil {
		return nil, store.NewErrNotFound("academic_unit", "")
	}
	return s.unit, nil
}

type personalAccessTokenInstitutionStoreFake struct {
	store.InstitutionStore
	events      *[]string
	institution *model.Institution
}

func (s *personalAccessTokenInstitutionStoreFake) GetSingleton(context.Context) (*model.Institution, error) {
	if s.events != nil {
		*s.events = append(*s.events, "institution")
	}
	if s.institution == nil {
		return nil, store.NewErrNotFound("institution", "")
	}
	return s.institution, nil
}
