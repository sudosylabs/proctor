// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type StartBrowserInvitationCommand struct {
	Claim  string
	Source string
}

type BrowserInvitationRequirement string

const (
	BrowserInvitationRequirementAccount BrowserInvitationRequirement = "account"
	BrowserInvitationRequirementSession BrowserInvitationRequirement = "session"
)

type BrowserInvitationStart struct {
	Handle       string
	BrowserProof string
	Purpose      model.InvitationPurpose
	Requirement  BrowserInvitationRequirement
	ExpiresAt    int64
	// Email is the Invitation's normalized target address, the mailbox the
	// claim was delivered to. Acceptance never takes an address from the browser.
	Email string
	// ClassDisplayName names the Class of a student Class Invitation and is
	// empty for every other purpose. Academic Unit and Role names are never
	// presented, matching the Invitation and membership mail.
	ClassDisplayName string
}

type BrowserInvitationAcceptanceCommand struct {
	Handle, BrowserProof, Password, Username, DisplayName, FirstName, LastName, Locale, Timezone, Source string
}

type BrowserInvitationSessionAcceptanceCommand struct {
	Handle, BrowserProof, Source string
}

// StartBrowserInvitation exchanges a raw Invitation claim for a hashed browser handle
// and proof bound to the pending Invitation, after counting the claim attempt.
func (s *Service) StartBrowserInvitation(
	ctx context.Context,
	_ invocation.Invocation,
	command StartBrowserInvitationCommand,
) (*BrowserInvitationStart, error) {
	if !model.IsValidCredentialToken(command.Claim) {
		return nil, apperror.New("invitation.invalid")
	}

	claimHash := model.HashInvitationClaim(command.Claim)
	if err := s.checkInvitationAttempt(ctx, claimHash, command.Source); err != nil {
		return nil, err
	}

	invitation, err := s.invitations.GetByClaimHash(ctx, claimHash)
	if err != nil || invitation == nil {
		return nil, invalidInvitationError(err)
	}
	if invitation.State != model.InvitationPending {
		return nil, apperror.New("invitation.invalid")
	}

	var requirement BrowserInvitationRequirement
	switch invitation.Purpose {
	case model.InvitationPurposeStudentClass, model.InvitationPurposeTeacherAcademicUnit:
		requirement = BrowserInvitationRequirementAccount
	case model.InvitationPurposeAcademicUnitRole, model.InvitationPurposeInstitutionRole:
		requirement = BrowserInvitationRequirementSession
	default:
		return nil, apperror.New("invitation.invalid")
	}

	var classDisplayName string
	if invitation.Purpose == model.InvitationPurposeStudentClass {
		class, classErr := s.classes.Get(ctx, invitation.ClassID)
		if classErr != nil && !store.IsNotFound(classErr) {
			return nil, apperror.New("invitation.unavailable").Wrap(classErr)
		}
		if class == nil {
			return nil, invalidInvitationError(classErr)
		}

		classDisplayName = class.DisplayName
	}

	institution, err := s.institutions.GetSingleton(ctx)
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return nil, apperror.New("invitation.unavailable").Wrap(err)
	}

	handle, proof := s.newToken(), s.newToken()
	if !model.IsValidCredentialToken(handle) || !model.IsValidCredentialToken(proof) || handle == proof {
		return nil, apperror.New("invitation.unavailable")
	}

	creation := &store.BrowserInvitationTransactionCreation{
		ID:                  model.NewBrowserAuthenticationTransactionID(),
		InstitutionID:       institution.ID,
		Issuer:              s.publicURL,
		InvitationID:        invitation.ID,
		InvitationPurpose:   invitation.Purpose,
		InvitationClaimHash: claimHash,
		HandleHash:          model.HashToken(handle),
		BrowserProofHash:    model.HashToken(proof),
	}
	saved, err := s.browserAuthentication.CreateInvitation(ctx, creation)
	if err != nil || saved == nil {
		return nil, browserInvitationStoreErrors.Translate(err)
	}

	if saved.ID != creation.ID ||
		saved.ExpiresAt.IsZero() ||
		saved.ExpiresAt.After(invitation.ExpiresAt) ||
		(invitation.IntendedEndsAt.Valid && saved.ExpiresAt.After(invitation.IntendedEndsAt.Time)) {
		return nil, apperror.New("invitation.unavailable")
	}

	return &BrowserInvitationStart{
		Handle:           handle,
		BrowserProof:     proof,
		Purpose:          invitation.Purpose,
		Requirement:      requirement,
		ExpiresAt:        saved.ExpiresAt.UnixMilli(),
		Email:            invitation.TargetEmail,
		ClassDisplayName: classDisplayName,
	}, nil
}

