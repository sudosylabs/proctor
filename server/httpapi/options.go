// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"
	"net/netip"
	"regexp"
	"time"

	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// Localizer is the narrow presentation-owned localization port. Application
// and domain failures remain stable codes; only the HTTP edge asks for prose.
type Localizer interface {
	Translate(locale, id string, args any) (string, error)
	SupportedLocales() []string
}

type Health interface {
	Live() bool
	Ready() bool
}

// Metrics is the narrow, transport-owned observation port. Route is the
// sealed template, never a request path or resource identifier.
type Metrics interface {
	HTTPStarted() func()
	ObserveHTTPRequest(route, method string, status int, duration time.Duration)
	ObserveHTTPPayload(route, method string, requestBytes, responseBytes int64)
}

// Tracing is the narrow, transport-owned tracing port. Composition supplies
// it only when tracing is enabled; package httpapi never imports a tracing
// SDK.
type Tracing interface {
	StartRequest(ctx context.Context, request TracedRequest) (context.Context, RequestSpan)
}

// TracedRequest is everything a request span may learn about a request:
// never its path, query, body, credentials, or client address.
type TracedRequest struct {
	// Header is consulted only for W3C trace context, and only when
	// ContinueParent is true.
	Header http.Header
	// Method is the bounded HTTP method.
	Method string
	// ContinueParent is true only when the connection peer is a trusted
	// proxy, so an untrusted client cannot choose a trace or force sampling.
	ContinueParent bool
	// RequestID is the request's server-assigned correlation ID.
	RequestID string
}

// RequestSpan is one request's span. SetRoute receives the sealed route
// template; End is idempotent.
type RequestSpan interface {
	SetRoute(route string)
	End(status int)
}

type AuthRequirement string
type IdempotencyRequirement string
type RouteProtocolKind string

const (
	AuthPublic                           AuthRequirement = "public"
	AuthMFARecoverySessionRequired       AuthRequirement = "mfa_recovery_session_required"
	AuthRecentMFARecoverySessionRequired AuthRequirement = "recent_mfa_recovery_session_required"
	AuthPrincipalRequired                AuthRequirement = "principal_required"
	AuthSessionRequired                  AuthRequirement = "session_required"
	AuthStrongSessionRequired            AuthRequirement = "strong_session_required"
	AuthRecentSessionRequired            AuthRequirement = "recent_session_required"
	AuthStrongRecentSessionRequired      AuthRequirement = "strong_recent_session_required"
	AuthRefreshCredentialRequired        AuthRequirement = "refresh_credential_required"
)

// AuthMFAEnrollmentSessionRequired admits an ordinary Session or one the
// Access Policy restricts to MFA enrollment, never an assisted-recovery
// Session. Only the second-factor challenge declares it.
const AuthMFAEnrollmentSessionRequired AuthRequirement = "mfa_enrollment_session_required"

const (
	IdempotencyNone     IdempotencyRequirement = "none"
	IdempotencyOptional IdempotencyRequirement = "optional"
	IdempotencyRequired IdempotencyRequirement = "required"
)

const (
	RouteProtocolRedirect        RouteProtocolKind = "redirect"
	RouteProtocolBinaryDownload  RouteProtocolKind = "binary_download"
	RouteProtocolStreamingUpload RouteProtocolKind = "streaming_upload"
	RouteProtocolUpgrade         RouteProtocolKind = "upgrade"
)

// Route is an immutable manifest entry exposed for contract and OpenAPI
// agreement checks after the catalog has been sealed.
type Route struct {
	Method       string
	Path         string
	Auth         AuthRequirement
	ErrorCodes   []string
	ProtocolName string
	ProtocolKind RouteProtocolKind
	Idempotency  IdempotencyRequirement
}

type routeMatcher struct {
	route      Route
	pathRegexp *regexp.Regexp
}

