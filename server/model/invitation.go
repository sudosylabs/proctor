// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Invitation lifetimes are whole days the inviter picks within these bounds.
// InvitationLifetime is the default, and the fixed lifetime of batch and
// onboarding import invitations.
const (
	InvitationLifetime        = 7 * 24 * time.Hour
	MinimumInvitationLifetime = 24 * time.Hour
	MaximumInvitationLifetime = 30 * 24 * time.Hour
)

// IsValidInvitationLifetime reports whether lifetime lies within the inclusive
// bounds.
func IsValidInvitationLifetime(lifetime time.Duration) bool {
	return lifetime >= MinimumInvitationLifetime && lifetime <= MaximumInvitationLifetime
}

func invitationLifetimeOrDefault(lifetime time.Duration) time.Duration {
	if lifetime == 0 {
		return InvitationLifetime
	}
	return lifetime
}

const invitationClaimHashDomain = "proctor.invitation_claim\x00"

// InvitationPurpose is the relationship package an invitation carries. Each
// purpose has its own application command; there is no empty or arbitrary
// package.
type InvitationPurpose string

const (
	InvitationPurposeStudentClass        InvitationPurpose = "student_class"
	InvitationPurposeTeacherAcademicUnit InvitationPurpose = "teacher_academic_unit"
	InvitationPurposeAcademicUnitRole    InvitationPurpose = "academic_unit_role"
	InvitationPurposeInstitutionRole     InvitationPurpose = "institution_role"
)

// InvitationState is the closed lifecycle of an invitation, which exists before
// its user does.
type InvitationState string

const (
	InvitationPending    InvitationState = "pending"
	InvitationAccepted   InvitationState = "accepted"
	InvitationRevoked    InvitationState = "revoked"
	InvitationExpired    InvitationState = "expired"
	InvitationSuperseded InvitationState = "superseded"
)

// InvitationProfileSuggestions are bounded profile defaults. Acceptance may use
// them to create an account, but they never identify a person or grant a
// relationship.
type InvitationProfileSuggestions struct {
	Username    string
	DisplayName string
	FirstName   string
	LastName    string
	Locale      string
	Timezone    string
}

// Invitation is the stored claim credential and its frozen relationship
// package. ClaimHash is never in JSON; raw claims never enter this model or
// storage.
type Invitation struct {
	ID                           InvitationID
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
	Revision                     int64
	Purpose                      InvitationPurpose
	State                        InvitationState
	TargetEmail                  string
	ClassID                      ClassID
	AcademicPeriodID             AcademicPeriodID
	AcademicUnitID               AcademicUnitID
	RoleID                       RoleID
	RoleActions                  []string
	IntendedStartsAt             time.Time
	IntendedEndsAt               OptionalTime
	Suggestions                  InvitationProfileSuggestions
	InviterUserID                UserID
	ScopeType                    RoleScopeType
	ScopeID                      string
	ClaimHash                    string `json:"-"`
	ExpiresAt                    time.Time
	AcceptedAt                   OptionalTime
	AcceptedUserID               UserID
	AcceptedAffiliationID        AffiliationID
	AcceptedClassMemberID        ClassMemberID
	AcceptedAcademicUnitMemberID AcademicUnitMemberID
	AcceptedRoleBindingID        RoleBindingID
}

// StudentClassInvitationInput is the package app chose after authorization and
// resolving the class.
type StudentClassInvitationInput struct {
	ID               InvitationID
	TargetEmail      string
	ClassID          ClassID
	AcademicPeriodID AcademicPeriodID
	IntendedStartsAt time.Time
	IntendedEndsAt   OptionalTime
	Suggestions      InvitationProfileSuggestions
	InviterUserID    UserID
	ScopeType        RoleScopeType
	ScopeID          string
	ClaimHash        string
	IssuedAt         time.Time
	// Lifetime bounds the claim; zero selects InvitationLifetime.
	Lifetime time.Duration
}

