// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package store declares the persistence contracts: one <Model>Store
// interface per aggregate, plus plain input and result types. store/sqlstore
// implements them on PostgreSQL. Each mutation runs in one transaction, so its
// audit record commits with the change. Consistent predicates on result types
// are part of the contract and storetest asserts them. After adding a method,
// run make -C server generate; a new method name also needs a constant in
// timerlayer/policy.go.
package store

import (
	"context"
)

// Catalog is every contract without the lifecycle methods, which only the
// node runtime may call.
type Catalog interface {
	Institution() InstitutionStore
	AcademicUnit() AcademicUnitStore
	Programme() ProgrammeStore
	ProgrammeLevel() ProgrammeLevelStore
	AcademicPeriod() AcademicPeriodStore
	ExamAuthoring() ExamAuthoringStore
	ExamRevision() ExamRevisionStore
	ExamSitting() ExamSittingStore
	ExamAttempt() ExamAttemptStore
	ExamFocusLoss() ExamFocusLossStore
	SecurityPreflight() SecurityPreflightStore
	NativeDelivery() NativeDeliveryStore
	BrowserDelivery() BrowserDeliveryStore
	DeliveryBudget() DeliveryBudgetStore
	ExamAttemptWorkspace() ExamAttemptWorkspaceStore
	ExamSubmission() ExamSubmissionStore
	ExamIntegrityReview() ExamIntegrityReviewStore
	ExamRecords() ExamRecordsStore
	ExamExport() ExamExportStore
	AuditExport() AuditExportStore
	ExamResource() ExamResourceStore
	ExamCorrection() ExamCorrectionStore
	ExamStarterWorkspace() ExamStarterWorkspaceStore
	Class() ClassStore
	User() UserStore
	UserSettings() UserSettingsStore
	File() FileStore
	Job() JobStore
	DomainEvent() DomainEventStore
	Mail() MailStore
	ExternalIdentity() ExternalIdentityStore
	ExternalLoginState() ExternalLoginStateStore
	BrowserAuthentication() BrowserAuthenticationStore
	DesktopRegistration() DesktopRegistrationStore
	UserToken() UserTokenStore
	Invitation() InvitationStore
	OnboardingImport() OnboardingImportStore
	PersonalAccessToken() PersonalAccessTokenStore
	MFA() MFAStore
	Affiliation() AffiliationStore
	AcademicUnitMember() AcademicUnitMemberStore
	ClassMember() ClassMemberStore
	PasswordCredential() PasswordCredentialStore
	Session() SessionStore
	SessionCredential() SessionCredentialStore
	Role() RoleStore
	RoleBinding() RoleBindingStore
	Audit() AuditStore
	Installation() InstallationStore
	AccessPolicy() AccessPolicyStore
	DesktopCompatibilityPolicy() DesktopCompatibilityPolicyStore
	PluginSettings() PluginSettingsStore
	EnvironmentProfile() EnvironmentProfileStore
	AssistantProfile() AssistantProfileStore
	AssistantTranscript() AssistantTranscriptStore
	AttemptEnvironment() AttemptEnvironmentStore
	AttemptRuntimeLease() AttemptRuntimeLeaseStore
	RetentionPolicy() RetentionPolicyStore
	Retention() RetentionStore
	ClusterDiscovery() ClusterDiscoveryStore
	ServingNodeLease() ServingNodeLeaseStore
	CommandOutcome() CommandOutcomeStore
}

// Store is the Catalog plus the schema and lifecycle methods held by the node
// runtime.
type Store interface {
	Catalog

	Ping(context.Context) error
	GetDBSchemaVersion(context.Context) (int, error)
	GetLocalSchemaVersion() (int, error)
	ValidateSchema(context.Context) error
	Close() error
}
