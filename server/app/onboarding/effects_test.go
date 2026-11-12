// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// importTransitionStoreFake answers every state transition with value, or
// fails it with err, and records the order of its calls.
type importTransitionStoreFake struct {
	store.OnboardingImportStore
	events *[]string
	value  *store.OnboardingImport
	err    error
}

func (f importTransitionStoreFake) transition(name string) (*store.OnboardingImport, error) {
	*f.events = append(*f.events, name)
	if f.err != nil {
		return nil, f.err
	}
	return f.value, nil
}

func (f importTransitionStoreFake) CreateOnboardingImport(context.Context, *store.OnboardingImportCreation) (*store.OnboardingImport, error) {
	return f.transition("create")
}

func (f importTransitionStoreFake) CompleteOnboardingImportPreview(context.Context, *store.OnboardingImportPreviewCompletion) (*store.OnboardingImport, error) {
	return f.transition("complete preview")
}

func (f importTransitionStoreFake) CommitOnboardingImport(context.Context, *store.OnboardingImportCommit) (*store.OnboardingImport, error) {
	return f.transition("commit")
}

func (f importTransitionStoreFake) CompleteOnboardingImportRow(context.Context, *store.OnboardingImportRowCompletion) (*store.OnboardingImport, error) {
	return f.transition("complete row")
}

func (f importTransitionStoreFake) FinishOnboardingImport(context.Context, *store.OnboardingImportFinish) (*store.OnboardingImport, error) {
	return f.transition("finish")
}

func (f importTransitionStoreFake) CancelOnboardingImport(context.Context, *store.OnboardingImportCancellation) (*store.OnboardingImport, error) {
	return f.transition("cancel")
}

func (f importTransitionStoreFake) FailOnboardingImport(context.Context, *store.OnboardingImportFailure) (*store.OnboardingImport, error) {
	return f.transition("fail")
}

type importScopeClassesFake struct {
	store.ClassStore
	unitID model.AcademicUnitID
}

func (f importScopeClassesFake) GetAcademicUnitID(context.Context, model.ClassID) (model.AcademicUnitID, error) {
	return f.unitID, nil
}

type importScopeUnitsFake struct {
	store.AcademicUnitStore
	ancestors []*model.AcademicUnit
	err       error
}

func (f importScopeUnitsFake) ListAncestors(context.Context, model.AcademicUnitID) ([]*model.AcademicUnit, error) {
	return f.ancestors, f.err
}

// importEffectsFixture is a Class in an Academic Unit under a root unit, and
// a sibling unit beside it whose subscribers must hear nothing.
type importEffectsFixture struct {
	institutionID model.InstitutionID
	rootID        model.AcademicUnitID
	unitID        model.AcademicUnitID
	siblingID     model.AcademicUnitID
	classID       model.ClassID
	events        []string
	publisher     *realtimetest.Publisher
	service       *Service
}

func newImportEffectsFixture() *importEffectsFixture {
	f := &importEffectsFixture{
		institutionID: model.NewInstitutionID(), rootID: model.NewAcademicUnitID(), unitID: model.NewAcademicUnitID(),
		siblingID: model.NewAcademicUnitID(), classID: model.NewClassID(),
	}
	f.publisher = &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		f.events = append(f.events, string(call.Method)+" "+call.Event.Name)
	}}
	f.service = &Service{
		classes: importScopeClassesFake{unitID: f.unitID},
		academicUnits: importScopeUnitsFake{ancestors: []*model.AcademicUnit{
			{ID: f.unitID, InstitutionID: f.institutionID, ParentID: f.rootID},
			{ID: f.rootID, InstitutionID: f.institutionID},
		}},
		publisher: f.publisher,
	}
	return f
}

func (f *importEffectsFixture) imports(value *store.OnboardingImport, err error) announcingImports {
	return announcingImports{
		OnboardingImportStore: importTransitionStoreFake{events: &f.events, value: value, err: err},
		service:               f.service,
	}
}

func importChanged(t *testing.T, value *store.OnboardingImport, scope model.Resource) realtimetest.Call {
	t.Helper()
	event, err := realtime.NewOnboardingImportChangedEvent(value.ID, value.State, scope)
	require.NoError(t, err)
	return realtimetest.Call{Method: realtimetest.Publish, Event: event}
}

