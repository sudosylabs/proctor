// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// UserListOptions filters and pages the user directory. Every filter narrows
// Visibility; none widens it.
//
// IncludeDisabled and OnlyDisabled take effect only with an InstitutionWide
// visibility, since a scoped visibility never sees a disabled user.
//
// AffiliationKinds and AcademicUnitID (the unit or an unarchived descendant)
// match current relationships at Visibility.ActiveAt, which they require. A
// scoped visibility returns no rows for an AcademicUnitID outside its user.view
// subtrees, so the filter cannot disclose membership the scope could not read.
// RoleIDs (at most 16) matches current bindings of unarchived roles inside
// RoleBindingVisibility, the caller's role_binding.view scopes, so it matches
// only bindings the directory summary could show.
//
// MFA keeps users whose account has, or lacks, an active Proctor two-step
// credential. RoleHolder keeps the role holders the access policy preflight
// counts, at Visibility.ActiveAt, and needs an InstitutionWide visibility;
// with MFA set to UserMFADisabled the count is exactly the preflight's
// RoleHoldersWithoutMFACount.
//
// NeverSignedIn, LastActivityAfter (inclusive) and LastActivityBefore
// (exclusive) need an InstitutionWide visibility; otherwise they are invalid
// input. Last activity is first set when a session is issued, so
// NeverSignedIn means no session ever was, and it cannot be combined with the
// bounds.
//
// The zero Order is username order. A continuation sets AfterID with the key
// for the order: AfterUsername, AfterCreatedAt or AfterLastActivityAt (invalid
// for a never-active user, who sorts least recent). The name order reads
// AfterID's sort key itself, so a continuation never carries a second spelling
// of the name.
type UserListOptions struct {
	ID                           model.UserID
	Query                        string
	Order                        UserOrder
	Descending                   bool
	AfterUsername                string
	AfterCreatedAt               time.Time
	AfterLastActivityAt          model.OptionalTime
	AfterID                      model.UserID
	Limit                        int
	IncludeDisabled              bool
	OnlyDisabled                 bool
	MissingDefaultProfilePicture bool
	AffiliationKinds             []model.AffiliationKind
	AcademicUnitID               model.AcademicUnitID
	RoleIDs                      []model.RoleID
	RoleBindingVisibility        UserVisibilityScope
	NeverSignedIn                bool
	LastActivityAfter            time.Time
	LastActivityBefore           time.Time
	MFA                          UserMFAFilter
	RoleHolder                   bool
	Visibility                   UserVisibilityScope
}

// UserMFAFilter narrows the directory by two-step verification; the zero
// value applies no filter.
type UserMFAFilter string

const (
	UserMFAEnabled  UserMFAFilter = "enabled"
	UserMFADisabled UserMFAFilter = "disabled"
)

// IsValid reports whether f is a known filter; the zero value is not one.
func (f UserMFAFilter) IsValid() bool {
	return f == UserMFAEnabled || f == UserMFADisabled
}

// UserMFAState is a user's active Proctor two-step credential. EnabledAt is
// when it was activated.
type UserMFAState struct {
	UserID    model.UserID
	EnabledAt model.OptionalTime
}

// MaximumUserListRoles bounds UserListOptions.RoleIDs.
const MaximumUserListRoles = 16

// UserOrder is a directory order, ascending by its key then ID.
type UserOrder string

const (
	UserOrderUsername   UserOrder = "username"
	UserOrderName       UserOrder = "name"
	UserOrderCreated    UserOrder = "created"
	UserOrderLastActive UserOrder = "last_active"
)

// IsValid reports whether o is a known order; the zero value is not one.
func (o UserOrder) IsValid() bool {
	switch o {
	case UserOrderUsername, UserOrderName, UserOrderCreated, UserOrderLastActive:
		return true
	}
	return false
}