// TeacherAcademicUnitInvitationInput freezes the exact unit and delegable role
// package an authorized inviter chose.
type TeacherAcademicUnitInvitationInput struct {
	ID               InvitationID
	TargetEmail      string
	AcademicUnitID   AcademicUnitID
	RoleID           RoleID
	RoleActions      []string
	IntendedStartsAt time.Time
	IntendedEndsAt   OptionalTime
	Suggestions      InvitationProfileSuggestions
	InviterUserID    UserID
	ScopeType        RoleScopeType
	ScopeID          string
	ClaimHash        string
	IssuedAt         time.Time
	// Lifetime bounds the claim; zero selects InvitationLifetime.
	Lifetime time.Duration
}

// ScopedRoleInvitationInput freezes one role assignment for an existing user,
// at one academic unit or the institution.
type ScopedRoleInvitationInput struct {
	ID               InvitationID
	Purpose          InvitationPurpose
	TargetEmail      string
	AcademicUnitID   AcademicUnitID
	RoleID           RoleID
	RoleActions      []string
	IntendedStartsAt time.Time
	IntendedEndsAt   OptionalTime
	InviterUserID    UserID
	ScopeType        RoleScopeType
	ScopeID          string
	ClaimHash        string
	IssuedAt         time.Time
	// Lifetime bounds the claim; zero selects InvitationLifetime.
	Lifetime time.Duration
}

// NewStudentClassInvitation builds a student class invitation. Expiry is
// derived here from the bounded lifetime so callers cannot widen it by
// accident.
func NewStudentClassInvitation(input StudentClassInvitationInput) (*Invitation, error) {
	issuedAt := TimeUTC(input.IssuedAt)
	invitation := &Invitation{
		ID:               input.ID,
		CreatedAt:        issuedAt,
		UpdatedAt:        issuedAt,
		Revision:         1,
		Purpose:          InvitationPurposeStudentClass,
		State:            InvitationPending,
		TargetEmail:      normalizeInvitationEmail(input.TargetEmail),
		ClassID:          input.ClassID,
		AcademicPeriodID: input.AcademicPeriodID,
		IntendedStartsAt: TimeUTC(input.IntendedStartsAt),
		IntendedEndsAt:   normalizeOptionalInvitationTime(input.IntendedEndsAt),
		Suggestions:      normalizeInvitationSuggestions(input.Suggestions),
		InviterUserID:    input.InviterUserID,
		ScopeType:        input.ScopeType,
		ScopeID:          input.ScopeID,
		ClaimHash:        input.ClaimHash,
		ExpiresAt:        issuedAt.Add(invitationLifetimeOrDefault(input.Lifetime)),
	}
	if err := invitation.Validate(); err != nil {
		return nil, err
	}

	return invitation, nil
}

// NewTeacherAcademicUnitInvitation builds one teacher package. RoleActions is
// copied and canonicalized so later role changes cannot broaden an unclaimed
// invitation.
func NewTeacherAcademicUnitInvitation(input TeacherAcademicUnitInvitationInput) (*Invitation, error) {
	issuedAt := TimeUTC(input.IssuedAt)
	invitation := &Invitation{
		ID:               input.ID,
		CreatedAt:        issuedAt,
		UpdatedAt:        issuedAt,
		Revision:         1,
		Purpose:          InvitationPurposeTeacherAcademicUnit,
		State:            InvitationPending,
		TargetEmail:      normalizeInvitationEmail(input.TargetEmail),
		AcademicUnitID:   input.AcademicUnitID,
		RoleID:           input.RoleID,
		RoleActions:      canonicalInvitationRoleActions(input.RoleActions),
		IntendedStartsAt: TimeUTC(input.IntendedStartsAt),
		IntendedEndsAt:   normalizeOptionalInvitationTime(input.IntendedEndsAt),
		Suggestions:      normalizeInvitationSuggestions(input.Suggestions),
		InviterUserID:    input.InviterUserID,
		ScopeType:        input.ScopeType,
		ScopeID:          input.ScopeID,
		ClaimHash:        input.ClaimHash,
		ExpiresAt:        issuedAt.Add(invitationLifetimeOrDefault(input.Lifetime)),
	}
	if err := invitation.Validate(); err != nil {
		return nil, err
	}

	return invitation, nil
}

