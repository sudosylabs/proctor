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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type browserInvitationTransactionStoreFake struct {
	store.BrowserAuthenticationStore
	created         *store.BrowserInvitationTransactionCreation
	createdAt       time.Time
	expiresAt       time.Time
	createResult    *store.BrowserInvitationCreated
	createTransform func(*store.BrowserInvitationCreated)
	createErr       error
	createNil       bool
	resolveResult   *store.BrowserInvitationResolution
	resolveErr      error
	resolveNil      bool
	resolveCalls    int
}

func (s *browserInvitationTransactionStoreFake) CreateInvitation(_ context.Context, input *store.BrowserInvitationTransactionCreation) (*store.BrowserInvitationCreated, error) {
	copy := *input
	s.created = &copy
	if s.createNil {
		return nil, nil
	}
	if s.createResult != nil || s.createErr != nil {
		return s.createResult, s.createErr
	}
	createdAt := s.createdAt
	if createdAt.IsZero() {
		createdAt = model.NowUTC()
	}
	expiresAt := s.expiresAt
	if expiresAt.IsZero() {
		expiresAt = createdAt.Add(model.BrowserAuthenticationTransactionLifetime)
	}
	created := &store.BrowserInvitationCreated{ID: input.ID, ExpiresAt: expiresAt}
	if s.createTransform != nil {
		s.createTransform(created)
	}
	return created, nil
}

func (s *browserInvitationTransactionStoreFake) ResolveInvitation(context.Context, string, string) (*store.BrowserInvitationResolution, error) {
	s.resolveCalls++
	if s.resolveNil {
		return nil, nil
	}
	if s.resolveResult != nil || s.resolveErr != nil {
		return s.resolveResult, s.resolveErr
	}
	return nil, &store.ErrNotFound{Resource: "browser authentication transaction"}
}

type browserInvitationStoreFake struct {
	store.InvitationStore
	invitation *model.Invitation
}

func (s browserInvitationStoreFake) GetByClaimHash(context.Context, string) (*model.Invitation, error) {
	return s.invitation, nil
}

// withBrowserStores returns a copy of s that also holds the browser
// Invitation handoff's Store contracts and proof source.
func withBrowserStores(s *Service, transactions store.BrowserAuthenticationStore, institution *model.Institution, newProof func() string) *Service {
	browser := *s
	browser.browserAuthentication, browser.institutions = transactions, institutionStoreFake{institution: institution}
	browser.publicURL, browser.newToken = "https://proctor.example.edu", newProof
	return &browser
}

func TestBrowserInvitationPreservesPasswordWorkFailures(t *testing.T) {
	for _, purpose := range []model.InvitationPurpose{model.InvitationPurposeStudentClass, model.InvitationPurposeTeacherAcademicUnit} {
		t.Run(string(purpose), func(t *testing.T) {
			testPasswordHashFailures(t, "invitation.unavailable", func(t *testing.T, ctx context.Context, hasher testPasswordHasher) error {
				invitation := &model.Invitation{ID: model.NewInvitationID(), Purpose: purpose,
					ClaimHash: model.HashInvitationClaim(model.NewCredentialToken()), TargetEmail: "invited@example.edu"}
				persistence := &invitationStoreFake{invitation: invitation}
				mail := &mailFake{}
				invitations := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
					&authorizerFake{}, mail, time.Now())
				invitations.hasher = hasher
				transactions := &browserInvitationTransactionStoreFake{resolveResult: &store.BrowserInvitationResolution{
					ID: model.NewBrowserAuthenticationTransactionID(), InvitationID: invitation.ID, InvitationClaimHash: invitation.ClaimHash,
				}}
				service := withBrowserStores(invitations, transactions, &model.Institution{ID: model.NewInstitutionID()}, model.NewCredentialToken)
				result, err := service.AcceptBrowserInvitation(ctx, invocation.Invocation{}, BrowserInvitationAcceptanceCommand{
					Handle: model.NewCredentialToken(), BrowserProof: model.NewCredentialToken(), Username: "invited",
					Password: "correct horse battery staple", Source: "192.0.2.38",
				})
				require.Nil(t, result, "failed password work advanced hosted Invitation acceptance")
				require.Nil(t, persistence.accepted, "failed password work advanced hosted Invitation acceptance")
				require.Nil(t, persistence.teacherAccepted, "failed password work advanced hosted Invitation acceptance")
				require.Empty(t, mail.directJobType, "failed password work advanced hosted Invitation acceptance")
				return err
			})
		})
	}
}

