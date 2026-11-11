// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type invitationStoreFake struct {
	store.InvitationStore
	invitation       *model.Invitation
	issued           *store.StudentClassInvitationIssue
	accepted         *store.StudentClassInvitationAcceptance
	teacherIssued    *store.TeacherAcademicUnitInvitationIssue
	teacherAccepted  *store.TeacherAcademicUnitInvitationAcceptance
	scopedIssued     *store.ScopedRoleInvitationIssue
	scopedAccepted   *store.ScopedRoleInvitationAcceptance
	scopedAcceptErr  error
	listOptions      store.InvitationListOptions
	countOptions     store.InvitationListOptions
	countMaximum     int
	countTotal       int
	resent           *store.InvitationResend
	revoked          *store.InvitationRevocation
	replaced         *store.InvitationReplacement
	externalAccepted *store.ExternalIdentityInvitationAcceptance
	events           *[]string
	idempotentIssues map[[32]byte]*model.Invitation
	batchDuplicates  map[[32]byte]bool
	issueExecutions  int
	onboardingNoOp   bool
}

func (f *invitationStoreFake) ResolveOnboardingInvitationNoOp(_ context.Context, candidate *model.Invitation) (*model.Invitation, bool, error) {
	if f.onboardingNoOp {
		return candidate, true, nil
	}
	return nil, false, nil
}

func (f *invitationStoreFake) RecordBatchDuplicate(_ context.Context, _ *store.InvitationBatchDuplicate, command *store.CommandIdempotency) (*store.InvitationBatchCommandResult, error) {
	if invitation := f.idempotentIssues[command.KeyDigest]; invitation != nil {
		return &store.InvitationBatchCommandResult{InvitationID: invitation.ID, Replayed: true}, nil
	}
	if f.batchDuplicates == nil {
		f.batchDuplicates = make(map[[32]byte]bool)
	}
	replayed := f.batchDuplicates[command.KeyDigest]
	f.batchDuplicates[command.KeyDigest] = true
	return &store.InvitationBatchCommandResult{Duplicate: true, Replayed: replayed}, nil
}

func (f *invitationStoreFake) ReplayIssue(_ context.Context, _ *store.InvitationReplay, command *store.CommandIdempotency) (*store.InvitationCommandResult, error) {
	if f.batchDuplicates[command.KeyDigest] {
		return &store.InvitationCommandResult{Duplicate: true, Replayed: true}, nil
	}
	if invitation := f.idempotentIssues[command.KeyDigest]; invitation != nil {
		return &store.InvitationCommandResult{Invitation: invitation, Replayed: true}, nil
	}
	return nil, store.NewErrNotFound("command_outcome", "idempotency_key")
}

func (f *invitationStoreFake) FindCommandOutcome(ctx context.Context, command *store.CommandIdempotency) (*store.InvitationCommandResult, error) {
	return f.ReplayIssue(ctx, &store.InvitationReplay{}, command)
}

func (f *invitationStoreFake) IssueStudentClass(_ context.Context, input *store.StudentClassInvitationIssue) (*model.Invitation, error) {
	f.issued = input
	f.invitation = input.Invitation
	return input.Invitation, nil
}

func (f *invitationStoreFake) IssueStudentClassIdempotently(_ context.Context, input *store.StudentClassInvitationIssue, command *store.CommandIdempotency) (*store.InvitationCommandResult, error) {
	f.issued = input
	if f.onboardingNoOp && input.Occurrence == nil && input.Delivery == nil && input.DeliveryJob == nil {
		return &store.InvitationCommandResult{Invitation: input.Invitation, NoOp: true}, nil
	}
	if f.idempotentIssues == nil {
		f.idempotentIssues = make(map[[32]byte]*model.Invitation)
	}
	if existing := f.idempotentIssues[command.KeyDigest]; existing != nil {
		return &store.InvitationCommandResult{Invitation: existing, Replayed: true}, nil
	}
	f.issueExecutions++
	f.invitation = input.Invitation
	f.idempotentIssues[command.KeyDigest] = input.Invitation
	return &store.InvitationCommandResult{Invitation: input.Invitation}, nil
}

