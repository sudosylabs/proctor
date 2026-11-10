// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CreateAcademicUnitMemberCommand is the input of CreateAcademicUnitMember.
type CreateAcademicUnitMemberCommand struct {
	AcademicUnitID model.AcademicUnitID
	UserID         model.UserID
	StartAt        int64
	IdempotencyKey string
	Batch          batchitem.Item
}

// EndAcademicUnitMemberCommand is the input of EndAcademicUnitMember.
type EndAcademicUnitMemberCommand struct {
	ID             model.AcademicUnitMemberID
	IdempotencyKey string
	BatchScopeID   string
	Batch          batchitem.Item
}

// CreateAcademicUnitMember creates an Academic Unit Membership after authorizing the caller.
func (s *Service) CreateAcademicUnitMember(ctx context.Context, invocation invocation.Invocation, command CreateAcademicUnitMemberCommand) (*model.AcademicUnitMember, error) {
	resource, err := s.authorizeUnit(ctx, invocation, command.AcademicUnitID, model.ActionAcademicUnitMembersManage)
	if err != nil {
		return nil, err
	}
	unitID, err := model.ParseAcademicUnitID(resource.ID)
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "academic_unit_id").Wrap(err)
	}
	userID, err := model.ParseUserID(strings.TrimSpace(command.UserID.String()))
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "user_id").Wrap(err)
	}
	memberID, err := model.ParseAcademicUnitMemberID(s.newID())
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "academic_unit_member_id").Wrap(err)
	}
	candidate := &model.AcademicUnitMember{
		AcademicUnitID: unitID,
		UserID:         userID,
		StartsAt:       model.TimeFromMillis(command.StartAt),
	}
	at := model.TimeUTC(s.now())
	candidate.PrepareCreate(memberID, at)
	if err := candidate.Validate(); err != nil {
		return nil, apperror.DomainInvalid("academic_unit_member.invalid", err)
	}
	recipient := &model.User{ID: userID, Revision: 1}
	var notice *store.PreparedMail
	if !command.Batch.Fields().RetainedOutcome {
		recipient, err = s.users.Get(ctx, userID)
		if err != nil {
			return nil, AcademicUnitMemberStoreErrors.Translate(err)
		}
		notice, err = s.mail.PrepareRelationshipTransition(mail.RelationshipTransitionPreparation{
			Recipient: recipient, OccurrenceID: model.NewMailOccurrenceID(),
			TemplateKey: model.MailTemplateAcademicUnitAssigned, ActionAt: at,
		})
		if err != nil {
			return nil, apperror.New("mail.unavailable").Wrap(err)
		}
	}
	idempotency, err := idempotency.Prepare(invocation, "academic_unit_member.add", command.IdempotencyKey, struct {
		AcademicUnitID string `json:"academic_unit_id"`
		UserID         string `json:"user_id"`
		StartAt        int64  `json:"start_at"`
	}{candidate.AcademicUnitID.String(), candidate.UserID.String(), command.StartAt})
	if err != nil {
		return nil, err
	}
	batchitem.BindOnboardingImport(idempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	batchitem.BindAuthorization(idempotency, command.Batch.Fields().Authorization)
	batchitem.BindBatch(idempotency, command.Batch.Fields().Metadata)
	changed := false
	result, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionAcademicUnitMembersManage,
			Resource:   resource,
			Operation:  "create_member",
			Value:      candidate.Auditable(),
			Subject:    candidate.UserID,
		},
		func() time.Time { return at },
		func(ctx context.Context, reference access.MutationReference) (*model.AcademicUnitMember, error) {
			input := &store.AcademicUnitMemberCreation{Member: candidate, ExpectedRecipientRevision: recipient.Revision,
				Notice: notice, AuditEventID: reference.ID, AuditAt: reference.MutationAt, Command: idempotency}
			value, storeErr := s.academicUnitMembers.Create(ctx, input)
			changed = storeErr == nil && !input.Replayed && !input.NoOp
			if command.Batch.Fields().Replayed != nil {
				*command.Batch.Fields().Replayed = input.Replayed || input.NoOp
			}
			return value, storeErr
		},
		AcademicUnitMemberStoreErrors.Translate,
	)
	if err == nil && changed {
		s.effects.InvalidateAuthorization(ctx, userID)
	}
	return result, err
}