func TestBrowserInvitationStartReplacesRawClaimWithTwoHashedProofs(t *testing.T) {
	t.Parallel()
	now := model.TimeUTC(time.Now())
	claim := model.NewCredentialToken()
	handle := model.NewCredentialToken()
	proof := model.NewCredentialToken()
	invitation := &model.Invitation{
		ID: model.NewInvitationID(), Purpose: model.InvitationPurposeStudentClass,
		State: model.InvitationPending, ClaimHash: model.HashInvitationClaim(claim),
		TargetEmail: "student.one@example.edu", ExpiresAt: now.Add(time.Minute),
	}
	attempts := newExpiringAuthenticationAttemptCache(time.Now)
	invitationService := &Service{invitations: browserInvitationStoreFake{invitation: invitation}, classes: invitationClassStoreFake{class: &model.Class{DisplayName: "Biology 101"}}, attempts: mustAuthenticationAttemptAccounting(t, attempts), rateLimit: testRateLimit}
	transactions := &browserInvitationTransactionStoreFake{createdAt: now, expiresAt: invitation.ExpiresAt}
	proofs := []string{handle, proof}
	service := withBrowserStores(invitationService, transactions, &model.Institution{ID: model.NewInstitutionID()}, func() string {
		value := proofs[0]
		proofs = proofs[1:]
		return value
	})
	started, err := service.StartBrowserInvitation(context.Background(), invocation.Invocation{}, StartBrowserInvitationCommand{Claim: claim, Source: "source"})
	require.NoError(t, err)
	require.Equal(t, handle, started.Handle, "start = %#v checks", started)
	require.Equal(t, proof, started.BrowserProof, "start = %#v checks", started)
	require.Equal(t, BrowserInvitationRequirementAccount, started.Requirement, "start = %#v checks", started)
	require.Equal(t, invitation.ExpiresAt.UnixMilli(), started.ExpiresAt, "start = %#v checks", started)
	require.Equal(t, 1, invitationAttemptChecks(attempts), "start = %#v checks", started)
	require.Equal(t, "student.one@example.edu", started.Email, "start = %#v", started)
	require.Equal(t, "Biology 101", started.ClassDisplayName, "start = %#v", started)
	created := transactions.created
	require.NotNil(t, created)
	require.Equal(t, invitation.ID, created.InvitationID)
	require.Equal(t, invitation.Purpose, created.InvitationPurpose)
	require.Equal(t, model.HashInvitationClaim(claim), created.InvitationClaimHash)
	require.Equal(t, model.HashToken(handle), created.HandleHash)
	require.Equal(t, model.HashToken(proof), created.BrowserProofHash)
	require.NotEqual(t, handle, created.HandleHash, "raw browser Invitation credential was persisted")
	require.NotEqual(t, proof, created.BrowserProofHash, "raw browser Invitation credential was persisted")
	require.NotEqual(t, claim, created.InvitationClaimHash, "raw browser Invitation credential was persisted")
}

func TestBrowserInvitationStoreNilResultsFailClosed(t *testing.T) {
	t.Parallel()
	now := model.TimeUTC(time.Now())
	claim := model.NewCredentialToken()
	invitation := &model.Invitation{
		ID: model.NewInvitationID(), Purpose: model.InvitationPurposeStudentClass,
		State: model.InvitationPending, ClaimHash: model.HashInvitationClaim(claim),
		ExpiresAt: now.Add(time.Minute),
	}
	invitationService := &Service{invitations: browserInvitationStoreFake{invitation: invitation}, classes: invitationClassStoreFake{class: &model.Class{DisplayName: "Biology 101"}}, attempts: testAttempts(t), rateLimit: testRateLimit}
	transactions := &browserInvitationTransactionStoreFake{createNil: true}
	proofs := []string{model.NewCredentialToken(), model.NewCredentialToken()}
	service := withBrowserStores(invitationService, transactions, &model.Institution{ID: model.NewInstitutionID()}, func() string {
		value := proofs[0]
		proofs = proofs[1:]
		return value
	})
	// An explicit nil result from an otherwise successful dependency must not
	// become a successful application response or a later nil dereference.
	_, err := service.StartBrowserInvitation(context.Background(), invocation.Invocation{}, StartBrowserInvitationCommand{Claim: claim, Source: "source"})
	require.True(t, apperror.Is(err, "invitation.unavailable"), "Start error = %v, want invitation.unavailable", err)

	transactions.createNil = false
	transactions.resolveNil = true
	_, _, err = service.resolveBrowserInvitation(context.Background(), model.NewCredentialToken(), model.NewCredentialToken())
	require.True(t, apperror.Is(err, "invitation.unavailable"), "resolve error = %v, want invitation.unavailable", err)
}

