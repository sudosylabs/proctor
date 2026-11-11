// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package store declares what the application may ask of durable storage. It
// holds interfaces and plain input and result types only. The PostgreSQL
// implementation is store/sqlstore, and no caller sees SQL or a transaction.
//
// # Reading the contracts
//
// There is one contract, an interface named <Model>Store, per aggregate, in
// the file named for its topic: ClassStore in academic.go, ExamSittingStore in
// exam_sitting.go. store.go holds only Catalog, which lists every contract,
// and Store, which adds the lifecycle methods the node runtime uses. errors.go
// holds the typed errors a contract returns.
//
// A mutation is one named method that takes one pointer input and runs in one
// transaction, so everything it writes commits together or not at all. Input
// and result types follow a small vocabulary:
//
//   - <Model>Creation, <Model>Update, <Model>Archive: the input of a create,
//     update or archive. It carries the audit reference (AuditEventID and
//     AuditAt, or a whole AuditEvent) so that the change and its audit record
//     commit together, and ExpectedRevision when the write is fenced.
//   - <Topic>Mutation: the same fence and audit reference for a change that is
//     not a plain create, update or archive.
//   - <Topic>Completion: the input that finishes work begun earlier, such as a
//     Job attempt or an audit attempt.
//   - <Topic>ListOptions: the filters, limit and position of a listing.
//   - <Topic>Page: one page of a listing and whether more follows.
//   - <Operation>Result: what an operation returns when a model value is not
//     enough, for example whether a command was replayed.
//   - <Topic>Snapshot and <Topic>Summary: state read together at one moment,
//     and a reduced projection that leaves private fields out.
//
// Older names do not all follow this; use it for new ones.
//
// Some result types have Consistent predicates (Consistent, ConsistentWith and
// a few longer forms). They are part of the contract: they say how the parts of
// a result belong together and to the IDs the caller asked for, and the
// conformance suite in storetest asserts them on every real result. The
// application does not re-check them. It checks only a transition's state,
// revision and time postconditions, through the narrower predicate declared
// beside the full one (Reallowed, Sealed, Renewed and the like), and reports
// false as an unavailable Store, never as "not found".
//
// # Adding an operation
//
//  1. Add the method and its input and result types to the contract in its
//     topic file, with a comment that says what it guarantees.
//  2. Implement it on the SQL<Model>Store in sqlstore/<model>_store.go.
//  3. Add a subtest to the contract's suite in storetest; sqlstore runs the
//     suites against PostgreSQL (make -C server integration-postgres).
//  4. Run make -C server generate to regenerate the timing, retry and cache
//     layers. A method name the timing layer has not seen needs a constant in
//     timerlayer/policy.go, and a read is retried or cached only when added by
//     hand to retrylayer/stores.go or localcachelayer/local_cache.go.
package store

import (
	"context"
)

// Catalog exposes the complete family of authoritative persistence contracts
// without lifecycle authority. Application construction borrows this view;
// the node runtime and Platform retain validation and closure ownership.
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
	ExamResource() ExamResourceStore
	ExamCorrection() ExamCorrectionStore
	ExamStarterWorkspace() ExamStarterWorkspaceStore
	Class() ClassStore
	User() UserStore
	UserSettings() UserSettingsStore
	File() FileStore
	Job() JobStore
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
	RetentionPolicy() RetentionPolicyStore
	Retention() RetentionStore
	ClusterDiscovery() ClusterDiscoveryStore
	ServingNodeLease() ServingNodeLeaseStore
	CommandOutcome() CommandOutcomeStore
}

// Store is the root persistence contract used by the application and platform.
// Concrete adapters expose each model store through this interface so callers
// do not depend on PostgreSQL implementation types. It is the Catalog plus the
// lifecycle and schema authority that only the node runtime holds.
type Store interface {
	Catalog

	Ping(context.Context) error
	GetDBSchemaVersion(context.Context) (int, error)
	GetLocalSchemaVersion() (int, error)
	ValidateSchema(context.Context) error
	Close() error
}