func TestOnboardingInvitationNoOpDoesNotRequireMail(t *testing.T) {
	t.Parallel()
	now := model.TimeFromMillis(1_800_000_000_000)
	periodID, classID := model.NewAcademicPeriodID(), model.NewClassID()
	persistence := &invitationStoreFake{onboardingNoOp: true}
	service := newInvitationServiceForTest(t, persistence,
		invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{}, &authorizerFake{}, &mailFake{disabled: true}, now)
	service.classes = invitationClassStoreFake{class: &model.Class{ID: classID, AcademicPeriodID: periodID}}
	service.periods = invitationPeriodStoreFake{period: &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour), EndsAt: now.Add(24 * time.Hour)}}
	view, err := service.IssueStudentClassInvitation(context.Background(), invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}),
		IssueStudentClassInvitationCommand{TargetEmail: "existing@example.edu", ClassID: classID, IdempotencyKey: "import-row",
			onboardingImportID: model.NewOnboardingImportID(), onboardingImportRowNumber: 1})
	require.NoError(t, err, "mail-disabled no-op = %#v / %#v", view, persistence.issued)
	require.True(t, view.NoOp, "mail-disabled no-op = %#v / %#v", view, persistence.issued)
	require.NotNil(t, persistence.issued, "mail-disabled no-op = %#v / %#v", view, persistence.issued)
	require.Nil(t, persistence.issued.Occurrence, "mail-disabled no-op = %#v / %#v", view, persistence.issued)
	require.Nil(t, persistence.issued.Delivery, "mail-disabled no-op = %#v / %#v", view, persistence.issued)
	require.Nil(t, persistence.issued.DeliveryJob, "mail-disabled no-op = %#v / %#v", view, persistence.issued)
}

func (f *invitationStoreFake) IssueTeacherAcademicUnit(_ context.Context, input *store.TeacherAcademicUnitInvitationIssue) (*model.Invitation, error) {
	f.teacherIssued = input
	f.invitation = input.Invitation
	return input.Invitation, nil
}

func (f *invitationStoreFake) IssueTeacherAcademicUnitIdempotently(_ context.Context, input *store.TeacherAcademicUnitInvitationIssue, _ *store.CommandIdempotency) (*store.InvitationCommandResult, error) {
	value, err := f.IssueTeacherAcademicUnit(context.Background(), input)
	return &store.InvitationCommandResult{Invitation: value}, err
}

func (f *invitationStoreFake) Get(context.Context, model.InvitationID) (*model.Invitation, error) {
	return f.invitation, nil
}

func (f *invitationStoreFake) GetByClaimHash(_ context.Context, hash string) (*model.Invitation, error) {
	if f.events != nil {
		*f.events = append(*f.events, "resolve")
	}
	if f.invitation == nil || f.invitation.ClaimHash != hash {
		return nil, store.NewErrNotFound("invitation", "claim")
	}
	return f.invitation, nil
}

func (f *invitationStoreFake) AcceptStudentClass(_ context.Context, input *store.StudentClassInvitationAcceptance) (*store.StudentClassInvitationAcceptanceResult, error) {
	f.accepted = input
	accepted := *f.invitation
	_ = accepted.Accept(input.User.ID, input.Affiliation.ID, input.ClassMember.ID, model.TimeUTC(input.AcceptedAt))
	return &store.StudentClassInvitationAcceptanceResult{Invitation: &accepted, User: input.User, Affiliation: input.Affiliation, ClassMember: input.ClassMember}, nil
}