// NewScopedRoleInvitation builds a role-only package for an existing user. The
// claim proves control of the invited mailbox; acceptance never changes the
// user's email or profile.
func NewScopedRoleInvitation(input ScopedRoleInvitationInput) (*Invitation, error) {
	issuedAt := TimeUTC(input.IssuedAt)
	invitation := &Invitation{
		ID:               input.ID,
		CreatedAt:        issuedAt,
		UpdatedAt:        issuedAt,
		Revision:         1,
		Purpose:          input.Purpose,
		State:            InvitationPending,
		TargetEmail:      normalizeInvitationEmail(input.TargetEmail),
		AcademicUnitID:   input.AcademicUnitID,
		RoleID:           input.RoleID,
		RoleActions:      canonicalInvitationRoleActions(input.RoleActions),
		IntendedStartsAt: TimeUTC(input.IntendedStartsAt),
		IntendedEndsAt:   normalizeOptionalInvitationTime(input.IntendedEndsAt),
		InviterUserID:    input.InviterUserID,
		ScopeType:        input.ScopeType,
		ScopeID:          input.ScopeID,
		ClaimHash:        input.ClaimHash,
		ExpiresAt:        issuedAt.Add(invitationLifetimeOrDefault(input.Lifetime)),
	}
	if err := invitation.Validate(); err != nil {
		return nil, err
	}

	return invitation, nil
}

// HashInvitationClaim returns the domain-separated stored digest of a random
// claim. Never use it for human-entered secrets.
func HashInvitationClaim(rawClaim string) string {
	sum := sha256.Sum256([]byte(invitationClaimHashDomain + rawClaim))
	return hex.EncodeToString(sum[:])
}

// Validate checks the invitation's fields and its frozen package, for new and
// stored values alike.
func (i *Invitation) Validate() error {
	const where = "Invitation.Validate"
	if i == nil {
		return invalidModelError(where, "invitation", "value", "is required", "")
	}
	if !i.ID.IsValid() {
		return invalidModelError(where, "invitation", "id", "must be a valid identifier", "")
	}

	details := "id=" + i.ID.String()
	if i.CreatedAt.IsZero() || i.UpdatedAt.IsZero() || i.UpdatedAt.Before(i.CreatedAt) {
		return invalidModelError(where, "invitation", "timestamps", "must be ordered and set", details)
	}
	if i.Revision < 1 {
		return invalidModelError(where, "invitation", "revision", "must be positive", details)
	}
	if !i.Purpose.IsValid() {
		return invalidModelError(where, "invitation", "purpose", "has an unknown value", details)
	}
	if !i.State.IsValid() {
		return invalidModelError(where, "invitation", "state", "has an unknown value", details)
	}
	if !IsValidEmail(i.TargetEmail) || i.TargetEmail != normalizeInvitationEmail(i.TargetEmail) {
		return invalidModelError(where, "invitation", "target_email", "must be a normalized email address", details)
	}
	if !i.InviterUserID.IsValid() {
		return invalidModelError(where, "invitation", "inviter_user_id", "must be a valid identifier", details)
	}
	if !i.ScopeType.IsValid() || !IsValidOpaqueID(i.ScopeID) {
		return invalidModelError(where, "invitation", "scope", "must identify a valid authorization scope", details)
	}
	if !IsValidTokenHash(i.ClaimHash) {
		return invalidModelError(where, "invitation", "claim_hash", "has an invalid format", details)
	}
	if !IsValidInvitationLifetime(i.ExpiresAt.Sub(i.CreatedAt)) {
		return invalidModelError(where, "invitation", "expires_at", "must be between one and thirty days after creation", details)
	}
	if err := validateInvitationSuggestions(i.Suggestions); err != nil {
		return invalidModelError(where, "invitation", "suggestions", err.Error(), details)
	}
	if err := i.validatePackage(details); err != nil {
		return err
	}

	return i.validateAcceptance(details)
}