// AcceptBrowserInvitation accepts a student Class or teacher Academic Unit Invitation
// through its browser handle and proof, creating the local account.
func (s *Service) AcceptBrowserInvitation(
	ctx context.Context,
	invocation invocation.Invocation,
	command BrowserInvitationAcceptanceCommand,
) (*InvitationAcceptanceView, error) {
	resolution, invitation, err := s.resolveBrowserInvitation(ctx, command.Handle, command.BrowserProof)
	if err != nil {
		return nil, err
	}
	if err = s.checkInvitationAttempt(ctx, model.HashToken(command.Handle), command.Source); err != nil {
		return nil, err
	}

	base := AcceptStudentClassInvitationCommand{
		Password:           command.Password,
		Username:           command.Username,
		DisplayName:        command.DisplayName,
		FirstName:          command.FirstName,
		LastName:           command.LastName,
		Locale:             command.Locale,
		Timezone:           command.Timezone,
		Source:             command.Source,
		browserTransaction: browserInvitationProof(resolution, command.Handle, command.BrowserProof),
	}
	var accepted *InvitationAcceptanceView
	switch invitation.Purpose {
	case model.InvitationPurposeStudentClass:
		accepted, err = s.acceptStudentClassByClaimHash(ctx, invocation, base, resolution.InvitationClaimHash)
	case model.InvitationPurposeTeacherAcademicUnit:
		accepted, err = s.acceptTeacherAcademicUnitByClaimHash(
			ctx,
			invocation,
			AcceptTeacherAcademicUnitInvitationCommand(base),
			resolution.InvitationClaimHash,
		)
	default:
		return nil, apperror.New("invitation.invalid")
	}

	if err != nil {
		return nil, err
	}

	return accepted, nil
}

// AcceptBrowserInvitationWithSession accepts a scoped Role Invitation through its browser
// handle and proof for the caller's web Session.
func (s *Service) AcceptBrowserInvitationWithSession(
	ctx context.Context,
	invocation invocation.Invocation,
	command BrowserInvitationSessionAcceptanceCommand,
) (*InvitationAcceptanceView, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil ||
		principal.CredentialType != model.CredentialSessionAccess ||
		principal.ClientType != model.SessionClientWeb {
		return nil, apperror.New("invitation.invalid")
	}

	resolution, invitation, err := s.resolveBrowserInvitation(ctx, command.Handle, command.BrowserProof)
	if err != nil {
		return nil, err
	}
	if err = s.checkInvitationAttempt(ctx, model.HashToken(command.Handle), command.Source); err != nil {
		return nil, err
	}
	if invitation.Purpose != model.InvitationPurposeAcademicUnitRole && invitation.Purpose != model.InvitationPurposeInstitutionRole {
		return nil, apperror.New("invitation.invalid")
	}

	accepted, err := s.acceptScopedRoleByClaimHash(
		ctx,
		invocation,
		resolution.InvitationClaimHash,
		invitation.Purpose,
		browserInvitationProof(resolution, command.Handle, command.BrowserProof),
	)
	if err != nil {
		return nil, err
	}

	return accepted, nil
}

func (s *Service) resolveBrowserInvitation(
	ctx context.Context,
	handle, proof string,
) (*store.BrowserInvitationResolution, *model.Invitation, error) {
	if !model.IsValidCredentialToken(handle) || !model.IsValidCredentialToken(proof) {
		return nil, nil, apperror.New("invitation.invalid")
	}

	handleHash, proofHash := model.HashToken(handle), model.HashToken(proof)
	resolution, err := s.browserAuthentication.ResolveInvitation(ctx, handleHash, proofHash)
	if err != nil || resolution == nil {
		return nil, nil, browserInvitationStoreErrors.Translate(err)
	}
	if !resolution.ID.IsValid() || !resolution.InvitationID.IsValid() || !model.IsValidTokenHash(resolution.InvitationClaimHash) {
		return nil, nil, apperror.New("invitation.unavailable")
	}

	invitation, err := s.invitations.GetByClaimHash(ctx, resolution.InvitationClaimHash)
	if err != nil || invitation == nil || invitation.ID != resolution.InvitationID {
		return nil, nil, browserInvitationStoreErrors.Translate(err)
	}

	return resolution, invitation, nil
}

func browserInvitationProof(resolution *store.BrowserInvitationResolution, handle, proof string) *store.BrowserInvitationTransactionProof {
	return &store.BrowserInvitationTransactionProof{
		ID:               resolution.ID,
		HandleHash:       model.HashToken(handle),
		BrowserProofHash: model.HashToken(proof),
	}
}

var browserInvitationStoreErrors = apperror.StoreTable{
	NotFound:    "invitation.invalid",
	Conflict:    "invitation.invalid",
	Unavailable: "invitation.unavailable",
}
