// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import "errors"

// resourceApplications is the fully resolved application graph consumed by
// the HTTP resource inventory. Construction validates it once; resource
// declaration performs no dependency discovery or fallback selection.
type resourceApplications struct {
	authenticator Authenticator

	registration            RegistrationApplication
	authentication          authenticationEntryApplication
	authenticationMethods   authenticationMethodApplication
	desktopAuthorization    DesktopAuthorization
	desktopReauthentication DesktopReauthentication
	desktopRegistrations    DesktopRegistrations
	externalAuthentication  externalAuthenticationEntryApplication
	browserInvitations      BrowserInvitationApplication

	userProfiles            UserProfileApplication
	userAccounts            UserAccountApplication
	userEmails              UserEmailApplication
	userSettings            UserSettingsApplication
	accountStates           AccountStateApplication
	sessionAdministration   SessionAdministrationApplication
	sessions                Sessions
	sessionReauthentication SessionReauthentication
	mfa                     MFA
	mfaAdministration       MFAAdministration
	mfaKeys                 MFAKeyApplication
	personalAccessTokens    PersonalAccessTokens

	institutions        InstitutionApplication
	academicUnits       AcademicUnitApplication
	programmes          ProgrammeApplication
	programmeLevels     ProgrammeLevelApplication
	academicPeriods     AcademicPeriodApplication
	classes             ClassApplication
	affiliations        AffiliationApplication
	academicUnitMembers AcademicUnitMemberApplication
	classMembers        ClassMemberApplication

	exams                ExamApplication
	examRevisions        ExamRevisionApplication
	examSittings         ExamSittingApplication
	examCorrections      ExamSittingCorrectionApplication
	examResources        ExamResourceApplication
	examStarterWorkspace ExamStarterWorkspaceApplication
	examAttempts         ExamAttemptApplication
	examIntegrityReviews ExamIntegrityReviewApplication

	invitations                   InvitationApplication
	onboardingImports             OnboardingImportApplication
	studentProgressions           StudentProgressionApplication
	academicAdministrationBatches AcademicAdministrationBatchApplication
	roles                         RoleApplication
	roleBindings                  RoleBindingApplication
	accessPolicy                  AccessPolicyApplication
	desktopCompatibility          DesktopCompatibilityApplication
	retentionPolicy               RetentionPolicyApplication
	retention                     RetentionApplication
	examRecords                   ExamRecordsApplication
	examExports                   ExamExportsApplication

	search               SearchApplication
	administrationCounts AdministrationCountsApplication

	audit     AuditListingApplication
	jobs      JobOperationsApplication
	mail      MailApplication
	bootstrap BootstrapApplication
}

func resolveResourceApplications(options Options) (resourceApplications, error) {
	if options.Identity == nil {
		return resourceApplications{}, errors.New("identity application is required")
	}
	identity := options.Identity
	// These assignments are deliberately compile-time projections. Adding an
	// identity resource requires changing its declared contract, not a runtime
	// assertion hidden in catalog construction.
	var authenticator Authenticator = identity
	var authentication authenticationEntryApplication = identity
	var authenticationMethods authenticationMethodApplication = identity
	var desktopAuthorization DesktopAuthorization = identity
	var desktopReauthentication DesktopReauthentication = identity
	var desktopRegistrations DesktopRegistrations = identity
	var externalAuthentication externalAuthenticationEntryApplication = identity
	var sessions Sessions = identity
	var mfa MFA = identity
	var mfaAdministration MFAAdministration = identity
	var mfaKeys MFAKeyApplication = identity
	var personalAccessTokens PersonalAccessTokens = identity
	var userEmails UserEmailApplication = identity
	var desktopCompatibility DesktopCompatibilityApplication = identity
	var sessionReauthentication SessionReauthentication = identity

	if err := requireResourceApplicationOptions(options); err != nil {
		return resourceApplications{}, err
	}

	return resourceApplications{
		authenticator: authenticator,
		registration:  options.Registration, authentication: authentication, authenticationMethods: authenticationMethods,
		desktopAuthorization: desktopAuthorization, externalAuthentication: externalAuthentication,
		desktopReauthentication: desktopReauthentication,
		desktopRegistrations:    desktopRegistrations,
		browserInvitations:      options.BrowserInvitations,
		userProfiles:            options.UserProfiles, userAccounts: options.UserAccounts, userEmails: userEmails, userSettings: options.UserSettings,
		accountStates: options.AccountStates, sessionAdministration: options.SessionAdministrations,
		sessions: sessions, mfa: mfa, personalAccessTokens: personalAccessTokens,
		mfaAdministration:       mfaAdministration,
		mfaKeys:                 mfaKeys,
		sessionReauthentication: sessionReauthentication,
		institutions:            options.Institutions, academicUnits: options.AcademicUnits,
		programmes: options.Programmes, programmeLevels: options.ProgrammeLevels,
		academicPeriods: options.AcademicPeriods, classes: options.Classes,
		affiliations: options.Affiliations, academicUnitMembers: options.AcademicUnitMembers,
		classMembers: options.ClassMembers,
		exams:        options.Exams, examRevisions: options.ExamRevisions, examSittings: options.ExamSittings,
		examCorrections: options.ExamCorrections, examResources: options.ExamResources,
		examStarterWorkspace: options.ExamStarterWorkspaces, examAttempts: options.ExamAttempts,
		examIntegrityReviews: options.ExamIntegrityReviews,
		invitations:          options.Invitations, onboardingImports: options.OnboardingImports,
		studentProgressions:           options.StudentProgressions,
		academicAdministrationBatches: options.AcademicAdministrationBatches,
		roles:                         options.Roles, roleBindings: options.RoleBindings, accessPolicy: options.AccessPolicy,
		desktopCompatibility: desktopCompatibility,
		retentionPolicy:      options.RetentionPolicy,
		retention:            options.Retention,
		examRecords:          options.ExamRecords,
		examExports:          options.ExamExports,
		search:               options.Search,
		administrationCounts: options.AdministrationCounts,
		audit:                options.AuditListings, jobs: options.Jobs, mail: options.Mail, bootstrap: options.Bootstrap,
	}, nil
}