func (i *Invitation) validatePackage(details string) error {
	const where = "Invitation.Validate"
	if i.Purpose == InvitationPurposeStudentClass {
		if !i.ClassID.IsValid() || !i.AcademicPeriodID.IsValid() {
			return invalidModelError(where, "invitation", "student_class", "must identify a Class and Academic Period", details)
		}
		if i.ScopeType != RoleScopeClass || i.ScopeID != i.ClassID.String() {
			return invalidModelError(where, "invitation", "scope", "must identify the exact invited Class", details)
		}

		if i.IntendedStartsAt.IsZero() ||
			(i.IntendedEndsAt.Valid && !i.IntendedEndsAt.Time.After(i.IntendedStartsAt)) {
			return invalidModelError(where, "invitation", "effective_bounds", "must form a valid half-open interval", details)
		}

		if i.AcademicUnitID.IsValid() || i.RoleID.IsValid() || len(i.RoleActions) != 0 {
			return invalidModelError(where, "invitation", "student_class", "must not contain a teacher package", details)
		}
	}

	if i.Purpose == InvitationPurposeTeacherAcademicUnit {
		if !i.AcademicUnitID.IsValid() ||
			!i.RoleID.IsValid() ||
			i.ScopeType != RoleScopeAcademicUnit ||
			i.ScopeID != i.AcademicUnitID.String() {
			return invalidModelError(where, "invitation", "teacher_academic_unit", "must identify the exact Academic Unit and Role", details)
		}

		if len(i.RoleActions) == 0 || !slices.IsSorted(i.RoleActions) {
			return invalidModelError(where, "invitation", "role_actions", "must be a nonempty canonical action snapshot", details)
		}

		for index, action := range i.RoleActions {
			if !IsGrantableAction(action) || (index > 0 && i.RoleActions[index-1] == action) {
				return invalidModelError(where, "invitation", "role_actions", "must contain unique grantable actions", details)
			}
		}

		if i.IntendedStartsAt.IsZero() ||
			(i.IntendedEndsAt.Valid && !i.IntendedEndsAt.Time.After(i.IntendedStartsAt)) {
			return invalidModelError(where, "invitation", "effective_bounds", "must form a valid half-open interval", details)
		}

		if i.ClassID.IsValid() || i.AcademicPeriodID.IsValid() {
			return invalidModelError(where, "invitation", "teacher_academic_unit", "must not contain a student package", details)
		}
	}

	if i.Purpose == InvitationPurposeAcademicUnitRole || i.Purpose == InvitationPurposeInstitutionRole {
		if !i.RoleID.IsValid() || len(i.RoleActions) == 0 || !slices.IsSorted(i.RoleActions) {
			return invalidModelError(where, "invitation", "scoped_role", "must identify a Role with a nonempty canonical action snapshot", details)
		}

		for index, action := range i.RoleActions {
			if !IsGrantableAction(action) || (index > 0 && i.RoleActions[index-1] == action) {
				return invalidModelError(where, "invitation", "role_actions", "must contain unique grantable actions", details)
			}
		}

		if i.IntendedStartsAt.IsZero() ||
			(i.IntendedEndsAt.Valid && !i.IntendedEndsAt.Time.After(i.IntendedStartsAt)) {
			return invalidModelError(where, "invitation", "effective_bounds", "must form a valid half-open interval", details)
		}

		if i.ClassID.IsValid() || i.AcademicPeriodID.IsValid() || !invitationScopedRoleTargetIsValid(i) {
			return invalidModelError(where, "invitation", "scoped_role", "must identify its exact authorization scope", details)
		}
	}

	return nil
}