func TestImportTransitionsAnnounceToTheImportScopeAndEveryEnclosingScope(t *testing.T) {
	t.Parallel()

	f := newImportEffectsFixture()
	value := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentClass,
		State: model.OnboardingImportExecuting, ScopeType: model.RoleScopeClass, ScopeID: f.classID.String()}

	committed, err := f.imports(value, nil).CommitOnboardingImport(context.Background(), &store.OnboardingImportCommit{})

	require.NoError(t, err)
	require.Same(t, value, committed)
	// The sibling unit is in no published scope, so its subscribers hear
	// nothing of this import.
	require.Equal(t, []realtimetest.Call{
		importChanged(t, value, model.Resource{Type: model.ResourceClass, ID: f.classID.String()}),
		importChanged(t, value, model.Resource{Type: model.ResourceAcademicUnit, ID: f.unitID.String()}),
		importChanged(t, value, model.Resource{Type: model.ResourceAcademicUnit, ID: f.rootID.String()}),
		importChanged(t, value, model.Resource{Type: model.ResourceInstitution, ID: f.institutionID.String()}),
	}, f.publisher.Calls())
	require.Equal(t, "commit", f.events[0], "the event follows the committed transition")
}

func TestEveryImportTransitionAnnouncesOnceAfterItCommits(t *testing.T) {
	t.Parallel()

	transitions := map[string]func(context.Context, announcingImports) (*store.OnboardingImport, error){
		"create": func(ctx context.Context, imports announcingImports) (*store.OnboardingImport, error) {
			return imports.CreateOnboardingImport(ctx, &store.OnboardingImportCreation{})
		},
		"complete preview": func(ctx context.Context, imports announcingImports) (*store.OnboardingImport, error) {
			return imports.CompleteOnboardingImportPreview(ctx, &store.OnboardingImportPreviewCompletion{})
		},
		"commit": func(ctx context.Context, imports announcingImports) (*store.OnboardingImport, error) {
			return imports.CommitOnboardingImport(ctx, &store.OnboardingImportCommit{})
		},
		"finish": func(ctx context.Context, imports announcingImports) (*store.OnboardingImport, error) {
			return imports.FinishOnboardingImport(ctx, &store.OnboardingImportFinish{})
		},
		"cancel": func(ctx context.Context, imports announcingImports) (*store.OnboardingImport, error) {
			return imports.CancelOnboardingImport(ctx, &store.OnboardingImportCancellation{})
		},
		"fail": func(ctx context.Context, imports announcingImports) (*store.OnboardingImport, error) {
			return imports.FailOnboardingImport(ctx, &store.OnboardingImportFailure{})
		},
	}
	for name, transition := range transitions {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newImportEffectsFixture()
			value := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportInstitution,
				State: model.OnboardingImportCompleted, ScopeType: model.RoleScopeInstitution, ScopeID: f.institutionID.String()}

			_, err := transition(context.Background(), f.imports(value, nil))
			require.NoError(t, err)
			require.Equal(t, []string{name, "Publish onboarding_import.changed"}, f.events)
			require.Equal(t, []realtimetest.Call{
				importChanged(t, value, model.Resource{Type: model.ResourceInstitution, ID: f.institutionID.String()}),
			}, f.publisher.Calls())

			failed := newImportEffectsFixture()
			refused := errors.New("revision conflict")
			_, err = transition(context.Background(), failed.imports(value, refused))
			require.ErrorIs(t, err, refused)
			require.Equal(t, []string{name}, failed.events, "a refused transition announces nothing")
		})
	}
}

func TestImportEffectsAnnounceNeitherRowsNorStudentProgressions(t *testing.T) {
	t.Parallel()

	f := newImportEffectsFixture()
	value := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentClass,
		State: model.OnboardingImportExecuting, ScopeType: model.RoleScopeClass, ScopeID: f.classID.String()}
	_, err := f.imports(value, nil).CompleteOnboardingImportRow(context.Background(), &store.OnboardingImportRowCompletion{})
	require.NoError(t, err)

	progression := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentProgression,
		State: model.OnboardingImportPreviewReady, ScopeType: model.RoleScopeClass, ScopeID: f.classID.String()}
	_, err = f.imports(progression, nil).CompleteOnboardingImportPreview(context.Background(), &store.OnboardingImportPreviewCompletion{})
	require.NoError(t, err)

	require.Equal(t, []string{"complete row", "complete preview"}, f.events)
}

