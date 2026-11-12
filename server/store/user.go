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

// UserListOptions is a bounded persistence query. IncludeDisabled is effective
// only with an explicit InstitutionWide visibility scope.
//
// AffiliationKinds keeps Users holding at least one current Affiliation of a
// listed kind; AcademicUnitID keeps Users with a current Academic Unit
// membership in that unit or an unarchived descendant. Both narrow the
// visibility result and are evaluated at Visibility.ActiveAt, which they
// require. A scoped visibility answers no rows for an AcademicUnitID outside
// its user.view subtrees (AcademicUnitRootIDs), so the filter never discloses
// membership the scope could not read.
//
// RoleIDs keeps Users holding a current Role Binding, at Visibility.ActiveAt,
// of one of at most 16 listed unarchived Roles inside RoleBindingVisibility:
// the caller's role_binding.view scopes (InstitutionWide, or the
// AcademicUnitRootIDs subtrees and their Classes plus ClassIDs). The filter
// therefore matches only bindings the directory summary could show, and it
// still narrows Visibility.
//
// OnlyDisabled keeps only disabled Users. Like IncludeDisabled it is
// effective only with an InstitutionWide visibility: a scoped visibility
// never sees a disabled User, so it answers no rows.
//
// NeverSignedIn, LastActivityAfter (inclusive) and LastActivityBefore
// (exclusive) read the User's last activity, which only an InstitutionWide
// visibility may read; any of them with another visibility is invalid input.
// A User's last activity is first set when a Session is issued to them and is
// only ever advanced afterwards, so NeverSignedIn (no last activity) means no
// Session was ever issued. It excludes both bounds, which never match such a
// User, and when both bounds are set the first precedes the second.
//
// Order picks the keyset and Descending reverses it; the zero Order is
// username order. A continuation sets AfterID, the previous page's last User,
// with its key in the same order: AfterUsername (username), AfterCreatedAt
// (created), or AfterLastActivityAt (last_active, invalid for a User never
// active, who sorts as the least recent). The name order reads the key of
// AfterID itself, the presented name the database folds for sorting, so a
// continuation never carries a second spelling of it.
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
	Visibility                   UserVisibilityScope
}

// MaximumUserListRoles bounds the Roles one User directory filter names.
const MaximumUserListRoles = 16

// UserOrder names a User directory order. Each is ascending by its key, then
// by ID.
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

// UserVisibilityScope constrains user list/search results in persistence.
// Scoped and zero-value visibility always exclude disabled Users.
type UserVisibilityScope struct {
	// InstitutionWide and AcademicUnitRootIDs are user.view relationship
	// visibility: current Academic Unit membership, Class membership, and
	// active-Role bindings are visibility anchors.
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	// For UserStore directory queries, ClassMemberInstitutionWide,
	// ClassMemberAcademicUnitRootIDs, and ClassIDs are class.members.view roster
	// visibility. They expose current Class members only and never Academic Unit
	// members or Role-Binding holders.
	ClassMemberInstitutionWide     bool
	ClassMemberAcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs                       []model.ClassID
	// ActiveAt is the UTC microsecond decision time shared with current role
	// authorization. Visibility anchors include their start and exclude their end.
	ActiveAt time.Time
}

// UserDirectorySummaryOptions selects the bounded directory summary of one
// page of Users. Every User's current Affiliations are read, at most
// AffiliationLimit of them; Academic Units come from current memberships in
// unarchived units, at most AcademicUnitLimit of them, and a scoped
// Visibility reads only units inside its user.view subtrees
// (AcademicUnitRootIDs), so the summary never names a relationship outside the
// scope. Current means effective at Visibility.ActiveAt, which is required.
type UserDirectorySummaryOptions struct {
	UserIDs           []model.UserID
	Visibility        UserVisibilityScope
	AffiliationLimit  int
	AcademicUnitLimit int
}

// UserDirectorySummary is one User's bounded directory summary. Affiliations
// are ordered by start then kind and AcademicUnits by display name then ID;
// each More counts the current entries past its limit.
type UserDirectorySummary struct {
	UserID            model.UserID
	Affiliations      []model.AffiliationKind
	AffiliationsMore  int
	AcademicUnits     []UserDirectoryAcademicUnit
	AcademicUnitsMore int
}

// UserDirectoryAcademicUnit names one Academic Unit in a directory summary.
type UserDirectoryAcademicUnit struct {
	ID          model.AcademicUnitID
	DisplayName string
}