func (i *Invitation) validateAcceptance(details string) error {
	const where = "Invitation.Validate"
	if i.State == InvitationAccepted {
		if !i.AcceptedAt.Valid ||
			!i.AcceptedUserID.IsValid() ||
			i.AcceptedAt.Time.Before(i.CreatedAt) ||
			!i.AcceptedAt.Time.Before(i.ExpiresAt) {
			return invalidModelError(where, "invitation", "acceptance", "must identify a User within the invitation lifetime", details)
		}

		switch i.Purpose {
		case InvitationPurposeStudentClass:
			if !i.AcceptedAffiliationID.IsValid() ||
				!i.AcceptedClassMemberID.IsValid() ||
				i.AcceptedAcademicUnitMemberID.IsValid() ||
				i.AcceptedRoleBindingID.IsValid() {
				return invalidModelError(where, "invitation", "acceptance", "must identify the accepted Class package", details)
			}
		case InvitationPurposeTeacherAcademicUnit:
			if !i.AcceptedAffiliationID.IsValid() ||
				i.AcceptedClassMemberID.IsValid() ||
				!i.AcceptedAcademicUnitMemberID.IsValid() ||
				!i.AcceptedRoleBindingID.IsValid() {
				return invalidModelError(where, "invitation", "acceptance", "must identify the accepted Academic Unit package", details)
			}
		case InvitationPurposeAcademicUnitRole, InvitationPurposeInstitutionRole:
			if i.AcceptedAffiliationID.IsValid() ||
				i.AcceptedClassMemberID.IsValid() ||
				i.AcceptedAcademicUnitMemberID.IsValid() ||
				!i.AcceptedRoleBindingID.IsValid() {
				return invalidModelError(where, "invitation", "acceptance", "must identify only the accepted Role Binding", details)
			}
		}
	} else if i.AcceptedAt.Valid ||
		!i.AcceptedUserID.IsZero() ||
		!i.AcceptedAffiliationID.IsZero() ||
		!i.AcceptedClassMemberID.IsZero() ||
		!i.AcceptedAcademicUnitMemberID.IsZero() ||
		!i.AcceptedRoleBindingID.IsZero() {
		return invalidModelError(where, "invitation", "acceptance", "must be empty outside accepted state", details)
	}

	return nil
}

// Accept applies the one successful claim transition. The store transaction
// still rechecks policy and relationships.
func (i *Invitation) Accept(userID UserID, affiliationID AffiliationID, classMemberID ClassMemberID, at time.Time) error {
	if i == nil ||
		i.Purpose != InvitationPurposeStudentClass ||
		i.State != InvitationPending ||
		!userID.IsValid() ||
		!affiliationID.IsValid() ||
		!classMemberID.IsValid() {
		return invalidModelError("Invitation.Accept", "invitation", "", "invitation cannot be accepted", "")
	}

	at = TimeUTC(at)
	if at.Before(i.CreatedAt) ||
		!at.Before(i.ExpiresAt) ||
		(i.IntendedEndsAt.Valid && !at.Before(i.IntendedEndsAt.Time)) {
		return invalidModelError("Invitation.Accept", "invitation", "", "invitation cannot be accepted at this time", "")
	}

	i.State = InvitationAccepted
	i.AcceptedAt = OptionalTimeFrom(at)
	i.AcceptedUserID = userID
	i.AcceptedAffiliationID = affiliationID
	i.AcceptedClassMemberID = classMemberID
	i.UpdatedAt = at
	i.Revision++
	if err := i.Validate(); err != nil {
		return err
	}

	return nil
}