func (f *invitationStoreFake) AcceptTeacherAcademicUnit(_ context.Context, input *store.TeacherAcademicUnitInvitationAcceptance) (*store.TeacherAcademicUnitInvitationAcceptanceResult, error) {
	f.teacherAccepted = input
	accepted := *f.invitation
	_ = accepted.AcceptTeacherAcademicUnit(input.User.ID, input.Affiliation.ID, input.AcademicUnitMember.ID, input.RoleBinding.ID, model.TimeUTC(input.AcceptedAt))
	return &store.TeacherAcademicUnitInvitationAcceptanceResult{Invitation: &accepted, User: input.User,
		Affiliation: input.Affiliation, AcademicUnitMember: input.AcademicUnitMember, RoleBinding: input.RoleBinding}, nil
}

func (f *invitationStoreFake) IssueScopedRole(_ context.Context, input *store.ScopedRoleInvitationIssue) (*model.Invitation, error) {
	f.scopedIssued = input
	f.invitation = input.Invitation
	return input.Invitation, nil
}

func (f *invitationStoreFake) IssueScopedRoleIdempotently(_ context.Context, input *store.ScopedRoleInvitationIssue, _ *store.CommandIdempotency) (*store.InvitationCommandResult, error) {
	value, err := f.IssueScopedRole(context.Background(), input)
	return &store.InvitationCommandResult{Invitation: value}, err
}

func (f *invitationStoreFake) AcceptScopedRole(_ context.Context, input *store.ScopedRoleInvitationAcceptance) (*store.ScopedRoleInvitationAcceptanceResult, error) {
	if f.events != nil {
		*f.events = append(*f.events, "accept")
	}
	f.scopedAccepted = input
	if f.scopedAcceptErr != nil {
		return nil, f.scopedAcceptErr
	}
	accepted := *f.invitation
	_ = accepted.AcceptScopedRole(input.UserID, input.RoleBinding.ID, model.TimeFromMillis(1_800_000_060_000))
	return &store.ScopedRoleInvitationAcceptanceResult{Invitation: &accepted, User: &model.User{ID: input.UserID}, RoleBinding: input.RoleBinding}, nil
}

func (f *invitationStoreFake) Maintain(context.Context, int) (*store.InvitationMaintenanceResult, error) {
	return &store.InvitationMaintenanceResult{}, nil
}

func (f *invitationStoreFake) List(_ context.Context, options store.InvitationListOptions) (*store.InvitationPage, error) {
	f.listOptions = options
	return &store.InvitationPage{Items: []*store.InvitationAdministrationRecord{{Invitation: f.invitation}}}, nil
}

func (f *invitationStoreFake) Count(_ context.Context, options store.InvitationListOptions, maximum int) (int, error) {
	f.countOptions, f.countMaximum = options, maximum
	return min(max(f.countTotal, 1), maximum), nil
}

func (f *invitationStoreFake) GetForAdministration(context.Context, model.InvitationID, store.InvitationVisibilityScope) (*store.InvitationAdministrationRecord, error) {
	return &store.InvitationAdministrationRecord{Invitation: f.invitation}, nil
}

func (f *invitationStoreFake) Resend(_ context.Context, input *store.InvitationResend) (*store.InvitationAdministrationRecord, error) {
	f.resent = input
	result := *f.invitation
	_ = result.Resend(input.ClaimHash, model.TimeUTC(input.AuditAt))
	return &store.InvitationAdministrationRecord{Invitation: &result, Delivery: &store.InvitationDeliverySummary{State: model.MailDeliveryQueued}}, nil
}

func (f *invitationStoreFake) ResendIdempotently(ctx context.Context, input *store.InvitationResend, _ *store.CommandIdempotency) (*store.InvitationAdministrationCommandResult, error) {
	record, err := f.Resend(ctx, input)
	return &store.InvitationAdministrationCommandResult{Record: record}, err
}

func (f *invitationStoreFake) Revoke(_ context.Context, input *store.InvitationRevocation) (*store.InvitationAdministrationRecord, error) {
	f.revoked = input
	result := *f.invitation
	_ = result.Revoke(model.TimeUTC(input.AuditAt))
	return &store.InvitationAdministrationRecord{Invitation: &result}, nil
}