// UserIdentity names one User beside a relationship that already makes the
// User visible, such as an Academic Unit membership listing.
type UserIdentity struct {
	ID          model.UserID
	Username    string
	DisplayName string
	Email       string
}

// UserVisibilityMatch identifies the authorized scope through which one User
// is visible. It lets callers record the exact subtree/class decision without
// assigning the target to an unrelated authorized scope when a principal has
// more than one binding.
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

// UserDisabledStateChange is the complete durable input for changing whether
// a user may operate. Disabling also revokes every active session in the same
// transaction. AuditEventID identifies an already-persisted attempt that must
// be completed successfully before any state change may commit.
type UserDisabledStateChange struct {
	ID               model.UserID
	ExpectedRevision int64
	Disabled         bool
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

// UserDisabledStateResult contains the committed user state and the minimal
// revocation facts needed for post-commit cache and realtime effects.
type UserDisabledStateResult struct {
	User               *model.User
	RevokedSessions    []*model.Session
	RevokedTokenHashes []string
}

// UserCreation is the named, import-safe transaction for creating one User.
// DefaultProfilePictureJob must be the typed, deduplicated generation intent
// prepared for User by the application. PasswordCredential is optional so
// external/imported accounts do not need to invent a local credential.
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

// PublicLocalUserRegistration is the complete anonymous self-registration
// transition. Persistence rechecks the current Access Policy and commits the
// unverified local account, initial settings work, successful audit, and
// frozen verification credential delivery as one transaction.
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

// UserStore persists login-capable accounts without their credentials.
type UserStore interface {
	// Create atomically persists the prepared User, its optional password
	// credential, and its matching active-deduplicated default-picture generation
	// intent. All future import-oriented creation must use this operation.
	Create(context.Context, *UserCreation) (*UserCreationResult, error)
	RegisterLocal(context.Context, *PublicLocalUserRegistration) (*PublicLocalUserRegistrationResult, error)
	Get(context.Context, model.UserID) (*model.User, error)
	GetByUsername(context.Context, string) (*model.User, error)
	GetByEmail(context.Context, string) (*model.User, error)
	List(context.Context, UserListOptions) ([]*model.User, error)
	// Count counts the Users List would return across every page for the
	// same filters and visibility, ignoring the keyset and limit, and stops
	// at maximum (see MaximumListCount).
	Count(context.Context, UserListOptions, int) (int, error)
	// ListDirectorySummaries reads the summary of every listed User in one
	// statement. A User with neither a current Affiliation nor a visible
	// current Academic Unit has no entry.
	ListDirectorySummaries(context.Context, UserDirectorySummaryOptions) ([]UserDirectorySummary, error)
	// ListIdentities reads the identities of at most 200 listed Users in one
	// statement, in no particular order; an unknown ID has no entry.
	ListIdentities(context.Context, []model.UserID) ([]UserIdentity, error)
	GetCurrentContext(context.Context, model.UserID, int) (*CurrentUserContext, error)
	MatchVisibility(context.Context, model.UserID, UserVisibilityScope) (UserVisibilityMatch, error)
	UpdateProfile(context.Context, *UserProfileUpdate) (*model.User, error)
	SetDisabled(context.Context, *UserDisabledStateChange) (*UserDisabledStateResult, error)
	UpdateLastLogin(context.Context, *UserLastLoginUpdate) error
	// ListDisplayNames reads the presentable name of each given unarchived
	// User: the display name, or the username when it is empty. At most
	// MaximumUserDisplayNames distinct Users are read; an archived or missing
	// User is absent from the result. It grants no authority.
	ListDisplayNames(context.Context, []model.UserID) ([]UserDisplayName, error)
}

// MaximumUserDisplayNames bounds one UserStore.ListDisplayNames read.
const MaximumUserDisplayNames = 200

// UserDisplayName is one User's presentable name.
type UserDisplayName struct {
	UserID      model.UserID
	DisplayName string
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

// CurrentUserContext is a bounded, credential-free navigation projection.
// It is never an authorization receipt and deliberately omits Role names,
// permission arrays, provider data, and examination content.
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

// ConsistentWith reports whether the context is the given User's, carries a
// username, display name and a known profile picture kind, lists at least two
// available product areas, and lists at most twenty management scopes.
//
// It is part of the Store contract: the conformance suite asserts it on
// every real result.
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