// requireResourceApplicationOptions rejects Options missing a focused
// application capability, reporting the first absent one.
func requireResourceApplicationOptions(options Options) error {
	required := []struct {
		missing bool
		message string
	}{
		{options.AcademicUnits == nil, "academic unit reads are required"},
		{options.Institutions == nil, "institution application is required"},
		{options.Programmes == nil, "programme application is required"},
		{options.ProgrammeLevels == nil, "programme level application is required"},
		{options.AcademicPeriods == nil, "academic period application is required"},
		{options.Classes == nil, "class application is required"},
		{options.Affiliations == nil, "affiliation application is required"},
		{options.AcademicUnitMembers == nil, "academic unit member application is required"},
		{options.ClassMembers == nil, "class member application is required"},
		{options.Invitations == nil, "invitation application is required"},
		{options.BrowserInvitations == nil, "browser invitation application is required"},
		{options.Registration == nil, "registration application is required"},
		{options.OnboardingImports == nil, "onboarding import application is required"},
		{options.StudentProgressions == nil, "student progression application is required"},
		{options.AcademicAdministrationBatches == nil, "academic administration batch application is required"},
		{options.UserProfiles == nil, "user profile application is required"},
		{options.UserAccounts == nil, "user account application is required"},
		{options.UserSettings == nil, "user settings application is required"},
		{options.AccountStates == nil, "account state application is required"},
		{options.SessionAdministrations == nil, "session administration application is required"},
		{options.Roles == nil, "role application is required"},
		{options.RoleBindings == nil, "role binding application is required"},
		{options.AuditListings == nil, "audit listing application is required"},
		{options.Bootstrap == nil, "bootstrap application is required"},
		{options.AccessPolicy == nil, "access policy application is required"},
		{options.RetentionPolicy == nil, "retention policy application is required"},
		{options.Retention == nil, "retention application is required"},
		{options.Search == nil, "search application is required"},
		{options.AdministrationCounts == nil, "administration counts application is required"},
		{options.Jobs == nil, "job operations application is required"},
		{options.Mail == nil, "mail application is required"},
		{options.Exams == nil, "exam application is required"},
		{options.ExamRevisions == nil, "exam revision application is required"},
		{options.ExamSittings == nil, "exam sitting application is required"},
		{options.ExamCorrections == nil, "exam sitting correction application is required"},
		{options.ExamResources == nil, "exam resource application is required"},
		{options.ExamStarterWorkspaces == nil, "exam starter workspace application is required"},
		{options.ExamAttempts == nil, "exam attempt application is required"},
		{options.ExamIntegrityReviews == nil, "exam integrity review application is required"},
		{options.ExamRecords == nil, "exam records application is required"},
		{options.ExamExports == nil, "exam exports application is required"},
	}
	for _, dependency := range required {
		if dependency.missing {
			return errors.New(dependency.message)
		}
	}
	return nil
}