func TestImportEffectsStillTellTheOwnScopeWhenEnclosingScopesCannotBeRead(t *testing.T) {
	t.Parallel()

	f := newImportEffectsFixture()
	unavailable := errors.New("database unavailable")
	f.service.academicUnits = importScopeUnitsFake{err: unavailable}
	value := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportTeacherAcademicUnit,
		State: model.OnboardingImportParsing, ScopeType: model.RoleScopeAcademicUnit, ScopeID: f.unitID.String()}

	_, err := f.imports(value, nil).CreateOnboardingImport(context.Background(), &store.OnboardingImportCreation{})

	require.NoError(t, err, "a failed effect never fails the committed transition")
	require.Equal(t, []realtimetest.Call{
		importChanged(t, value, model.Resource{Type: model.ResourceAcademicUnit, ID: f.unitID.String()}),
		{Method: realtimetest.Report, Operation: "onboarding_import.changed", Err: unavailable},
	}, f.publisher.Calls())
}

func TestScopedRoleAcceptanceTellsOnlyTheInviter(t *testing.T) {
	t.Parallel()

	now := model.TimeFromMillis(1_800_000_000_000)
	unitID, inviterID, userID := model.NewAcademicUnitID(), model.NewUserID(), model.NewUserID()
	raw := model.NewCredentialToken()
	invitation, err := model.NewScopedRoleInvitation(model.ScopedRoleInvitationInput{
		ID: model.NewInvitationID(), Purpose: model.InvitationPurposeAcademicUnitRole, TargetEmail: "invited@example.edu",
		AcademicUnitID: unitID, RoleID: model.NewRoleID(), RoleActions: []string{string(model.ActionAcademicUnitView)},
		IntendedStartsAt: now, InviterUserID: inviterID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
		ClaimHash: model.HashInvitationClaim(raw), IssuedAt: now,
	})
	require.NoError(t, err)
	accept := func(persistence store.InvitationStore) (*realtimetest.Publisher, error) {
		service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
			&authorizerFake{}, &mailFake{}, now.Add(time.Minute))
		publisher := &realtimetest.Publisher{}
		service.publisher = publisher
		principal := model.Principal{UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
			CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
			AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: now}
		_, acceptErr := service.AcceptAcademicUnitRoleInvitation(context.Background(), invocation.New(principal, model.RequestMetadata{}),
			AcceptAcademicUnitRoleInvitationCommand{Claim: raw, Source: "127.0.0.1"})
		return publisher, acceptErr
	}

	publisher, err := accept(&invitationStoreFake{invitation: invitation})
	require.NoError(t, err)
	changed, err := realtime.NewInvitationChangedEvent(inviterID, invitation.ID, model.InvitationAccepted)
	require.NoError(t, err)
	require.Equal(t, []realtimetest.Call{
		{Method: realtimetest.Publish, Event: changed},
		{Method: realtimetest.InvalidateAuthorization, UserID: userID},
	}, publisher.Calls(), "the inviter hears once, and the accepting User only of their new authority")

	publisher, err = accept(&replayedScopedRoleStoreFake{invitationStoreFake: &invitationStoreFake{invitation: invitation}})
	require.NoError(t, err)
	require.Empty(t, publisher.Calls(), "a replayed acceptance announces nothing again")

	publisher, err = accept(&invitationStoreFake{invitation: invitation,
		scopedAcceptErr: store.NewErrConflict("invitation", "invitation_role_binding_conflict", nil)})
	require.Error(t, err)
	require.Empty(t, publisher.Calls(), "a refused acceptance announces nothing")
}

type replayedScopedRoleStoreFake struct {
	*invitationStoreFake
}

func (f *replayedScopedRoleStoreFake) AcceptScopedRole(
	ctx context.Context,
	input *store.ScopedRoleInvitationAcceptance,
) (*store.ScopedRoleInvitationAcceptanceResult, error) {
	result, err := f.invitationStoreFake.AcceptScopedRole(ctx, input)
	if err != nil {
		return nil, err
	}
	result.Replayed = true
	return result, nil
}