func (f *invitationStoreFake) RevokeIdempotently(ctx context.Context, input *store.InvitationRevocation, _ *store.CommandIdempotency) (*store.InvitationAdministrationCommandResult, error) {
	record, err := f.Revoke(ctx, input)
	return &store.InvitationAdministrationCommandResult{Record: record}, err
}

func (f *invitationStoreFake) Replace(_ context.Context, input *store.InvitationReplacement) (*store.InvitationAdministrationRecord, error) {
	f.replaced = input
	return &store.InvitationAdministrationRecord{Invitation: input.Replacement}, nil
}

func (f *invitationStoreFake) AcceptExternalIdentity(_ context.Context, input *store.ExternalIdentityInvitationAcceptance) (*store.ExternalIdentityInvitationAcceptanceResult, error) {
	f.externalAccepted = input
	return &store.ExternalIdentityInvitationAcceptanceResult{Invitation: f.invitation, Identity: input.Identity,
		User: input.User, Affiliation: input.Affiliation, ClassMember: input.ClassMember,
		AcademicUnitMember: input.AcademicUnitMember, RoleBinding: input.RoleBinding}, nil
}

type invitationClassStoreFake struct {
	store.ClassStore
	class *model.Class
}

func (f invitationClassStoreFake) Get(context.Context, model.ClassID) (*model.Class, error) {
	return f.class, nil
}

type invitationPeriodStoreFake struct {
	store.AcademicPeriodStore
	period *model.AcademicPeriod
}

func (f invitationPeriodStoreFake) Get(context.Context, model.AcademicPeriodID) (*model.AcademicPeriod, error) {
	return f.period, nil
}

type invitationAcademicUnitStoreFake struct {
	store.AcademicUnitStore
	unit *model.AcademicUnit
}

func (f invitationAcademicUnitStoreFake) Get(context.Context, model.AcademicUnitID) (*model.AcademicUnit, error) {
	return f.unit, nil
}

type invitationRoleStoreFake struct {
	store.RoleStore
	role *model.Role
}

func (f invitationRoleStoreFake) Get(context.Context, model.RoleID) (*model.Role, error) {
	return f.role, nil
}

// mailFake stands in for the mail mail: it records the Invitation
// action URL and the public registration verification it prepares. A
// disabled fake suppresses acceptance notices and refuses registration mail.
type mailFake struct {
	issueURL        string
	disabled        bool
	directJobType   model.JobType
	directErr       error
	prepareCalls    int
	actionURL       string
	registrationErr error
}

func (f *mailFake) Enabled() bool { return !f.disabled }

func (f *mailFake) PrepareInvitation(invitation *model.Invitation, actionURL string) (*store.PreparedMail, error) {
	f.issueURL = actionURL
	return invitationPreparedMail(invitation.InviterUserID, "", invitation.ID, model.MailOccurrenceID(invitation.ID.String()), model.MailTemplateAccessStudentClassInvitation, invitation.CreatedAt, invitation.ExpiresAt), nil
}

func (f *mailFake) PrepareInvitationResend(invitation *model.Invitation, actionURL string, actor model.UserID, at time.Time) (*store.PreparedMail, error) {
	f.issueURL = actionURL
	return invitationPreparedMail(actor, "", invitation.ID, model.NewMailOccurrenceID(), model.MailTemplateAccessStudentClassInvitation, at, invitation.ExpiresAt), nil
}

func (f *mailFake) PrepareInvitationRevocation(invitation *model.Invitation, actor model.UserID, at time.Time) (*store.PreparedMail, error) {
	prepared := invitationPreparedMail(actor, "", invitation.ID, model.NewMailOccurrenceID(), model.MailTemplateAccessInvitationRevoked, at, at.Add(24*time.Hour))
	command, _ := model.EncodeMailDeliveryCommand(model.MailDeliveryCommand{DeliveryID: prepared.Delivery.ID})
	prepared.Job, _ = model.NewJob(prepared.Job.ID, model.JobTypeMailDeliver, command, prepared.Delivery.ID.String(), at, at, model.MailMaximumAttempts)
	return prepared, nil
}