// UserVisibilityScope limits which users a query may see. A scoped or zero
// visibility never sees a disabled user.
type UserVisibilityScope struct {
	// InstitutionWide and AcademicUnitRootIDs are user.view visibility: a user
	// is visible through a current unit membership, class membership or role
	// binding.
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	// The ClassMember fields and ClassIDs are class.members.view visibility:
	// they show current class members only, never unit members or role
	// holders.
	ClassMemberInstitutionWide     bool
	ClassMemberAcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs                       []model.ClassID
	// ActiveAt is the instant (UTC microseconds) role authorization used;
	// relationships count over [start, end).
	ActiveAt time.Time
}

// UserDirectorySummaryOptions selects the summaries of a page of users: up to
// AffiliationLimit current affiliations and AcademicUnitLimit current
// unarchived units each, at Visibility.ActiveAt, which is required. A scoped
// visibility reads only units inside its user.view subtrees, so a summary
// never names a relationship outside the scope.
type UserDirectorySummaryOptions struct {
	UserIDs           []model.UserID
	Visibility        UserVisibilityScope
	AffiliationLimit  int
	AcademicUnitLimit int
}

// UserDirectorySummary is one user's directory summary. Affiliations are by
// start then kind, AcademicUnits by display name then ID; each More counts the
// entries past its limit.
type UserDirectorySummary struct {
	UserID            model.UserID
	Affiliations      []model.AffiliationKind
	AffiliationsMore  int
	AcademicUnits     []UserDirectoryAcademicUnit
	AcademicUnitsMore int
}

// UserDirectoryAcademicUnit is one academic unit in a directory summary.
type UserDirectoryAcademicUnit struct {
	ID          model.AcademicUnitID
	DisplayName string
}

// UserMembershipKind is what a user is a current member of.
type UserMembershipKind string

const (
	UserMembershipAcademicUnit UserMembershipKind = "academic_unit"
	UserMembershipClass        UserMembershipKind = "class"
)

// IsValid reports whether k is a known kind.
func (k UserMembershipKind) IsValid() bool {
	return k == UserMembershipAcademicUnit || k == UserMembershipClass
}

// UserMembershipListOptions pages one user's memberships current at ActiveAt
// (required): unit memberships of unarchived units inside UnitVisibility
// (academic_unit.members.view; its ClassIDs are unused), then class
// enrolments in unarchived classes, levels and programmes inside
// ClassVisibility (class.members.view). Each kind is ordered by start then
// membership ID; a continuation sets all three After fields from the previous
// page's last entry. A membership outside both visibilities is neither listed
// nor counted. Limit is 1..201.
type UserMembershipListOptions struct {
	UserID          model.UserID
	ActiveAt        time.Time
	UnitVisibility  ClassVisibilityScope
	ClassVisibility ClassVisibilityScope
	AfterKind       UserMembershipKind
	AfterStartAt    time.Time
	AfterID         string
	Limit           int
}

// UserMembership is one current membership. A unit membership names its unit;
// a class enrolment names its class, programme level, programme and academic
// period. ID is the academic unit or class membership's own ID.
type UserMembership struct {
	Kind               UserMembershipKind
	ID                 string
	StartAt            time.Time
	EndAt              model.OptionalTime
	AcademicUnitID     model.AcademicUnitID
	AcademicUnitName   string
	ClassID            model.ClassID
	ClassName          string
	ProgrammeLevelID   model.ProgrammeLevelID
	ProgrammeLevelName string
	ProgrammeID        model.ProgrammeID
	ProgrammeName      string
	AcademicPeriodID   model.AcademicPeriodID
	AcademicPeriodName string
}

// UserIdentity names a user beside a relationship that already makes them
// visible, such as a membership listing. It is not itself a visibility check.
type UserIdentity struct {
	ID          model.UserID
	Username    string
	DisplayName string
	Email       string
}

// UserVisibilityMatch is the scope through which a user is visible, so a
// caller with several bindings records the one that actually matched.
type UserVisibilityMatch struct {
	ScopeType model.RoleScopeType
	ScopeID   string
}

