// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the Apache License, Version 2.0.
// See LICENSES/Apache-2.0.txt and NOTICE in the server module root for
// license and attribution information.
// SPDX-License-Identifier: Apache-2.0
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/public/model/permission.go. Proctor keeps
// stable, action-oriented permission identifiers while defining inheritance
// explicitly for its institution and academic hierarchy.

package model

import (
	"slices"
	"sort"
)

// Action is a stable authorization identifier for a domain capability, never
// an HTTP method or route name.
type Action string

const (
	ActionInstitutionManage              Action = "institution.manage"
	ActionRoleManage                     Action = "role.manage"
	ActionAuditView                      Action = "audit.view"
	ActionAcademicAuditView              Action = "academic_audit.view"
	ActionUserView                       Action = "user.view"
	ActionUserManage                     Action = "user.manage"
	ActionUserProfilePictureManage       Action = "user.profile_picture.manage"
	ActionSessionView                    Action = "session.view"
	ActionSessionManage                  Action = "session.manage"
	ActionDesktopRegistrationRevoke      Action = "desktop_registration.revoke"
	ActionJobView                        Action = "job.view"
	ActionJobManage                      Action = "job.manage"
	ActionMailView                       Action = "mail.view"
	ActionMailManage                     Action = "mail.manage"
	ActionMailRekey                      Action = "mail.rekey"
	ActionMFARekey                       Action = "mfa.rekey"
	ActionExamCreate                     Action = "exam.create"
	ActionExamCreateOverride             Action = "exam.create.override"
	ActionExamView                       Action = "exam.view"
	ActionExamViewOverride               Action = "exam.view.override"
	ActionExamManage                     Action = "exam.manage"
	ActionExamManageOverride             Action = "exam.manage.override"
	ActionExamPublish                    Action = "exam.publish"
	ActionExamPublishOverride            Action = "exam.publish.override"
	ActionExamSittingCreate              Action = "exam.sitting.create"
	ActionExamSittingCreateOverride      Action = "exam.sitting.create.override"
	ActionExamSittingView                Action = "exam.sitting.view"
	ActionExamSittingViewOverride        Action = "exam.sitting.view.override"
	ActionExamSittingManage              Action = "exam.sitting.manage"
	ActionExamSittingManageOverride      Action = "exam.sitting.manage.override"
	ActionExamSittingParticipate         Action = "exam.sitting.participate"
	ActionExamAttemptBrowserActivityView Action = "exam.attempt.browser_activity.view"
	ActionSubmissionView                 Action = "submission.view"
	ActionSubmissionViewOverride         Action = "submission.view.override"
	ActionSubmissionReview               Action = "submission.review"
	ActionSubmissionReviewOverride       Action = "submission.review.override"
	ActionSubmissionRelease              Action = "submission.release"
	ActionSubmissionReleaseOverride      Action = "submission.release.override"
	ActionExamRecordsComplete            Action = "exam.records.complete"
	ActionExamRecordsCompleteOverride    Action = "exam.records.complete.override"
	ActionExamRecordsExport              Action = "exam.records.export"
	ActionExamRecordsExportOverride      Action = "exam.records.export.override"
	ActionExamRecordsHold                Action = "exam.records.hold"
	ActionExamRecordsHoldOverride        Action = "exam.records.hold.override"
	ActionRetentionHoldRelease           Action = "retention_hold.release"

	ActionAcademicUnitView          Action = "academic_unit.view"
	ActionAcademicUnitManage        Action = "academic_unit.manage"
	ActionAcademicUnitMembersView   Action = "academic_unit.members.view"
	ActionAcademicUnitMembersManage Action = "academic_unit.members.manage"
	ActionAcademicPeriodView        Action = "academic_period.view"
	ActionAcademicPeriodManage      Action = "academic_period.manage"
	ActionProgrammeView             Action = "programme.view"
	ActionProgrammeManage           Action = "programme.manage"
	ActionProgrammeLevelView        Action = "programme_level.view"
	ActionProgrammeLevelManage      Action = "programme_level.manage"

	ActionClassView                 Action = "class.view"
	ActionClassManage               Action = "class.manage"
	ActionClassMembersView          Action = "class.members.view"
	ActionClassMembersManage        Action = "class.members.manage"
	ActionAcademicProgressionManage Action = "academic.progression.manage"

	ActionAccessPolicyView                 Action = "access_policy.view"
	ActionAccessPolicyManage               Action = "access_policy.manage"
	ActionDesktopCompatibilityPolicyManage Action = "desktop_compatibility_policy.manage"
	// ActionPluginManage views and changes the plugins compiled into the
	// server: whether each one runs, and its settings.
	ActionPluginManage Action = "plugin.manage"
	// ActionEnvironmentProfileManage views, creates and retires the profiles
	// exams' execution environments are created from.
	ActionEnvironmentProfileManage Action = "environment_profile.manage"
	ActionAssistantProfileManage   Action = "assistant_profile.manage"
	ActionRetentionPolicyView      Action = "retention_policy.view"
	ActionRetentionPolicyManage    Action = "retention_policy.manage"
	ActionRetentionCleanupManage   Action = "retention_cleanup.manage"
	ActionUserMFAReset             Action = "user.mfa.reset"
	ActionInvitationView           Action = "invitation.view"
	ActionInvitationCreate         Action = "invitation.create"
	ActionInvitationManage         Action = "invitation.manage"
	ActionOnboardingBatchView      Action = "onboarding_batch.view"
	ActionOnboardingBatchManage    Action = "onboarding_batch.manage"
	ActionExternalIdentityManage   Action = "external_identity.manage"
	ActionRoleView                 Action = "role.view"
	ActionRoleBindingView          Action = "role_binding.view"
	ActionRoleBindingManage        Action = "role_binding.manage"
)