// EndAcademicUnitMember ends an Academic Unit Membership after authorizing the caller.
func (s *Service) EndAcademicUnitMember(ctx context.Context, invocation invocation.Invocation, command EndAcademicUnitMemberCommand) (*model.AcademicUnitMember, error) {
	id := command.ID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "academic_unit_member_id")
	}
	if err := s.authorizer.AuthorizePreflight(
		ctx, invocation, model.ActionAcademicUnitMembersManage, model.ResourceAcademicUnit,
	); err != nil {
		return nil, err
	}
	current, err := s.academicUnitMembers.Get(ctx, id)
	if err != nil {
		return nil, AcademicUnitMemberStoreErrors.Translate(err)
	}
	resource, err := s.authorizeUnit(ctx, invocation, current.AcademicUnitID, model.ActionAcademicUnitMembersManage)
	if err != nil {
		return nil, concealMembershipAuthorizationError(err, "academic_unit_member")
	}
	if command.BatchScopeID != "" && strings.TrimSpace(command.BatchScopeID) != current.AcademicUnitID.String() {
		return nil, apperror.New("resource.not_found").WithField("resource", "academic_unit_member")
	}
	idempotency, err := idempotency.Prepare(invocation, "academic_unit_member.end", command.IdempotencyKey, struct {
		ID      string `json:"id"`
		ScopeID string `json:"scope_id,omitempty"`
	}{id.String(), strings.TrimSpace(command.BatchScopeID)})
	if err != nil {
		return nil, err
	}
	batchitem.BindOnboardingImport(idempotency, command.Batch.Fields().OnboardingImportID, command.Batch.Fields().OnboardingImportRowNumber)
	batchitem.BindAuthorization(idempotency, command.Batch.Fields().Authorization)
	batchitem.BindBatch(idempotency, command.Batch.Fields().Metadata)
	recipient := &model.User{ID: current.UserID, Revision: 1}
	var notice *store.PreparedMail
	at := model.TimeUTC(s.now())
	if !command.Batch.Fields().RetainedOutcome {
		recipient, err = s.users.Get(ctx, current.UserID)
		if err != nil {
			return nil, AcademicUnitMemberStoreErrors.Translate(err)
		}
		notice, err = s.mail.PrepareRelationshipTransition(mail.RelationshipTransitionPreparation{
			Recipient: recipient, OccurrenceID: model.NewMailOccurrenceID(),
			TemplateKey: model.MailTemplateAcademicUnitAssignmentEnded, ActionAt: at,
		})
		if err != nil {
			return nil, apperror.New("mail.unavailable").Wrap(err)
		}
	}
	changed := false
	result, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionAcademicUnitMembersManage,
			Resource:   resource,
			Operation:  "end_member",
			Prior:      current.Auditable(),
			Subject:    current.UserID,
		},
		func() time.Time { return at },
		func(ctx context.Context, reference access.MutationReference) (*model.AcademicUnitMember, error) {
			input := &store.AcademicUnitMemberEnd{
				ID: id, ExpectedRevision: current.Revision, EndAt: reference.MutationAt,
				ExpectedRecipientRevision: recipient.Revision, Notice: notice,
				AuditEventID: reference.ID, AuditAt: reference.MutationAt, Command: idempotency,
			}
			value, storeErr := s.academicUnitMembers.End(ctx, input)
			changed = storeErr == nil && !input.Replayed && !input.NoOp
			if command.Batch.Fields().Replayed != nil {
				*command.Batch.Fields().Replayed = input.Replayed || input.NoOp
			}
			return value, storeErr
		},
		AcademicUnitMemberStoreErrors.Translate,
	)
	if err == nil && changed {
		s.effects.InvalidateAuthorization(ctx, current.UserID)
	}
	return result, err
}

func concealMembershipAuthorizationError(err error, resource string) error {
	if apperror.Is(err, "authorization.denied") {
		return apperror.New("resource.not_found").WithField("resource", resource)
	}
	return err
}

func (s *Service) authorizeUnit(ctx context.Context, invocation invocation.Invocation, unitID model.AcademicUnitID, action model.Action) (model.Resource, error) {
	if !unitID.IsValid() {
		return model.Resource{}, apperror.New("request.invalid").WithField("field", "academic_unit_id")
	}
	resource := model.Resource{Type: model.ResourceAcademicUnit, ID: unitID.String()}
	if err := s.authorizer.Authorize(ctx, invocation, action, resource); err != nil {
		return model.Resource{}, err
	}
	return resource, nil
}

// AcademicUnitMemberStoreErrors translates Academic Unit Membership Store
// failures; onboarding imports present them the same way.
var AcademicUnitMemberStoreErrors = apperror.AdministrationStoreTable("academic_unit_member", "academic_unit_member.conflict", "academic_unit_member.invalid", map[string]apperror.Code{
	"academic_unit_member_end_time": "resource.not_found",
})

// ListAcademicUnitMembersPageQuery opts into bounded membership enumeration. Each page is
// authorized independently and explicit selectors preserve its effective filter.
type ListAcademicUnitMembersPageQuery struct {
	AfterUserID    model.UserID
	AfterID        model.AcademicUnitMemberID
	AcademicUnitID model.AcademicUnitID
	MembershipPageQuery
}