type UserProfileUpdate struct {
	UserID           model.UserID
	Changes          model.UserProfileChanges
	ExpectedRevision int64
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// UserDisabledStateChange disables or enables a user and completes the
// recorded audit attempt in the same commit. Disabling also revokes every
// active session.
type UserDisabledStateChange struct {
	ID               model.UserID
	ExpectedRevision int64
	Disabled         bool
	// DisabledBy and DisabledReason are recorded on a disable and cleared on
	// an enable. Either may be empty: an actorless disable names nobody.
	DisabledBy       model.UserID
	DisabledReason   string
	Capabilities     AccessDeploymentCapabilities
	Occurrence       *model.MailOccurrence
	Delivery         *model.MailDelivery
	DeliveryJob      *model.Job
	ChangedAt        time.Time
	RevocationReason model.SessionRevocationReason
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
	Command          *CommandIdempotency
	Replayed         bool
	NoOp             bool
}

// UserDisabledStateResult is the committed user and what post-commit cache
// and realtime invalidation need.
type UserDisabledStateResult struct {
	User               *model.User
	RevokedSessions    []*model.Session
	RevokedTokenHashes []string
}

// UserCreation creates one user; imports use it too. DefaultProfilePictureJob
// is the deduplicated picture-generation Job for User. PasswordCredential is
// nil for an external or imported account.
type UserCreation struct {
	User                     *model.User
	Settings                 *model.UserSettingsDocument
	PasswordCredential       *model.PasswordCredential
	DefaultProfilePictureJob *model.Job
}

type UserCreationResult struct {
	User               *model.User
	PasswordCredential *model.PasswordCredential
}

// PublicLocalUserRegistration is anonymous self-registration. The store
// rechecks the access policy and commits the unverified account, its
// settings, audit and verification mail in one transaction.
type PublicLocalUserRegistration struct {
	User                     *model.User
	Settings                 *model.UserSettingsDocument
	PasswordCredential       *model.PasswordCredential
	DefaultProfilePictureJob *model.Job
	VerificationToken        *model.UserToken
	TokenLifetime            time.Duration
	MailLifetime             time.Duration
	VerificationOccurrence   *model.MailOccurrence
	VerificationDelivery     *model.MailDelivery
	VerificationJob          *model.Job
	AuditEvent               *model.AuditEvent
}

type PublicLocalUserRegistrationResult struct {
	User       *model.User
	Token      *model.UserToken
	AuditEvent *model.AuditEvent
}

// UserStore persists accounts; credentials live in their own stores.
type UserStore interface {
	// Create persists the user, its optional password and its default-picture
	// intent atomically; every import-style creation must use it.
	Create(context.Context, *UserCreation) (*UserCreationResult, error)
	RegisterLocal(context.Context, *PublicLocalUserRegistration) (*PublicLocalUserRegistrationResult, error)
	Get(context.Context, model.UserID) (*model.User, error)
	GetByUsername(context.Context, string) (*model.User, error)
	GetByEmail(context.Context, string) (*model.User, error)
	List(context.Context, UserListOptions) ([]*model.User, error)
	// Count counts what List would return across every page, ignoring the
	// keyset and limit, capped at maximum (see MaximumListCount).
	Count(context.Context, UserListOptions, int) (int, error)
	// ListDirectorySummaries omits a user with no current affiliation and no
	// visible current academic unit.
	ListDirectorySummaries(context.Context, UserDirectorySummaryOptions) ([]UserDirectorySummary, error)
	// ListIdentities reads up to MaximumUserDisplayNames users in no particular order; an unknown
	// ID has no entry.
	ListIdentities(context.Context, []model.UserID) ([]UserIdentity, error)
	// ListMFAStates reads up to 200 users' active
	// two-step credentials in no particular order; a user without one has no
	// entry. It never reads a pending setup or recovery codes.
	ListMFAStates(context.Context, []model.UserID) ([]UserMFAState, error)
	// ListMemberships pages a user's current unit memberships and class
	// enrolments inside the given visibilities.
	ListMemberships(context.Context, UserMembershipListOptions) ([]UserMembership, error)
	GetCurrentContext(context.Context, model.UserID, int) (*CurrentUserContext, error)
	MatchVisibility(context.Context, model.UserID, UserVisibilityScope) (UserVisibilityMatch, error)
	UpdateProfile(context.Context, *UserProfileUpdate) (*model.User, error)
	SetDisabled(context.Context, *UserDisabledStateChange) (*UserDisabledStateResult, error)
	UpdateLastLogin(context.Context, *UserLastLoginUpdate) error
	// ListDisplayNames returns each unarchived user's display name, or the
	// username when it is empty, for up to MaximumUserDisplayNames distinct
	// users; archived or missing users are absent. It grants no authority.
	ListDisplayNames(context.Context, []model.UserID) ([]UserDisplayName, error)
	// ListVisibleDisplayNames is ListDisplayNames limited to users visible as
	// the directory applies the scope: a scoped visibility never names a
	// disabled user and a zero visibility names nobody.
	ListVisibleDisplayNames(context.Context, []model.UserID, UserVisibilityScope) ([]UserDisplayName, error)
}

// MaximumUserDisplayNames bounds one UserStore.ListDisplayNames read.
const MaximumUserDisplayNames = 2_000

// UserDisplayName is the display name, or the username when that is empty.
type UserDisplayName struct {
	UserID      model.UserID
	DisplayName string
	// UsernameFallback is true when DisplayName contains a username in
	// place of an unset name. Only expose it through directory visibility.
	UsernameFallback bool
}

type CurrentUserProductArea string

const (
	CurrentUserProductAreaAccount         CurrentUserProductArea = "account"
	CurrentUserProductAreaAdministration  CurrentUserProductArea = "administration"
	CurrentUserProductAreaExamManagement  CurrentUserProductArea = "exam_management"
	CurrentUserProductAreaSettings        CurrentUserProductArea = "settings"
	CurrentUserProductAreaStudentActivity CurrentUserProductArea = "student_activity"
)

type CurrentUserManagementScope struct {
	ScopeType   model.RoleScopeType
	ScopeID     string
	DisplayName string
}

type CurrentUserAttemptSelector struct {
	AttemptID model.ExamAttemptID
	SittingID model.ExamSittingID
	State     model.ExamAttemptState
}

// CurrentUserContext drives navigation only; it is never proof of
// authorization. It deliberately omits role names, permissions, provider data
// and exam content.
type CurrentUserContext struct {
	UserID                  model.UserID
	Username                string
	DisplayName             string
	ProfilePictureKind      model.ProfilePictureKind
	NoCurrentAffiliation    bool
	NoAssignedAccess        bool
	AvailableProductAreas   []CurrentUserProductArea
	ManagementScopes        []CurrentUserManagementScope
	ManagementScopesHasMore bool
	UnresolvedAttempt       *CurrentUserAttemptSelector
}

// ConsistentWith reports whether the context is the user's, with a username,
// display name, known picture kind, at least two product areas and at most
// twenty management scopes.
func (c *CurrentUserContext) ConsistentWith(userID model.UserID) bool {
	return !(c == nil ||
		c.UserID != userID ||
		c.Username == "" ||
		c.DisplayName == "" ||
		(c.ProfilePictureKind != model.ProfilePictureCustom && c.ProfilePictureKind != model.ProfilePictureDefault) ||
		len(c.AvailableProductAreas) < 2 ||
		len(c.ManagementScopes) > 20)
}

// UserLastLoginUpdate is the input to UserStore.UpdateLastLogin.
type UserLastLoginUpdate struct {
	ID model.UserID
	At time.Time
}