// AcceptTeacherAcademicUnit records the outcome of applying a teacher package.
// Replay reads these kept IDs, not the latest relationship rows.
func (i *Invitation) AcceptTeacherAcademicUnit(
	userID UserID,
	affiliationID AffiliationID,
	memberID AcademicUnitMemberID,
	bindingID RoleBindingID,
	at time.Time,
) error {
	if i == nil ||
		i.Purpose != InvitationPurposeTeacherAcademicUnit ||
		i.State != InvitationPending ||
		!userID.IsValid() ||
		!affiliationID.IsValid() ||
		!memberID.IsValid() ||
		!bindingID.IsValid() {
		return invalidModelError(
			"Invitation.AcceptTeacherAcademicUnit",
			"invitation",
			"",
			"teacher academic unit invitation cannot be accepted",
			"",
		)
	}

	at = TimeUTC(at)
	if at.Before(i.CreatedAt) ||
		!at.Before(i.ExpiresAt) ||
		(i.IntendedEndsAt.Valid && !at.Before(i.IntendedEndsAt.Time)) {
		return invalidModelError(
			"Invitation.AcceptTeacherAcademicUnit",
			"invitation",
			"",
			"teacher academic unit invitation cannot be accepted at this time",
			"",
		)
	}

	i.State = InvitationAccepted
	i.AcceptedAt = OptionalTimeFrom(at)
	i.AcceptedUserID = userID
	i.AcceptedAffiliationID = affiliationID
	i.AcceptedAcademicUnitMemberID = memberID
	i.AcceptedRoleBindingID = bindingID
	i.UpdatedAt = at
	i.Revision++
	return i.Validate()
}

// AcceptScopedRole records the existing user and the role binding the
// acceptance transaction chose.
func (i *Invitation) AcceptScopedRole(userID UserID, bindingID RoleBindingID, at time.Time) error {
	if i == nil ||
		(i.Purpose != InvitationPurposeAcademicUnitRole && i.Purpose != InvitationPurposeInstitutionRole) ||
		i.State != InvitationPending ||
		!userID.IsValid() ||
		!bindingID.IsValid() {
		return invalidModelError("Invitation.AcceptScopedRole", "invitation", "", "scoped Role invitation cannot be accepted", "")
	}

	at = TimeUTC(at)
	if at.Before(i.CreatedAt) ||
		!at.Before(i.ExpiresAt) ||
		(i.IntendedEndsAt.Valid && !at.Before(i.IntendedEndsAt.Time)) {
		return invalidModelError("Invitation.AcceptScopedRole", "invitation", "", "scoped Role invitation cannot be accepted at this time", "")
	}

	i.State = InvitationAccepted
	i.AcceptedAt = OptionalTimeFrom(at)
	i.AcceptedUserID = userID
	i.AcceptedRoleBindingID = bindingID
	i.UpdatedAt = at
	i.Revision++
	return i.Validate()
}

func invitationScopedRoleTargetIsValid(invitation *Invitation) bool {
	if invitation == nil {
		return false
	}

	switch invitation.Purpose {
	case InvitationPurposeAcademicUnitRole:
		return invitation.AcademicUnitID.IsValid() &&
			invitation.ScopeType == RoleScopeAcademicUnit &&
			invitation.ScopeID == invitation.AcademicUnitID.String()
	case InvitationPurposeInstitutionRole:
		return invitation.AcademicUnitID.IsZero() && invitation.ScopeType == RoleScopeInstitution && IsValidOpaqueID(invitation.ScopeID)
	default:
		return false
	}
}

// Expire closes a pending invitation, only once its claim lifetime or packaged
// relationship interval has passed at database time.
func (i *Invitation) Expire(at time.Time) error {
	if i == nil || i.State != InvitationPending {
		return invalidModelError("Invitation.Expire", "invitation", "", "invitation cannot be expired", "")
	}
	at = TimeUTC(at)
	elapsed := !at.Before(i.ExpiresAt) || (i.IntendedEndsAt.Valid && !at.Before(i.IntendedEndsAt.Time))
	if at.Before(i.CreatedAt) || !elapsed {
		return invalidModelError("Invitation.Expire", "invitation", "", "invitation cannot be expired at this time", "")
	}
	i.State = InvitationExpired
	i.UpdatedAt = at
	i.Revision++
	return i.Validate()
}