// AcademicUnitMemberPage is one page of a unit's memberships. Users names
// every listed member; its Email is set only for a caller holding
// Institution-wide user.view. RolesHere holds each member's current Role
// Bindings scoped exactly to the unit, at most academicUnitMemberRolesHere
// each, and is nil when the caller lacks role_binding.view on the unit.
type AcademicUnitMemberPage struct {
	Members   []*model.AcademicUnitMember
	HasMore   bool
	ActiveAt  int64
	Users     map[model.UserID]store.UserIdentity
	RolesHere map[model.UserID]store.UserRoleBindingSummary
}

// academicUnitMemberRolesHere bounds the Role Bindings shown per member.
const academicUnitMemberRolesHere = 5

// ListAcademicUnitMembersPage lists a page of Academic Unit Memberships after authorizing the caller.
func (s *Service) ListAcademicUnitMembersPage(ctx context.Context, invocation invocation.Invocation, query ListAcademicUnitMembersPageQuery) (*AcademicUnitMemberPage, error) {
	resource, err := s.authorizeUnit(ctx, invocation, query.AcademicUnitID, model.ActionAcademicUnitMembersView)
	if err != nil {
		return nil, err
	}
	now := model.TimeUTC(s.now())
	activeAt, limit, err := resolveMembershipPage(query.MembershipPageQuery, now)
	if err != nil {
		return nil, err
	}
	scopeID, err := model.ParseAcademicUnitID(resource.ID)
	if err != nil {
		return nil, apperror.New("request.invalid").WithField("field", "academic_unit_id")
	}
	if query.AfterID.IsZero() != query.AfterUserID.IsZero() || (!query.AfterID.IsZero() && (!query.AfterID.IsValid() || !query.AfterUserID.IsValid())) {
		return nil, apperror.New("request.invalid").WithField("field", "cursor")
	}
	options := store.AcademicUnitMemberPageOptions{AcademicUnitID: scopeID, ActiveAt: model.TimeFromMillis(activeAt), Limit: limit, AfterID: query.AfterID, AfterUserID: query.AfterUserID}

	page, err := s.academicUnitMembers.ListPageByAcademicUnit(ctx, options)
	if err != nil {
		return nil, AcademicUnitMemberStoreErrors.Translate(err)
	}
	result := &AcademicUnitMemberPage{Members: page.Members, ActiveAt: activeAt, HasMore: page.HasMore}
	if result.Members == nil {
		result.Members = []*model.AcademicUnitMember{}
	}
	if err := s.describeMembers(ctx, invocation, resource, result, now); err != nil {
		return nil, err
	}
	return result, nil
}

// describeMembers names the page's members in one read and, for a caller
// holding role_binding.view on the unit, reads their current Role Bindings
// scoped exactly to it in another. Seeing a unit's members shows who they
// are; email stays with Institution-wide user.view, as in the User directory.
func (s *Service) describeMembers(ctx context.Context, invocation invocation.Invocation, unit model.Resource, page *AcademicUnitMemberPage, now time.Time) error {
	page.Users = map[model.UserID]store.UserIdentity{}
	if len(page.Members) == 0 {
		return nil
	}
	userIDs := make([]model.UserID, 0, len(page.Members))
	for _, member := range page.Members {
		if _, seen := page.Users[member.UserID]; !seen {
			page.Users[member.UserID] = store.UserIdentity{}
			userIDs = append(userIDs, member.UserID)
		}
	}
	identities, err := s.users.ListIdentities(ctx, userIDs)
	if err != nil {
		return AcademicUnitMemberStoreErrors.Translate(err)
	}
	showEmail, err := s.authorizer.CanViewInstitutionUsers(ctx, invocation)
	if err != nil {
		return err
	}
	for _, identity := range identities {
		if !showEmail {
			identity.Email = ""
		}
		page.Users[identity.ID] = identity
	}
	for _, userID := range userIDs {
		if page.Users[userID].ID != userID {
			return apperror.New("administration.unavailable").WithField("resource", "academic_unit_member")
		}
	}
	showRoles, err := s.authorizer.CanViewRoleBindings(ctx, invocation, unit)
	if err != nil || !showRoles {
		return err
	}
	unitID := model.AcademicUnitID(unit.ID)
	roles, err := s.roleBindings.ListVisibleSummariesByUsers(ctx, store.RoleBindingSummaryOptions{
		UserIDs: userIDs, Limit: academicUnitMemberRolesHere,
		Visibility: store.UserVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{unitID}, ActiveAt: now},
		ScopeType:  model.RoleScopeAcademicUnit, ScopeID: unit.ID,
	})
	if err != nil {
		return AcademicUnitMemberStoreErrors.Translate(err)
	}
	page.RolesHere = make(map[model.UserID]store.UserRoleBindingSummary, len(userIDs))
	for _, userID := range userIDs {
		page.RolesHere[userID] = store.UserRoleBindingSummary{UserID: userID}
	}
	for _, summary := range roles {
		page.RolesHere[summary.UserID] = summary
	}
	return nil
}