// Options contains composition-owned dependencies. New validates the complete
// set before compiling any resource route into the immutable HTTP manifest.
type Options struct {
	Logger                        *logging.Logger
	Metrics                       Metrics
	Tracing                       Tracing
	Localizer                     Localizer
	Health                        Health
	Identity                      IdentityApplication
	Search                        SearchApplication
	AdministrationCounts          AdministrationCountsApplication
	Jobs                          JobOperationsApplication
	AcademicUnits                 AcademicUnitApplication
	Institutions                  InstitutionApplication
	Programmes                    ProgrammeApplication
	ProgrammeLevels               ProgrammeLevelApplication
	AcademicPeriods               AcademicPeriodApplication
	Classes                       ClassApplication
	Affiliations                  AffiliationApplication
	AcademicUnitMembers           AcademicUnitMemberApplication
	ClassMembers                  ClassMemberApplication
	Invitations                   InvitationApplication
	BrowserInvitations            BrowserInvitationApplication
	Registration                  RegistrationApplication
	OnboardingImports             OnboardingImportApplication
	StudentProgressions           StudentProgressionApplication
	AcademicAdministrationBatches AcademicAdministrationBatchApplication
	UserProfiles                  UserProfileApplication
	UserAccounts                  UserAccountApplication
	UserSettings                  UserSettingsApplication
	AccountStates                 AccountStateApplication
	SessionAdministrations        SessionAdministrationApplication
	Roles                         RoleApplication
	RoleBindings                  RoleBindingApplication
	AuditListings                 AuditListingApplication
	Bootstrap                     BootstrapApplication
	AccessPolicy                  AccessPolicyApplication
	RetentionPolicy               RetentionPolicyApplication
	Retention                     RetentionApplication
	Mail                          MailApplication
	Exams                         ExamApplication
	ExamRevisions                 ExamRevisionApplication
	ExamSittings                  ExamSittingApplication
	ExamCorrections               ExamSittingCorrectionApplication
	ExamResources                 ExamResourceApplication
	ExamStarterWorkspaces         ExamStarterWorkspaceApplication
	ExamAttempts                  ExamAttemptApplication
	ExamIntegrityReviews          ExamIntegrityReviewApplication
	ExamRecords                   ExamRecordsApplication
	ExamExports                   ExamExportsApplication
	BuildInfo                     BuildInfo
	PublicURL                     string
	MaxBodyBytes                  int64
	// ReadTimeout and WriteTimeout repeat the server-wide connection
	// deadlines. Streaming upload and binary download routes extend them by
	// the time their declared size takes at the minimum admitted throughput.
	// Zero leaves the corresponding deadline unchanged.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// TrustedProxies lists reverse-proxy peers whose X-Forwarded-For and
	// X-Request-ID headers are accepted. Empty trusts no peer.
	TrustedProxies          []netip.Prefix
	RecentAuthenticationTTL time.Duration
	NodeID                  string
	// WebSocket is the sibling transport constructed at composition root.
	// HTTP owns only route mounting and session middleware around Accept.
	WebSocket WebSocketTransport
}

// WebSocketTransport is the narrow mount surface HTTP needs from the sibling
// websocket package. Composition supplies the concrete hub. Accept owns the
// response after invocation; a returned error must describe a pre-upgrade
// failure and must not follow a committed response.
type WebSocketTransport interface {
	Accept(
		writer http.ResponseWriter,
		request *http.Request,
		principal model.Principal,
		metadata model.RequestMetadata,
		connectionID string,
		sequence int64,
		allowMissingOrigin bool,
	) error
}

// IdentityApplication is the identity area's use cases: credential
// resolution, sign-in and recovery, Sessions, two-step verification, personal
// access tokens, Desktop authorization and compatibility, and email changes.
type IdentityApplication interface {
	Authenticator
	authenticationEntryApplication
	DesktopAuthorization
	DesktopReauthentication
	DesktopRegistrations
	externalAuthenticationEntryApplication
	authenticationMethodApplication
	Sessions
	SessionReauthentication
	PersonalAccessTokens
	MFA
	MFAAdministration
	MFAKeyApplication
	DesktopCompatibilityApplication
	UserEmailApplication
}