// ResourceType identifies an authorization target.
type ResourceType string

const (
	ResourceInstitution    ResourceType = "institution"
	ResourceAcademicUnit   ResourceType = "academic_unit"
	ResourceProgramme      ResourceType = "programme"
	ResourceProgrammeLevel ResourceType = "programme_level"
	ResourceAcademicPeriod ResourceType = "academic_period"
	ResourceClass          ResourceType = "class"
	ResourceUser           ResourceType = "user"
	ResourceExam           ResourceType = "exam"
	ResourceExamSitting    ResourceType = "exam_sitting"
	ResourceSubmission     ResourceType = "submission"
	ResourceMailDelivery   ResourceType = "mail_delivery"
)

// ResourceTypes returns the closed resource catalogue in lexical order.
func ResourceTypes() []ResourceType {
	return slices.Clone(resourceTypes)
}

// The contract's ResourceType enum is kept equal to resourceTypes.
var resourceTypes = []ResourceType{
	ResourceAcademicPeriod,
	ResourceAcademicUnit,
	ResourceClass,
	ResourceExam,
	ResourceExamSitting,
	ResourceInstitution,
	ResourceMailDelivery,
	ResourceProgramme,
	ResourceProgrammeLevel,
	ResourceSubmission,
	ResourceUser,
}

// Resource is the concrete object an action is checked against.
type Resource struct {
	Type ResourceType
	ID   string
}

// ActionDefinition says which resources an action accepts and which role
// scopes may grant it. The registry is closed on purpose: an unknown
// permission stored on a role may survive a downgrade, but it authorizes
// nothing until the registry knows it.
type ActionDefinition struct {
	Action                    Action
	ResourceType              ResourceType
	CompatibleResourceTypes   []ResourceType
	InheritInstitutionScope   bool
	InheritAcademicUnitScopes bool
	// RelationshipOnly marks a capability established from current domain
	// state; no role or personal access token scope can ever grant it.
	RelationshipOnly bool
	// PersonalAccessTokenForbidden keeps sensitive administration behind an
	// interactive session, though roles may still grant it.
	PersonalAccessTokenForbidden bool
	// SystemAdministratorOnly keeps the action in the protected built-in role;
	// custom roles and token scopes cannot delegate it.
	SystemAdministratorOnly bool
	// AcademicAudit admits the action's events to academic_audit.view.
	AcademicAudit bool
}

// AcceptsResource reports whether the action may authorize resourceType,
// either its own target or an owning scope used for a create or a bounded
// collection.
func (d ActionDefinition) AcceptsResource(resourceType ResourceType) bool {
	return d.ResourceType == resourceType || slices.Contains(d.CompatibleResourceTypes, resourceType)
}