// Resend rotates the claim of an otherwise unchanged pending invitation. It
// keeps the creation time and expiry; extending one needs an explicit
// replacement.
func (i *Invitation) Resend(claimHash string, at time.Time) error {
	if i == nil || i.State != InvitationPending || !IsValidTokenHash(claimHash) || claimHash == i.ClaimHash {
		return invalidModelError("Invitation.Resend", "invitation", "", "invitation cannot be resent", "")
	}

	at = TimeUTC(at)
	if at.Before(i.CreatedAt) ||
		!at.Before(i.ExpiresAt) ||
		(i.IntendedEndsAt.Valid && !at.Before(i.IntendedEndsAt.Time)) {
		return invalidModelError("Invitation.Resend", "invitation", "", "invitation cannot be resent at this time", "")
	}

	i.ClaimHash = claimHash
	i.UpdatedAt = at
	i.Revision++
	return i.Validate()
}

// Revoke makes a pending invitation unusable at once. Delivery cleanup and any
// notice for mail already handed to SMTP belong to the store transaction.
func (i *Invitation) Revoke(at time.Time) error {
	return i.terminalizeAdministratively(InvitationRevoked, at)
}

// Supersede closes a pending invitation just before the same transaction
// inserts its replacement.
func (i *Invitation) Supersede(at time.Time) error {
	return i.terminalizeAdministratively(InvitationSuperseded, at)
}

// HasSamePackage reports whether other carries exactly the same recipient and
// grant package, ignoring identity, claim, lifecycle, timestamps and inviter.
func (i *Invitation) HasSamePackage(other *Invitation) bool {
	return i != nil &&
		other != nil &&
		i.Purpose == other.Purpose &&
		i.TargetEmail == other.TargetEmail &&
		i.ClassID == other.ClassID &&
		i.AcademicPeriodID == other.AcademicPeriodID &&
		i.AcademicUnitID == other.AcademicUnitID &&
		i.RoleID == other.RoleID &&
		slices.Equal(i.RoleActions, other.RoleActions) &&
		i.IntendedStartsAt.Equal(other.IntendedStartsAt) &&
		sameInvitationOptionalTime(i.IntendedEndsAt, other.IntendedEndsAt) &&
		i.Suggestions == other.Suggestions &&
		i.ScopeType == other.ScopeType &&
		i.ScopeID == other.ScopeID
}

func sameInvitationOptionalTime(first, second OptionalTime) bool {
	return first.Valid == second.Valid && (!first.Valid || first.Time.Equal(second.Time))
}

func (i *Invitation) terminalizeAdministratively(state InvitationState, at time.Time) error {
	if i == nil || i.State != InvitationPending || (state != InvitationRevoked && state != InvitationSuperseded) {
		return invalidModelError(
			"Invitation.terminalizeAdministratively",
			"invitation",
			"",
			"invitation cannot be terminalized administratively",
			"",
		)
	}

	at = TimeUTC(at)
	if at.Before(i.CreatedAt) ||
		!at.Before(i.ExpiresAt) ||
		(i.IntendedEndsAt.Valid && !at.Before(i.IntendedEndsAt.Time)) {
		return invalidModelError(
			"Invitation.terminalizeAdministratively",
			"invitation",
			"",
			"invitation cannot be terminalized administratively at this time",
			"",
		)
	}

	i.State = state
	i.UpdatedAt = at
	i.Revision++
	return i.Validate()
}

// EffectiveStartsAt returns when an accepted relationship starts: never before
// acceptedAt, but a future start is kept.
func (i *Invitation) EffectiveStartsAt(acceptedAt time.Time) time.Time {
	acceptedAt = TimeUTC(acceptedAt)
	if i == nil || i.IntendedStartsAt.Before(acceptedAt) {
		return acceptedAt
	}
	return i.IntendedStartsAt
}

// IsPendingAt reports whether a claim may still be evaluated at database time
// at. It does not replace rechecking policy or the package.
func (i *Invitation) IsPendingAt(at time.Time) bool {
	return i != nil && i.State == InvitationPending && TimeUTC(at).Before(i.ExpiresAt)
}