func (f *mailFake) PrepareInvitationAccepted(request mail.NoticePreparation) (*store.PreparedMail, error) {
	if f.directErr != nil {
		return nil, f.directErr
	}
	f.directJobType = model.JobTypeMailDeliver
	prepared := invitationPreparedMail(request.Recipient.ID, request.Recipient.ID, "", model.NewMailOccurrenceID(),
		model.MailTemplateAccessInvitationAccepted, request.At, request.At.Add(24*time.Hour))
	command, _ := model.EncodeMailDeliveryCommand(model.MailDeliveryCommand{DeliveryID: prepared.Delivery.ID})
	prepared.Job, _ = model.NewJob(prepared.Job.ID, model.JobTypeMailDeliver, command, prepared.Delivery.ID.String(), request.At, request.At, model.MailMaximumAttempts)
	if f.disabled {
		prepared.Job, _ = prepared.Job.RequestCancellation(request.At)
		prepared.Delivery.State = model.MailDeliverySuppressed
		prepared.Delivery.PublicFailureCode = model.MailDeliveryDisabledCode
		prepared.Delivery.EncryptedPayload = nil
	}
	return prepared, nil
}

func invitationPreparedMail(actor, targetUser model.UserID, targetInvitation model.InvitationID, occurrenceID model.MailOccurrenceID, key model.MailTemplateKey, at, deadline time.Time) *store.PreparedMail {
	deliveryID, jobID := model.NewMailDeliveryID(), model.NewJobID()
	command, _ := model.EncodeMailDeliveryCommand(model.MailDeliveryCommand{DeliveryID: deliveryID})
	job, _ := model.NewJob(jobID, model.JobTypeMailDeliverCredential, command, deliveryID.String(), at, at, model.MailMaximumAttempts)
	return &store.PreparedMail{
		Occurrence: &model.MailOccurrence{ID: occurrenceID, Kind: model.MailOccurrenceInvitation, TemplateKey: key, ActorUserID: actor, CreatedAt: at},
		Delivery: &model.MailDelivery{ID: deliveryID, OccurrenceID: occurrenceID, JobID: jobID, TargetUserID: targetUser,
			TargetInvitationID: targetInvitation, TemplateKey: key, TemplateDigest: strings.Repeat("d", 64),
			MaskedRecipient: "s***@example.edu", State: model.MailDeliveryQueued, CreatedAt: at, UpdatedAt: at,
			MessageDate: at, Deadline: deadline, MessageID: "<invite." + deliveryID.String() + "@example.test>",
			EncryptedPayload: []byte(`{"version":1,"key_id":"11111111111111111111111111111111","ciphertext":"sealed"}`), Revision: 1},
		Job: job,
	}
}

func newInvitationServiceForTest(t *testing.T, persistence store.InvitationStore, units store.AcademicUnitStore,
	roles store.RoleStore, authorization resourceAuthorizer, composer mailComposer, now time.Time,
) *Service {
	t.Helper()
	events := []string{}
	return &Service{invitations: persistence, classes: invitationClassStoreFake{}, periods: invitationPeriodStoreFake{}, academicUnits: units, roles: roles,
		authorizer: authorization, mail: composer, hasher: passwordHasherFake{}, auditor: &mutationAttemptAuditorFake{events: &events, beginID: model.NewAuditEventID().String()},
		attempts: testAttempts(t), rateLimit: testRateLimit, publisher: &realtimetest.Publisher{}, nodeID: "node-1", publicURL: "https://proctor.example.edu",
		recentAuthenticationTTL: 15 * time.Minute, newToken: model.NewCredentialToken, now: func() time.Time { return now }}
}