func TestBrowserInvitationMalformedStoreFactsFailClosed(t *testing.T) {
	t.Parallel()
	now := model.TimeUTC(time.Now())
	claim := model.NewCredentialToken()
	invitation := &model.Invitation{
		ID: model.NewInvitationID(), Purpose: model.InvitationPurposeStudentClass,
		State: model.InvitationPending, ClaimHash: model.HashInvitationClaim(claim), ExpiresAt: now.Add(time.Minute),
	}
	invitationService := &Service{invitations: browserInvitationStoreFake{invitation: invitation}, classes: invitationClassStoreFake{class: &model.Class{DisplayName: "Biology 101"}}, attempts: testAttempts(t), rateLimit: testRateLimit}
	for name, institution := range map[string]*model.Institution{
		"nil Institution":              nil,
		"invalid Institution identity": {},
	} {
		t.Run(name, func(t *testing.T) {
			service := withBrowserStores(invitationService, &browserInvitationTransactionStoreFake{}, institution, model.NewCredentialToken)
			_, err := service.StartBrowserInvitation(context.Background(), invocation.Invocation{}, StartBrowserInvitationCommand{Claim: claim, Source: "source"})
			require.True(t, apperror.Is(err, "invitation.unavailable"), "Start error = %v", err)
		})
	}
	newService := func(transactions *browserInvitationTransactionStoreFake) *Service {
		service := withBrowserStores(invitationService, transactions, &model.Institution{ID: model.NewInstitutionID()}, model.NewCredentialToken)
		return service
	}

	for name, result := range map[string]*store.BrowserInvitationCreated{
		"wrong identity": {ID: model.NewBrowserAuthenticationTransactionID(), ExpiresAt: now.Add(time.Second)},
		"zero expiry":    {ID: "placeholder"},
		"late expiry":    {ID: "placeholder", ExpiresAt: invitation.ExpiresAt.Add(time.Second)},
	} {
		t.Run(name, func(t *testing.T) {
			transactions := &browserInvitationTransactionStoreFake{createResult: result}
			service := newService(transactions)
			if result.ID == "placeholder" {
				// The created ID is unknown until the command reaches the fake;
				// make only the expiry malformed in these two cases.
				transactions.createResult = nil
				transactions.createTransform = func(created *store.BrowserInvitationCreated) {
					created.ExpiresAt = result.ExpiresAt
				}
			}
			_, err := service.StartBrowserInvitation(context.Background(), invocation.Invocation{}, StartBrowserInvitationCommand{Claim: claim, Source: "source"})
			require.True(t, apperror.Is(err, "invitation.unavailable"), "Start error = %v", err)
		})
	}

	for name, result := range map[string]*store.BrowserInvitationResolution{
		"invalid transaction": {InvitationID: invitation.ID, InvitationClaimHash: invitation.ClaimHash},
		"invalid Invitation":  {ID: model.NewBrowserAuthenticationTransactionID(), InvitationClaimHash: invitation.ClaimHash},
		"invalid claim": {ID: model.NewBrowserAuthenticationTransactionID(), InvitationID: invitation.ID,
			InvitationClaimHash: "invalid"},
		"mismatched Invitation": {ID: model.NewBrowserAuthenticationTransactionID(), InvitationID: model.NewInvitationID(),
			InvitationClaimHash: invitation.ClaimHash},
	} {
		t.Run("resolve "+name, func(t *testing.T) {
			service := newService(&browserInvitationTransactionStoreFake{resolveResult: result})
			_, _, err := service.resolveBrowserInvitation(context.Background(), model.NewCredentialToken(), model.NewCredentialToken())
			require.True(t, apperror.Is(err, "invitation.unavailable"), "resolve error = %v", err)
		})
	}
}