// SupportsRoleScope reports whether a role binding at scopeType can grant the
// action. It differs from AcceptsResource: a binding at an academic unit may
// grant an action on a descendant exam, sitting, submission, programme or
// class.
func (d ActionDefinition) SupportsRoleScope(scopeType RoleScopeType) bool {
	switch scopeType {
	case RoleScopeInstitution:
		return d.InheritInstitutionScope
	case RoleScopeAcademicUnit:
		return d.InheritAcademicUnitScopes
	case RoleScopeClass:
		return d.AcceptsResource(ResourceClass)
	default:
		return false
	}
}

var actionDefinitions = map[Action]ActionDefinition{
	ActionInstitutionManage: {
		Action:                  ActionInstitutionManage,
		ResourceType:            ResourceInstitution,
		InheritInstitutionScope: true,
	},
	ActionRoleManage: {
		Action:                       ActionRoleManage,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
	},
	ActionRoleView: {
		Action:                       ActionRoleView,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
	},
	ActionRoleBindingView: {
		Action:                       ActionRoleBindingView,
		ResourceType:                 ResourceInstitution,
		CompatibleResourceTypes:      []ResourceType{ResourceAcademicUnit, ResourceClass},
		InheritInstitutionScope:      true,
		InheritAcademicUnitScopes:    true,
		PersonalAccessTokenForbidden: true,
		AcademicAudit:                true,
	},
	ActionRoleBindingManage: {
		Action:                       ActionRoleBindingManage,
		ResourceType:                 ResourceInstitution,
		CompatibleResourceTypes:      []ResourceType{ResourceAcademicUnit, ResourceClass},
		InheritInstitutionScope:      true,
		InheritAcademicUnitScopes:    true,
		PersonalAccessTokenForbidden: true,
		AcademicAudit:                true,
	},
	ActionAuditView: {
		Action:                  ActionAuditView,
		ResourceType:            ResourceInstitution,
		InheritInstitutionScope: true,
	},
	ActionAcademicAuditView: {
		Action:                    ActionAcademicAuditView,
		ResourceType:              ResourceAcademicUnit,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionUserView: {
		Action:                    ActionUserView,
		ResourceType:              ResourceUser,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionUserManage: {
		Action:                  ActionUserManage,
		ResourceType:            ResourceUser,
		InheritInstitutionScope: true,
	},
	ActionUserProfilePictureManage: {
		Action:                  ActionUserProfilePictureManage,
		ResourceType:            ResourceUser,
		InheritInstitutionScope: true,
	},
	ActionSessionView: {
		Action:                  ActionSessionView,
		ResourceType:            ResourceUser,
		InheritInstitutionScope: true,
	},
	ActionSessionManage: {
		Action:                  ActionSessionManage,
		ResourceType:            ResourceUser,
		InheritInstitutionScope: true,
	},
	ActionDesktopRegistrationRevoke: {
		Action:                       ActionDesktopRegistrationRevoke,
		ResourceType:                 ResourceUser,
		RelationshipOnly:             true,
		PersonalAccessTokenForbidden: true,
	},
	ActionJobView: {
		Action:                  ActionJobView,
		ResourceType:            ResourceInstitution,
		InheritInstitutionScope: true,
	},
	ActionJobManage: {
		Action:                  ActionJobManage,
		ResourceType:            ResourceInstitution,
		InheritInstitutionScope: true,
	},
	ActionMailView: {
		Action:                  ActionMailView,
		ResourceType:            ResourceInstitution,
		InheritInstitutionScope: true,
	},
	ActionMailManage: {
		Action:                  ActionMailManage,
		ResourceType:            ResourceInstitution,
		InheritInstitutionScope: true,
	},
	ActionMailRekey: {
		Action:                       ActionMailRekey,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
	},
	ActionMFARekey: {
		Action:                       ActionMFARekey,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
	},
	ActionExamCreate: {
		Action:                    ActionExamCreate,
		ResourceType:              ResourceAcademicUnit,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamCreateOverride: {
		Action:                    ActionExamCreateOverride,
		ResourceType:              ResourceAcademicUnit,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamView: {
		Action:                    ActionExamView,
		ResourceType:              ResourceExam,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamViewOverride: {
		Action:                    ActionExamViewOverride,
		ResourceType:              ResourceExam,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamManage: {
		Action:                    ActionExamManage,
		ResourceType:              ResourceExam,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamManageOverride: {
		Action:                    ActionExamManageOverride,
		ResourceType:              ResourceExam,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamPublish: {
		Action:                    ActionExamPublish,
		ResourceType:              ResourceExam,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamPublishOverride: {
		Action:                    ActionExamPublishOverride,
		ResourceType:              ResourceExam,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamSittingCreate: {
		Action:                    ActionExamSittingCreate,
		ResourceType:              ResourceExam,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamSittingCreateOverride: {
		Action:                    ActionExamSittingCreateOverride,
		ResourceType:              ResourceExam,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamSittingView: {
		Action:                    ActionExamSittingView,
		ResourceType:              ResourceExamSitting,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamSittingViewOverride: {
		Action:                    ActionExamSittingViewOverride,
		ResourceType:              ResourceExamSitting,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamSittingManage: {
		Action:                    ActionExamSittingManage,
		ResourceType:              ResourceExamSitting,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamSittingManageOverride: {
		Action:                    ActionExamSittingManageOverride,
		ResourceType:              ResourceExamSitting,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamSittingParticipate: {
		Action:           ActionExamSittingParticipate,
		ResourceType:     ResourceExamSitting,
		RelationshipOnly: true,
	},
	ActionExamAttemptBrowserActivityView: {
		Action:                    ActionExamAttemptBrowserActivityView,
		ResourceType:              ResourceExamSitting,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},

	ActionSubmissionView: {
		Action:                    ActionSubmissionView,
		ResourceType:              ResourceSubmission,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionSubmissionViewOverride: {
		Action:                    ActionSubmissionViewOverride,
		ResourceType:              ResourceSubmission,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionSubmissionReview: {
		Action:                    ActionSubmissionReview,
		ResourceType:              ResourceSubmission,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionSubmissionReviewOverride: {
		Action:                    ActionSubmissionReviewOverride,
		ResourceType:              ResourceSubmission,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionSubmissionRelease: {
		Action:                    ActionSubmissionRelease,
		ResourceType:              ResourceSubmission,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionSubmissionReleaseOverride: {
		Action:                    ActionSubmissionReleaseOverride,
		ResourceType:              ResourceSubmission,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
	},
	ActionExamRecordsComplete: {
		Action:                       ActionExamRecordsComplete,
		ResourceType:                 ResourceExam,
		CompatibleResourceTypes:      []ResourceType{ResourceExamSitting, ResourceSubmission},
		InheritInstitutionScope:      true,
		InheritAcademicUnitScopes:    true,
		PersonalAccessTokenForbidden: true,
	},
	ActionExamRecordsCompleteOverride: {
		Action:                       ActionExamRecordsCompleteOverride,
		ResourceType:                 ResourceExam,
		CompatibleResourceTypes:      []ResourceType{ResourceExamSitting, ResourceSubmission},
		InheritInstitutionScope:      true,
		InheritAcademicUnitScopes:    true,
		PersonalAccessTokenForbidden: true,
	},
	ActionExamRecordsExport: {
		Action:                       ActionExamRecordsExport,
		ResourceType:                 ResourceExam,
		CompatibleResourceTypes:      []ResourceType{ResourceExamSitting, ResourceSubmission},
		InheritInstitutionScope:      true,
		InheritAcademicUnitScopes:    true,
		PersonalAccessTokenForbidden: true,
	},
	ActionExamRecordsExportOverride: {
		Action:                       ActionExamRecordsExportOverride,
		ResourceType:                 ResourceExam,
		CompatibleResourceTypes:      []ResourceType{ResourceExamSitting, ResourceSubmission},
		InheritInstitutionScope:      true,
		InheritAcademicUnitScopes:    true,
		PersonalAccessTokenForbidden: true,
	},
	ActionExamRecordsHold: {
		Action:                       ActionExamRecordsHold,
		ResourceType:                 ResourceExam,
		CompatibleResourceTypes:      []ResourceType{ResourceExamSitting, ResourceSubmission},
		InheritInstitutionScope:      true,
		InheritAcademicUnitScopes:    true,
		PersonalAccessTokenForbidden: true,
	},
	ActionExamRecordsHoldOverride: {
		Action:                       ActionExamRecordsHoldOverride,
		ResourceType:                 ResourceExam,
		CompatibleResourceTypes:      []ResourceType{ResourceExamSitting, ResourceSubmission},
		InheritInstitutionScope:      true,
		InheritAcademicUnitScopes:    true,
		PersonalAccessTokenForbidden: true,
	},
	ActionRetentionHoldRelease: {
		Action:                       ActionRetentionHoldRelease,
		ResourceType:                 ResourceExam,
		CompatibleResourceTypes:      []ResourceType{ResourceExamSitting, ResourceSubmission},
		InheritInstitutionScope:      true,
		InheritAcademicUnitScopes:    true,
		PersonalAccessTokenForbidden: true,
		SystemAdministratorOnly:      true,
	},
	ActionAcademicUnitView: {
		Action:                    ActionAcademicUnitView,
		ResourceType:              ResourceAcademicUnit,
		CompatibleResourceTypes:   []ResourceType{ResourceInstitution},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionAcademicUnitManage: {
		Action:                    ActionAcademicUnitManage,
		ResourceType:              ResourceAcademicUnit,
		CompatibleResourceTypes:   []ResourceType{ResourceInstitution},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionAcademicUnitMembersView: {
		Action:                    ActionAcademicUnitMembersView,
		ResourceType:              ResourceAcademicUnit,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionAcademicUnitMembersManage: {
		Action:                    ActionAcademicUnitMembersManage,
		ResourceType:              ResourceAcademicUnit,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionAcademicPeriodView: {
		Action:                    ActionAcademicPeriodView,
		ResourceType:              ResourceAcademicPeriod,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionAcademicPeriodManage: {
		Action:                    ActionAcademicPeriodManage,
		ResourceType:              ResourceAcademicPeriod,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionProgrammeView: {
		Action:                    ActionProgrammeView,
		ResourceType:              ResourceProgramme,
		CompatibleResourceTypes:   []ResourceType{ResourceAcademicUnit},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionProgrammeManage: {
		Action:                    ActionProgrammeManage,
		ResourceType:              ResourceProgramme,
		CompatibleResourceTypes:   []ResourceType{ResourceAcademicUnit},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionProgrammeLevelView: {
		Action:                    ActionProgrammeLevelView,
		ResourceType:              ResourceProgrammeLevel,
		CompatibleResourceTypes:   []ResourceType{ResourceProgramme},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionProgrammeLevelManage: {
		Action:                    ActionProgrammeLevelManage,
		ResourceType:              ResourceProgrammeLevel,
		CompatibleResourceTypes:   []ResourceType{ResourceProgramme},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionClassView: {
		Action:                    ActionClassView,
		ResourceType:              ResourceClass,
		CompatibleResourceTypes:   []ResourceType{ResourceProgrammeLevel, ResourceAcademicUnit},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionClassManage: {
		Action:                    ActionClassManage,
		ResourceType:              ResourceClass,
		CompatibleResourceTypes:   []ResourceType{ResourceProgrammeLevel},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionClassMembersView: {
		Action:                    ActionClassMembersView,
		ResourceType:              ResourceClass,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionClassMembersManage: {
		Action:                    ActionClassMembersManage,
		ResourceType:              ResourceClass,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionAcademicProgressionManage: {
		Action:                    ActionAcademicProgressionManage,
		ResourceType:              ResourceClass,
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionAccessPolicyView: {
		Action:                       ActionAccessPolicyView,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
	},
	ActionAccessPolicyManage: {
		Action:                       ActionAccessPolicyManage,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
	},
	ActionRetentionPolicyView: {
		Action:                       ActionRetentionPolicyView,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
	},
	ActionRetentionPolicyManage: {
		Action:                       ActionRetentionPolicyManage,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
		SystemAdministratorOnly:      true,
	},
	ActionRetentionCleanupManage: {
		Action:                       ActionRetentionCleanupManage,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
		SystemAdministratorOnly:      true,
	},
	ActionUserMFAReset: {
		Action:                       ActionUserMFAReset,
		ResourceType:                 ResourceUser,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
		SystemAdministratorOnly:      true,
	},
	ActionDesktopCompatibilityPolicyManage: {
		Action:                       ActionDesktopCompatibilityPolicyManage,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
		SystemAdministratorOnly:      true,
	},
	ActionPluginManage: {
		Action:                       ActionPluginManage,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
		SystemAdministratorOnly:      true,
	},
	ActionEnvironmentProfileManage: {
		Action:                       ActionEnvironmentProfileManage,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
		SystemAdministratorOnly:      true,
	},
	ActionAssistantProfileManage: {
		Action:                       ActionAssistantProfileManage,
		ResourceType:                 ResourceInstitution,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
		SystemAdministratorOnly:      true,
	},
	ActionInvitationView: {
		Action:                    ActionInvitationView,
		ResourceType:              ResourceInstitution,
		CompatibleResourceTypes:   []ResourceType{ResourceAcademicUnit, ResourceClass},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionInvitationCreate: {
		Action:                    ActionInvitationCreate,
		ResourceType:              ResourceInstitution,
		CompatibleResourceTypes:   []ResourceType{ResourceAcademicUnit, ResourceClass},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionInvitationManage: {
		Action:                    ActionInvitationManage,
		ResourceType:              ResourceInstitution,
		CompatibleResourceTypes:   []ResourceType{ResourceAcademicUnit, ResourceClass},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionOnboardingBatchView: {
		Action:                    ActionOnboardingBatchView,
		ResourceType:              ResourceInstitution,
		CompatibleResourceTypes:   []ResourceType{ResourceAcademicUnit, ResourceClass},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionOnboardingBatchManage: {
		Action:                    ActionOnboardingBatchManage,
		ResourceType:              ResourceInstitution,
		CompatibleResourceTypes:   []ResourceType{ResourceAcademicUnit, ResourceClass},
		InheritInstitutionScope:   true,
		InheritAcademicUnitScopes: true,
		AcademicAudit:             true,
	},
	ActionExternalIdentityManage: {
		Action:                       ActionExternalIdentityManage,
		ResourceType:                 ResourceUser,
		InheritInstitutionScope:      true,
		PersonalAccessTokenForbidden: true,
	},
}

func DefinitionForAction(action Action) (ActionDefinition, bool) {
	definition, ok := actionDefinitions[action]
	return definition, ok
}

// AllActions returns, in stable order, every action of the protected system
// administrator role: system-administrator-only actions included,
// relationship-only ones excluded.
func AllActions() []string {
	actions := make([]string, 0, len(actionDefinitions))
	for action, definition := range actionDefinitions {
		if definition.RelationshipOnly {
			continue
		}
		actions = append(actions, string(action))
	}
	sort.Strings(actions)
	return actions
}

// IsSystemAdministratorAction reports whether action belongs to the protected
// built-in role, which excludes relationship-only capabilities.
func IsSystemAdministratorAction(action string) bool {
	definition, ok := actionDefinitions[Action(action)]
	return ok && !definition.RelationshipOnly
}

func IsKnownAction(action string) bool {
	_, ok := actionDefinitions[Action(action)]
	return ok
}

// IsGrantableAction reports whether action may appear in a role or token
// scope. Relationship-only capabilities are known but never transferable.
func IsGrantableAction(action string) bool {
	definition, ok := actionDefinitions[Action(action)]
	return ok && !definition.RelationshipOnly && !definition.SystemAdministratorOnly
}

// IsPersonalAccessTokenAction reports whether a role-grantable action may also
// appear in a personal access token's ceiling.
func IsPersonalAccessTokenAction(action string) bool {
	definition, ok := actionDefinitions[Action(action)]
	return ok &&
		!definition.RelationshipOnly &&
		!definition.SystemAdministratorOnly &&
		!definition.PersonalAccessTokenForbidden
}

// PersonalAccessTokenActions returns, in stable order, every action a personal
// access token scope may hold.
func PersonalAccessTokenActions() []string {
	actions := make([]string, 0, len(actionDefinitions))
	for action := range actionDefinitions {
		if IsPersonalAccessTokenAction(string(action)) {
			actions = append(actions, string(action))
		}
	}
	sort.Strings(actions)
	return actions
}

// ResourceTypes returns, in stable order, the action's own resource type and
// each compatible owning scope.
func (d ActionDefinition) ResourceTypes() []ResourceType {
	types := append([]ResourceType{d.ResourceType}, d.CompatibleResourceTypes...)
	slices.Sort(types)
	return slices.Compact(types)
}

// Validate checks that the resource has a known type and a valid opaque ID.
func (r Resource) Validate() error {
	const where = "Resource.Validate"
	if !IsValidOpaqueID(r.ID) {
		return invalidModelError(where, "resource", "id", "must be a valid identifier", "")
	}
	if !slices.Contains(resourceTypes, r.Type) {
		return invalidModelError(where, "resource", "type", "has an unknown value", "id="+r.ID)
	}
	return nil
}
