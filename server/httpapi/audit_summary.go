// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"sort"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// An audit summary is one sentence the server writes for an audit row in the
// request's locale, beside the row's stable codes. Its message is chosen by
// the row's catalogue key alone, and its arguments are noun phrases: a
// current name the application read for the caller, or a generic noun ("a
// class", "a user") when the caller could not see that record now. A summary
// therefore never says more than the row's own fields and the caller's
// present visibility.
//
// The institution audit list keys a summary by action, or by action and
// recorded operation where the self-history catalogue distinguishes
// operations (model.AuditSelfEventKeys); a redacted academic row carries no
// parameters, so it always reads the action's message and names no subject.
// Self history keys a summary by event name, with one message for an event
// about the caller's account and another for one the caller performed on
// anyone or nothing else; its actor is a kind, never a person.
const (
	auditSummaryActorHidden = "audit.actor.hidden"
	auditSummaryActorSystem = "audit.actor.system"
	auditSummarySubject     = "audit.subject.hidden"
	auditSummaryUnknown     = "audit.resource.unknown"
	auditSummaryRoleNamed   = "audit.self.role.named"
	auditSummaryRoleUnnamed = "audit.self.role.unnamed"
)

// auditOperation is one action and the operation its writer recorded.
type auditOperation struct {
	action    model.Action
	operation string
}

// selfAuditSummaryVariableSet declares the variables of an event's two
// self-history messages: about the caller's account, and performed by the
// caller on anyone or nothing else.
type selfAuditSummaryVariableSet struct {
	about []string
	other []string
}

// auditSummaryStatuses are the outcomes whose summary wraps the action's
// sentence; a successful row reads the sentence alone.
var auditSummaryStatuses = []model.AuditStatus{model.AuditStatusAttempt, model.AuditStatusFail}