func TestBrowserInvitationSessionAcceptanceRequiresWebSession(t *testing.T) {
	t.Parallel()
	now := model.NowUTC()
	transactions := &browserInvitationTransactionStoreFake{}
	service := withBrowserStores(&Service{invitations: browserInvitationStoreFake{}, attempts: testAttempts(t), rateLimit: testRateLimit}, transactions, &model.Institution{ID: model.NewInstitutionID()}, model.NewCredentialToken)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, AuthenticatedAt: now,
	}
	_, err := service.AcceptBrowserInvitationWithSession(
		context.Background(),
		invocation.New(principal, model.RequestMetadata{}),
		BrowserInvitationSessionAcceptanceCommand{
			Handle: model.NewCredentialToken(), BrowserProof: model.NewCredentialToken(), Source: "source",
		},
	)
	require.True(t, apperror.Is(err, "invitation.invalid"), "AcceptSession error=%v resolve_calls", err)
	require.Equal(t, 0, transactions.resolveCalls, "AcceptSession error=%v resolve_calls", err)
}

type browserInvitationClassFailureFake struct {
	store.ClassStore
	err error
}

func (f browserInvitationClassFailureFake) Get(context.Context, model.ClassID) (*model.Class, error) {
	return nil, f.err
}

func TestBrowserInvitationStartNamesOnlyTheStudentClass(t *testing.T) {
	t.Parallel()
	now := model.TimeUTC(time.Now())
	for _, purpose := range []model.InvitationPurpose{model.InvitationPurposeTeacherAcademicUnit, model.InvitationPurposeAcademicUnitRole, model.InvitationPurposeInstitutionRole} {
		t.Run(string(purpose), func(t *testing.T) {
			claim := model.NewCredentialToken()
			invitation := &model.Invitation{ID: model.NewInvitationID(), Purpose: purpose, State: model.InvitationPending,
				ClaimHash: model.HashInvitationClaim(claim), TargetEmail: "staff@example.edu", ExpiresAt: now.Add(time.Minute),
				AcademicUnitID: model.NewAcademicUnitID(), RoleID: model.NewRoleID()}
			// A Class reader that fails proves non-student purposes never consult it.
			invitations := &Service{invitations: browserInvitationStoreFake{invitation: invitation},
				classes: browserInvitationClassFailureFake{err: errors.New("unexpected Class read")}, attempts: testAttempts(t), rateLimit: testRateLimit}
			service := withBrowserStores(invitations, &browserInvitationTransactionStoreFake{createdAt: now, expiresAt: invitation.ExpiresAt}, &model.Institution{ID: model.NewInstitutionID()}, model.NewCredentialToken)
			started, err := service.StartBrowserInvitation(context.Background(), invocation.Invocation{}, StartBrowserInvitationCommand{Claim: claim, Source: "source"})
			require.NoError(t, err)
			require.Equal(t, "staff@example.edu", started.Email)
			require.Empty(t, started.ClassDisplayName)
		})
	}
}

func TestBrowserInvitationStartClassFailuresCreateNoTransaction(t *testing.T) {
	t.Parallel()
	now := model.TimeUTC(time.Now())
	for name, test := range map[string]struct {
		classes store.ClassStore
		code    string
	}{
		"missing Class":     {classes: browserInvitationClassFailureFake{err: store.NewErrNotFound("class", "missing")}, code: "invitation.invalid"},
		"Class unavailable": {classes: browserInvitationClassFailureFake{err: errors.New("connection reset")}, code: "invitation.unavailable"},
		"nil Class":         {classes: invitationClassStoreFake{}, code: "invitation.invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			claim := model.NewCredentialToken()
			invitation := &model.Invitation{ID: model.NewInvitationID(), Purpose: model.InvitationPurposeStudentClass, State: model.InvitationPending,
				ClaimHash: model.HashInvitationClaim(claim), TargetEmail: "student@example.edu", ExpiresAt: now.Add(time.Minute)}
			transactions := &browserInvitationTransactionStoreFake{}
			invitations := &Service{invitations: browserInvitationStoreFake{invitation: invitation},
				classes: test.classes, attempts: testAttempts(t), rateLimit: testRateLimit}
			service := withBrowserStores(invitations, transactions, &model.Institution{ID: model.NewInstitutionID()}, model.NewCredentialToken)
			_, err := service.StartBrowserInvitation(context.Background(), invocation.Invocation{}, StartBrowserInvitationCommand{Claim: claim, Source: "source"})
			require.True(t, apperror.Is(err, test.code), "Start error = %v, want %s", err, test.code)
			require.Nil(t, transactions.created, "a failed Class read created a browser transaction")
		})
	}
}