// Auditable returns the relationship package; recipient and claim material
// are left out.
func (i *Invitation) Auditable() map[string]any {
	if i == nil {
		return map[string]any{}
	}
	return map[string]any{
		"id":                               i.ID.String(),
		"purpose":                          i.Purpose,
		"state":                            i.State,
		"class_id":                         i.ClassID.String(),
		"academic_period_id":               i.AcademicPeriodID.String(),
		"academic_unit_id":                 i.AcademicUnitID.String(),
		"role_id":                          i.RoleID.String(),
		"role_actions":                     append([]string(nil), i.RoleActions...),
		"inviter_user_id":                  i.InviterUserID.String(),
		"scope_type":                       i.ScopeType,
		"scope_id":                         i.ScopeID,
		"created_at":                       MillisFromTime(i.CreatedAt),
		"updated_at":                       MillisFromTime(i.UpdatedAt),
		"expires_at":                       MillisFromTime(i.ExpiresAt),
		"accepted_at":                      i.AcceptedAt.Millis(),
		"accepted_user_id":                 i.AcceptedUserID.String(),
		"accepted_affiliation_id":          i.AcceptedAffiliationID.String(),
		"accepted_class_member_id":         i.AcceptedClassMemberID.String(),
		"accepted_academic_unit_member_id": i.AcceptedAcademicUnitMemberID.String(),
		"accepted_role_binding_id":         i.AcceptedRoleBindingID.String(),
	}
}

func canonicalInvitationRoleActions(actions []string) []string {
	result := append([]string(nil), actions...)
	sort.Strings(result)
	return result
}

func (p InvitationPurpose) IsValid() bool {
	switch p {
	case InvitationPurposeStudentClass,
		InvitationPurposeTeacherAcademicUnit,
		InvitationPurposeAcademicUnitRole,
		InvitationPurposeInstitutionRole:
		return true
	default:
		return false
	}
}

func (s InvitationState) IsValid() bool {
	switch s {
	case InvitationPending, InvitationAccepted, InvitationRevoked, InvitationExpired, InvitationSuperseded:
		return true
	default:
		return false
	}
}

func normalizeInvitationEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(SanitizeUnicode(value)))
}

func normalizeOptionalInvitationTime(value OptionalTime) OptionalTime {
	if !value.Valid {
		return OptionalTime{}
	}
	return OptionalTimeFrom(value.Time)
}

func normalizeInvitationSuggestions(value InvitationProfileSuggestions) InvitationProfileSuggestions {
	value.Username = strings.ToLower(SanitizeUnicode(value.Username))
	value.DisplayName = SanitizeUnicode(value.DisplayName)
	value.FirstName = SanitizeUnicode(value.FirstName)
	value.LastName = SanitizeUnicode(value.LastName)
	value.Locale = SanitizeUnicode(value.Locale)
	value.Timezone = SanitizeUnicode(value.Timezone)
	return value
}

func validateInvitationSuggestions(value InvitationProfileSuggestions) error {
	if value.Username != "" && !IsValidUsername(value.Username) {
		return invalidModelError("validateInvitationSuggestions", "invitation", "", "username has an invalid format", "")
	}
	if utf8.RuneCountInString(value.DisplayName) > UserDisplayNameMaxRunes ||
		utf8.RuneCountInString(value.FirstName) > UserPersonalNameMaxRunes ||
		utf8.RuneCountInString(value.LastName) > UserPersonalNameMaxRunes {
		return invalidModelError("validateInvitationSuggestions", "invitation", "", "profile name is too long", "")
	}
	if ContainsControlCharacter(value.DisplayName) || ContainsControlCharacter(value.FirstName) || ContainsControlCharacter(value.LastName) {
		return invalidModelError("validateInvitationSuggestions", "invitation", "", "profile name must not contain control characters", "")
	}
	if value.Locale != "" && (len(value.Locale) > UserLocaleMaxLength || !validLocale.MatchString(value.Locale)) {
		return invalidModelError("validateInvitationSuggestions", "invitation", "", "locale has an invalid format", "")
	}
	if len(value.Timezone) > UserTimezoneMaxLength {
		return invalidModelError("validateInvitationSuggestions", "invitation", "", "timezone is too long", "")
	}
	return nil
}

var _ Auditable = (*Invitation)(nil)