func TestInvitationServiceIssuesAndAcceptsWithoutPersistingRawClaim(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	periodID, classID, inviterID := model.NewAcademicPeriodID(), model.NewClassID(), model.NewUserID()
	period := &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour), EndsAt: now.Add(180 * 24 * time.Hour)}
	class := &model.Class{ID: classID, AcademicPeriodID: periodID}
	persistence := &invitationStoreFake{}
	authorizer := &authorizerFake{}
	composer := &mailFake{}
	events := []string{}
	auditor := &mutationAttemptAuditorFake{events: &events, beginID: model.NewAuditEventID().String()}
	raw := model.NewCredentialToken()
	service := &Service{invitations: persistence, classes: invitationClassStoreFake{class: class}, periods: invitationPeriodStoreFake{period: period}, academicUnits: invitationAcademicUnitStoreFake{}, roles: invitationRoleStoreFake{}, authorizer: authorizer, mail: composer, hasher: passwordHasherFake{}, auditor: auditor, attempts: testAttempts(t), rateLimit: testRateLimit, publisher: &realtimetest.Publisher{}, nodeID: "node-1", publicURL: "https://proctor.example.edu", recentAuthenticationTTL: 15 * time.Minute, newToken: func() string { return raw }, now: func() time.Time { return now }}
	call := invocation.New(model.Principal{UserID: inviterID}, model.RequestMetadata{})
	view, err := service.IssueStudentClassInvitation(context.Background(), call, IssueStudentClassInvitationCommand{
		TargetEmail: " Student@Example.edu ", ClassID: classID, IntendedStartsAt: model.MillisFromTime(period.StartsAt),
		IntendedEndsAt: model.MillisFromTime(period.EndsAt), SuggestedUsername: "student-one", SuggestedDisplayName: "Student One", SuggestedLocale: "en",
	})
	require.NoError(t, err)
	require.Equal(t, persistence.invitation.ID, view.ID, "unsafe or incomplete issue: view=%#v input=%#v url", view, persistence.issued)
	require.NotContains(t, view.String(), raw, "unsafe or incomplete issue: view=%#v input=%#v url", view, persistence.issued)
	require.Equal(t, model.HashInvitationClaim(raw), persistence.issued.Invitation.ClaimHash, "unsafe or incomplete issue: view=%#v input=%#v url", view, persistence.issued)
	require.NotContains(t, string(persistence.issued.Delivery.EncryptedPayload), raw, "unsafe or incomplete issue: view=%#v input=%#v url", view, persistence.issued)
	require.Contains(t, composer.issueURL, "/join#token=", "unsafe or incomplete issue: view=%#v input=%#v url", view, persistence.issued)
	require.Len(t, authorizer.actions, 2)
	require.Equal(t, model.ActionInvitationCreate, authorizer.actions[0])
	require.Equal(t, model.ActionClassMembersManage, authorizer.actions[1])
	result, err := service.AcceptStudentClassInvitation(context.Background(), invocation.Invocation{}, AcceptStudentClassInvitationCommand{
		Claim: raw, Password: "correct horse battery staple", Username: "student-one", DisplayName: "Student One", Locale: "en",
	})
	require.NoError(t, err)
	require.NotNil(t, result.User, "acceptance result/input = %#v / %#v", result, persistence.accepted)
	require.Equal(t, "student@example.edu", result.User.Email, "acceptance result/input = %#v / %#v", result, persistence.accepted)
	require.True(t, result.User.EmailVerified, "acceptance result/input = %#v / %#v", result, persistence.accepted)
	require.Equal(t, "encoded:correct horse battery staple", persistence.accepted.PasswordCredential.PasswordHash, "acceptance result/input = %#v / %#v", result, persistence.accepted)
	require.Equal(t, model.HashInvitationClaim(raw), persistence.accepted.ClaimHash, "acceptance result/input = %#v / %#v", result, persistence.accepted)
}