// auditSummaryLocalizationDefinitions declares every message an audit
// summary can read, with its template variables.
func auditSummaryLocalizationDefinitions() []localization.Definition {
	plain := []string{auditSummaryActorHidden, auditSummaryActorSystem, auditSummarySubject, auditSummaryUnknown, auditSummaryRoleUnnamed}
	for _, resourceType := range model.ResourceTypes() {
		plain = append(plain, auditResourceNounID(resourceType))
	}
	for _, kind := range []administration.SelfAuditActorKind{
		administration.SelfAuditActorSelf,
		administration.SelfAuditActorAdministrator,
		administration.SelfAuditActorSystem,
	} {
		plain = append(plain, selfAuditActorID(kind))
	}

	definitions := make([]localization.Definition, 0, len(plain)+len(auditActionSummaryVariables)+
		len(auditOperationSummaryVariables)+len(selfAuditSummaryVariables)*2+len(auditSummaryStatuses)+1)
	for _, id := range plain {
		definitions = append(definitions, localization.Definition{ID: id, Origin: localizationOrigin})
	}
	definitions = append(definitions, localization.Definition{ID: auditSummaryRoleNamed, Origin: localizationOrigin, Variables: []string{"Name"}})
	for _, status := range auditSummaryStatuses {
		definitions = append(definitions, localization.Definition{
			ID: auditStatusSummaryID(status), Origin: localizationOrigin, Variables: []string{"Summary"},
		})
	}
	for action, variables := range auditActionSummaryVariables {
		definitions = append(definitions, localization.Definition{ID: auditActionSummaryID(action), Origin: localizationOrigin, Variables: variables})
	}
	for key, variables := range auditOperationSummaryVariables {
		definitions = append(definitions, localization.Definition{ID: auditOperationSummaryID(key), Origin: localizationOrigin, Variables: variables})
	}
	for event, variables := range selfAuditSummaryVariables {
		definitions = append(definitions,
			localization.Definition{ID: selfAuditSummaryID(event, store.AuditSelfRelationSubject), Origin: localizationOrigin, Variables: variables.about},
			localization.Definition{ID: selfAuditSummaryID(event, store.AuditSelfRelationActor), Origin: localizationOrigin, Variables: variables.other},
		)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	return definitions
}

func auditActionSummaryID(action model.Action) string { return "audit.action." + string(action) }

func auditOperationSummaryID(key auditOperation) string {
	return "audit.operation." + string(key.action) + "." + key.operation
}

func auditStatusSummaryID(status model.AuditStatus) string { return "audit.status." + string(status) }

func auditResourceNounID(resourceType model.ResourceType) string {
	return "audit.resource." + string(resourceType)
}

func selfAuditActorID(kind administration.SelfAuditActorKind) string {
	return "audit.self.actor." + string(kind)
}

func selfAuditSummaryID(event model.AuditSelfEvent, relation store.AuditSelfRelation) string {
	if relation == store.AuditSelfRelationActor {
		return "audit.self.event." + string(event) + ".other"
	}
	return "audit.self.event." + string(event)
}

// auditSummary writes the institution audit list's sentence for one event.
func auditSummary(request *http.Request, event *model.AuditEvent, names store.AuditNames) string {
	id := auditActionSummaryID(model.Action(event.Action))
	if key := (auditOperation{action: model.Action(event.Action), operation: event.Operation()}); key.operation != "" {
		if _, known := auditOperationSummaryVariables[key]; known {
			id = auditOperationSummaryID(key)
		}
	}

	actor := route.Translate(request, auditSummaryActorSystem, "")
	if !event.ActorID.IsZero() {
		actor = names.Users[event.ActorID]
		if actor == "" {
			actor = route.Translate(request, auditSummaryActorHidden, "")
		}
	}
	subject := names.Users[event.SubjectUserID]
	if event.SubjectUserID.IsZero() || subject == "" {
		subject = route.Translate(request, auditSummarySubject, "")
	}

	summary := route.TranslateRequest(request, localization.Request{ID: id, TemplateData: map[string]string{
		"Actor":    actor,
		"Subject":  subject,
		"Resource": auditResourceName(request, event.Resource, names),
	}}, event.Action)
	return auditStatusSummary(request, event.Status, summary)
}

// auditResourceName is the current name of a User, Academic Unit, or Class
// resource the caller may see, or the generic noun of its type.
func auditResourceName(request *http.Request, resource model.Resource, names store.AuditNames) string {
	var name string
	switch resource.Type {
	case model.ResourceUser:
		name = names.Users[model.UserID(resource.ID)]
	case model.ResourceAcademicUnit:
		name = names.AcademicUnits[model.AcademicUnitID(resource.ID)]
	case model.ResourceClass:
		name = names.Classes[model.ClassID(resource.ID)]
	}
	if name != "" {
		return name
	}
	return auditResourceNoun(request, resource.Type)
}

func auditResourceNoun(request *http.Request, resourceType model.ResourceType) string {
	return route.Translate(request, auditResourceNounID(resourceType), route.Translate(request, auditSummaryUnknown, ""))
}

// auditStatusSummary wraps the sentence of an unfinished or failed row, so a
// summary never reads as done when it was not.
func auditStatusSummary(request *http.Request, status model.AuditStatus, summary string) string {
	if status == model.AuditStatusSuccess {
		return summary
	}
	return route.TranslateRequest(request, localization.Request{
		ID: auditStatusSummaryID(status), TemplateData: map[string]string{"Summary": summary},
	}, summary)
}

// selfAuditSummary writes the self history's sentence for one event. The
// actor is "you", "an administrator", or "the system"; the resource is named
// only where the projection kept it, and the Role only when it was bound to
// the caller.
func selfAuditSummary(request *http.Request, event administration.SelfAuditEvent) string {
	resource := event.ResourceName
	if resource == "" {
		resource = auditResourceNoun(request, event.Resource.Type)
	}

	role := route.Translate(request, auditSummaryRoleUnnamed, "")
	if event.Role.DisplayName != "" {
		name := event.Role.DisplayName
		if event.Role.Name == model.SystemAdministratorRoleName {
			name = route.Translate(request, systemAdministratorRoleNameID, name)
		}
		role = route.TranslateRequest(request, localization.Request{
			ID: auditSummaryRoleNamed, TemplateData: map[string]string{"Name": name},
		}, name)
	}

	summary := route.TranslateRequest(request, localization.Request{
		ID: selfAuditSummaryID(event.Event, event.Relation),
		TemplateData: map[string]string{
			"Actor":    route.Translate(request, selfAuditActorID(event.ActorKind), ""),
			"Resource": resource,
			"Role":     role,
		},
	}, string(event.Event))
	if event.Event == model.AuditSelfEventSignInFailed {
		// The event name already says the sign-in failed.
		return summary
	}
	return auditStatusSummary(request, event.Status, summary)
}

// The variables each summary message uses. A test pins these tables to the
// action catalogue, the self-history operations, and the self-history events,
// and construction refuses an English message whose placeholders differ.

var auditActionSummaryVariables = map[model.Action][]string{
	model.ActionAcademicProgressionManage:               {"Actor", "Resource"},
	model.ActionAcademicAuditView:                       {"Actor"},
	model.ActionAcademicPeriodManage:                    {"Actor", "Resource"},
	model.ActionAcademicPeriodView:                      {"Actor", "Resource"},
	model.ActionAcademicUnitManage:                      {"Actor", "Resource"},
	model.ActionAcademicUnitMembersManage:               {"Actor", "Resource"},
	model.ActionAcademicUnitMembersView:                 {"Actor", "Resource"},
	model.ActionAcademicUnitView:                        {"Actor", "Resource"},
	model.ActionAccessPolicyManage:                      {"Actor"},
	model.ActionAccessPolicyView:                        {"Actor"},
	model.ActionAuditView:                               {"Actor"},
	model.ActionAuthenticationAdministratorMFAReset:     {"Actor", "Resource"},
	model.ActionAuthenticationAdministratorRecovery:     {"Actor", "Resource"},
	model.ActionAuthenticationDesktopAuthorization:      {"Actor"},
	model.ActionAuthenticationDesktopSessionIssue:       {"Actor"},
	model.ActionAuthenticationEmailChangeUndo:           {"Resource"},
	model.ActionAuthenticationEmailVerificationComplete: {"Resource"},
	model.ActionAuthenticationEmailVerificationRequest:  {"Resource"},
	model.ActionAuthenticationExternalLogin:             {"Resource"},
	model.ActionAuthenticationExternalProvision:         {"Resource"},
	model.ActionAuthenticationLogin:                     {"Resource"},
	model.ActionAuthenticationPasswordResetComplete:     {"Resource"},
	model.ActionAuthenticationPasswordResetRequest:      {"Resource"},
	model.ActionAuthenticationPublicRegistration:        {"Resource"},
	model.ActionAuthenticationReauthenticate:            {"Actor"},
	model.ActionClassManage:                             {"Actor", "Resource"},
	model.ActionClassMembersManage:                      {"Actor", "Resource"},
	model.ActionClassMembersView:                        {"Actor", "Resource"},
	model.ActionClassView:                               {"Actor", "Resource"},
	model.ActionDesktopCompatibilityPolicyManage:        {"Actor"},
	model.ActionDesktopRegistrationRename:               {"Actor", "Resource"},
	model.ActionDesktopRegistrationRevoke:               {"Actor", "Resource"},
	model.ActionExamAttemptBrowserActivityView:          {"Actor", "Resource"},
	model.ActionExamCreate:                              {"Actor", "Resource"},
	model.ActionExamCreateOverride:                      {"Actor", "Resource"},
	model.ActionExamManage:                              {"Actor", "Resource"},
	model.ActionExamManageOverride:                      {"Actor", "Resource"},
	model.ActionExamPublish:                             {"Actor", "Resource"},
	model.ActionExamPublishOverride:                     {"Actor", "Resource"},
	model.ActionExamRecordsComplete:                     {"Actor", "Resource"},
	model.ActionExamRecordsCompleteOverride:             {"Actor", "Resource"},
	model.ActionExamRecordsExport:                       {"Actor", "Resource"},
	model.ActionExamRecordsExportOverride:               {"Actor", "Resource"},
	model.ActionExamRecordsHold:                         {"Actor", "Resource"},
	model.ActionExamRecordsHoldOverride:                 {"Actor", "Resource"},
	model.ActionExamSittingCreate:                       {"Actor", "Resource"},
	model.ActionExamSittingCreateOverride:               {"Actor", "Resource"},
	model.ActionExamSittingManage:                       {"Actor", "Resource"},
	model.ActionExamSittingManageOverride:               {"Actor", "Resource"},
	model.ActionExamSittingParticipate:                  {"Actor", "Resource"},
	model.ActionExamSittingView:                         {"Actor", "Resource"},
	model.ActionExamSittingViewOverride:                 {"Actor", "Resource"},
	model.ActionExamView:                                {"Actor", "Resource"},
	model.ActionExamViewOverride:                        {"Actor", "Resource"},
	model.ActionExternalIdentityManage:                  {"Actor", "Resource"},
	model.ActionInstallationBootstrap:                   {"Actor"},
	model.ActionInstitutionManage:                       {"Actor"},
	model.ActionInvitationAccept:                        {"Actor"},
	model.ActionInvitationCreate:                        {"Actor", "Resource"},
	model.ActionInvitationManage:                        {"Actor", "Resource"},
	model.ActionInvitationView:                          {"Actor", "Resource"},
	model.ActionJobManage:                               {"Actor"},
	model.ActionJobView:                                 {"Actor"},
	model.ActionMailManage:                              {"Actor"},
	model.ActionMailRekey:                               {"Actor"},
	model.ActionMailView:                                {"Actor"},
	model.ActionMFAActivate:                             {"Actor"},
	model.ActionMFAChallenge:                            {"Actor"},
	model.ActionMFADisable:                              {"Actor"},
	model.ActionMFARecoveryCodesRegenerate:              {"Actor"},
	model.ActionMFARekey:                                {"Actor"},
	model.ActionMFASetup:                                {"Actor"},
	model.ActionOnboardingBatchManage:                   {"Actor", "Resource"},
	model.ActionOnboardingBatchView:                     {"Actor", "Resource"},
	model.ActionPersonalAccessTokenCreate:               {"Actor"},
	model.ActionPersonalAccessTokenDisable:              {"Actor"},
	model.ActionPersonalAccessTokenEnable:               {"Actor"},
	model.ActionPersonalAccessTokenRevoke:               {"Actor"},
	model.ActionProgrammeManage:                         {"Actor", "Resource"},
	model.ActionProgrammeView:                           {"Actor", "Resource"},
	model.ActionProgrammeLevelManage:                    {"Actor", "Resource"},
	model.ActionProgrammeLevelView:                      {"Actor", "Resource"},
	model.ActionRetentionCleanupManage:                  {"Actor"},
	model.ActionRetentionHoldRelease:                    {"Actor", "Resource"},
	model.ActionRetentionPolicyManage:                   {"Actor"},
	model.ActionRetentionPolicyView:                     {"Actor"},
	model.ActionRoleManage:                              {"Actor"},
	model.ActionRoleSystemAdminReconcile:                {"Actor"},
	model.ActionRoleView:                                {"Actor"},
	model.ActionRoleBindingManage:                       {"Actor", "Resource"},
	model.ActionRoleBindingView:                         {"Actor", "Resource"},
	model.ActionSessionManage:                           {"Actor", "Resource"},
	model.ActionSessionView:                             {"Actor", "Resource"},
	model.ActionSubmissionRelease:                       {"Actor", "Resource"},
	model.ActionSubmissionReleaseOverride:               {"Actor", "Resource"},
	model.ActionSubmissionReview:                        {"Actor", "Resource"},
	model.ActionSubmissionReviewOverride:                {"Actor", "Resource"},
	model.ActionSubmissionView:                          {"Actor", "Resource"},
	model.ActionSubmissionViewOverride:                  {"Actor", "Resource"},
	model.ActionUserManage:                              {"Actor", "Resource"},
	model.ActionUserMFAReset:                            {"Actor", "Resource"},
	model.ActionUserProfilePictureManage:                {"Actor", "Resource"},
	model.ActionUserSearch:                              {"Actor"},
	model.ActionUserSettingsReplace:                     {"Actor"},
	model.ActionUserView:                                {"Actor", "Resource"},
}

var auditOperationSummaryVariables = map[auditOperation][]string{
	{model.ActionAcademicUnitMembersManage, "create_member"}:         {"Actor", "Resource", "Subject"},
	{model.ActionAcademicUnitMembersManage, "end_member"}:            {"Actor", "Resource", "Subject"},
	{model.ActionAuthenticationDesktopSessionIssue, "exchange"}:      {"Actor"},
	{model.ActionClassMembersManage, "end"}:                          {"Actor", "Resource", "Subject"},
	{model.ActionClassMembersManage, "enroll"}:                       {"Actor", "Resource", "Subject"},
	{model.ActionClassMembersManage, "transfer_in"}:                  {"Actor", "Resource", "Subject"},
	{model.ActionClassMembersManage, "transfer_out"}:                 {"Actor", "Resource", "Subject"},
	{model.ActionDesktopRegistrationRename, "rename"}:                {"Actor", "Subject"},
	{model.ActionDesktopRegistrationRevoke, "revoke"}:                {"Actor", "Subject"},
	{model.ActionExternalIdentityManage, "change_password"}:          {"Actor", "Subject"},
	{model.ActionExternalIdentityManage, "enroll_password"}:          {"Actor", "Subject"},
	{model.ActionExternalIdentityManage, "remove_password"}:          {"Actor", "Subject"},
	{model.ActionExternalIdentityManage, "unlink_provider"}:          {"Actor", "Subject"},
	{model.ActionInvitationAccept, "accept_scoped_role_invitation"}:  {"Actor"},
	{model.ActionRoleBindingManage, "create_binding"}:                {"Actor", "Resource", "Subject"},
	{model.ActionRoleBindingManage, "end_binding"}:                   {"Actor", "Resource", "Subject"},
	{model.ActionSessionManage, "logout"}:                            {"Actor"},
	{model.ActionSessionManage, "rename_own_session"}:                {"Actor"},
	{model.ActionSessionManage, "revoke_other_sessions"}:             {"Actor"},
	{model.ActionSessionManage, "revoke_own_session"}:                {"Actor"},
	{model.ActionSessionManage, "revoke_own_sessions"}:               {"Actor"},
	{model.ActionSessionManage, "revoke_session"}:                    {"Actor", "Subject"},
	{model.ActionSessionManage, "revoke_sessions"}:                   {"Actor", "Subject"},
	{model.ActionUserManage, "change_email"}:                         {"Actor", "Subject"},
	{model.ActionUserManage, "create"}:                               {"Actor", "Subject"},
	{model.ActionUserManage, "disable_account"}:                      {"Actor", "Subject"},
	{model.ActionUserManage, "enable_account"}:                       {"Actor", "Subject"},
	{model.ActionUserManage, "end"}:                                  {"Actor", "Subject"},
	{model.ActionUserManage, "remove_profile_picture"}:               {"Actor", "Subject"},
	{model.ActionUserManage, "set_profile_picture"}:                  {"Actor", "Subject"},
	{model.ActionUserManage, "update_profile"}:                       {"Actor", "Subject"},
	{model.ActionUserManage, "verify_email_privileged"}:              {"Actor", "Subject"},
	{model.ActionUserProfilePictureManage, "remove_profile_picture"}: {"Actor", "Subject"},
	{model.ActionUserProfilePictureManage, "set_profile_picture"}:    {"Actor", "Subject"},
}

var selfAuditSummaryVariables = map[model.AuditSelfEvent]selfAuditSummaryVariableSet{
	model.AuditSelfEventAcademicUnitJoined:              {about: []string{"Actor", "Resource"}, other: []string{"Resource"}},
	model.AuditSelfEventAcademicUnitLeft:                {about: []string{"Actor", "Resource"}, other: []string{"Resource"}},
	model.AuditSelfEventAccountDisabled:                 {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventAccountEnabled:                  {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventAccountRecoveredByAdministrator: {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventAffiliationEnded:                {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventAffiliationStarted:              {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventClassEnrolled:                   {about: []string{"Actor", "Resource"}, other: []string{"Resource"}},
	model.AuditSelfEventClassTransferredIn:              {about: []string{"Actor", "Resource"}, other: []string{"Resource"}},
	model.AuditSelfEventClassTransferredOut:             {about: []string{"Actor", "Resource"}, other: []string{"Resource"}},
	model.AuditSelfEventClassUnenrolled:                 {about: []string{"Actor", "Resource"}, other: []string{"Resource"}},
	model.AuditSelfEventDesktopRegistrationRenamed:      {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventDesktopRegistrationRevoked:      {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventDesktopSignedIn:                 {about: nil, other: nil},
	model.AuditSelfEventEmailChangeUndone:               {about: nil, other: nil},
	model.AuditSelfEventEmailChanged:                    {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventEmailVerified:                   {about: nil, other: nil},
	model.AuditSelfEventInvitationAccepted:              {about: nil, other: nil},
	model.AuditSelfEventMFADisabled:                     {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventMFAEnabled:                      {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventMFARecoveryCodesRegenerated:     {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventMFAResetByAdministrator:         {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventMFASetupStarted:                 {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventPasswordChanged:                 {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventPasswordEnrolled:                {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventPasswordRemoved:                 {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventPasswordReset:                   {about: nil, other: nil},
	model.AuditSelfEventTokenCreated:                    {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventTokenDisabled:                   {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventTokenEnabled:                    {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventTokenRevoked:                    {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventProfileChanged:                  {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventProfilePictureChanged:           {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventProfilePictureRemoved:           {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventProviderUnlinked:                {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventRoleEnded:                       {about: []string{"Actor", "Resource", "Role"}, other: []string{"Resource"}},
	model.AuditSelfEventRoleGranted:                     {about: []string{"Actor", "Resource", "Role"}, other: []string{"Resource"}},
	model.AuditSelfEventSessionRenamed:                  {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventSessionRevoked:                  {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventSessionsRevokedAll:              {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventSessionsRevokedOthers:           {about: []string{"Actor"}, other: nil},
	model.AuditSelfEventSignInFailed:                    {about: nil, other: nil},
	model.AuditSelfEventSignInSucceeded:                 {about: nil, other: nil},
	model.AuditSelfEventSignedOut:                       {about: nil, other: nil},
}
